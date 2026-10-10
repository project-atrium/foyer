package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/filesystem"
)

const (
	// maxImage is what one picture may weigh. PocketBase's own download
	// helper reads whatever comes, on Go's default client.
	maxImage = 15 << 20
	// fetchAtOnce bounds TMDB's image servers' share of a scan.
	fetchAtOnce = 4
)

// sizes are the sizes artwork is kept in: enough for a television, and
// PocketBase makes the smaller thumbnails from them.
var sizes = map[string]string{"poster": "w780", "backdrop": "w1280", "logo": "w500", "still": "w300"}

// fetchArtwork fetches every picture a library's titles name and do not
// hold yet, into PocketBase's own storage — local, or S3 as its settings
// say. A picture that will not come is asked for again next scan.
func (e *Engine) fetchArtwork(ctx context.Context, lib *core.Record, run *run) {
	client, _ := e.tmdb()
	if client == nil {
		return
	}
	name := lib.GetString("name")
	jobs := []struct {
		collection string
		filter     string
		fields     []string
	}{{name, "(poster_path != '' && poster = '') || (backdrop_path != '' && backdrop = '') || (logo_path != '' && logo = '')", []string{"poster", "backdrop", "logo"}}}
	if lib.GetString("kind") == Shows {
		jobs = append(jobs, struct {
			collection string
			filter     string
			fields     []string
		}{Episodes(name), "still_path != '' && still = ''", []string{"still"}})
	}
	lock := e.writes(lib.Id)
	for _, job := range jobs {
		records, err := e.app.FindRecordsByFilter(job.collection, job.filter, "", 0, 0)
		if err != nil {
			run.note("Artwork could not be looked for: " + err.Error())
			continue
		}
		gate := make(chan struct{}, fetchAtOnce)
		var wg sync.WaitGroup
		for _, record := range records {
			wg.Go(func() {
				select {
				case gate <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-gate }()
				fetched := map[string]*filesystem.File{}
				for _, field := range job.fields {
					p := record.GetString(field + "_path")
					if p == "" || record.GetString(field) != "" {
						continue
					}
					file, err := e.download(ctx, client.ImageURL(sizes[field], p))
					if err != nil {
						continue
					}
					fetched[field] = file
				}
				if len(fetched) == 0 {
					return
				}
				lock.Lock()
				defer lock.Unlock()
				// Read again: the scan may have changed it meanwhile.
				current, err := e.app.FindRecordById(job.collection, record.Id)
				if err != nil {
					return
				}
				for field, file := range fetched {
					if current.GetString(field+"_path") == record.GetString(field+"_path") {
						current.Set(field, file)
					}
				}
				if err := e.app.Save(current); err == nil {
					run.update(func(p *Progress) { p.Artwork++ })
				}
			})
		}
		wg.Wait()
	}
}

// download fetches a picture whole, and no larger than maxImage.
func (e *Engine) download(ctx context.Context, address string) (*filesystem.File, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	response, err := e.images.Do(request)
	if err != nil {
		return nil, errors.New("the picture could not be fetched")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the picture's server answered %d", response.StatusCode)
	}
	if t := response.Header.Get("Content-Type"); t != "" && !strings.HasPrefix(t, "image/") {
		return nil, errors.New("not a picture")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxImage+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxImage {
		return nil, errors.New("the picture is too large")
	}
	return filesystem.NewFileFromBytes(data, path.Base(address))
}
