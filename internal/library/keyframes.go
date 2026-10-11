package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/pocketbase/pocketbase/core"

	"foyer/internal/hls"
)

// Keyframes is where a file's keyframes lie, for a remux: kept on the file
// once read, and read from the share the first time one needs it — its
// header and its index, a few ranged reads, never the file. A file that keeps
// no index is kept as having none, until it changes; a share that fails is
// no answer, and nothing is kept.
func (e *Engine) Keyframes(ctx context.Context, file *core.Record) (hls.Index, error) {
	switch file.GetString("keyframes_state") {
	case "done":
		var idx hls.Index
		if err := file.UnmarshalJSONField("keyframes", &idx); err == nil && idx.Version == hls.IndexVersion {
			return idx, nil
		}
	case "none":
		return hls.Index{}, hls.ErrNoIndex
	}
	v, err, _ := e.indexes.Do(file.Id, func() (any, error) {
		idx, err := hls.ReadIndex(ctx, e.ranges(file), file.GetString("name"), file.GetInt64("size"))
		switch {
		case errors.Is(err, hls.ErrNoIndex):
			return hls.Index{}, e.keepFile(file, func(r *core.Record) {
				r.Set("keyframes", nil)
				r.Set("keyframes_state", "none")
			})
		case err != nil:
			return hls.Index{}, err
		}
		return idx, e.keepFile(file, func(r *core.Record) {
			r.Set("keyframes", idx)
			r.Set("keyframes_state", "done")
		})
	})
	if err != nil {
		return hls.Index{}, err
	}
	idx := v.(hls.Index)
	if len(idx.Keyframes) == 0 {
		return hls.Index{}, hls.ErrNoIndex
	}
	return idx, nil
}

// ranges reads a file's bytes by ranges, from the share.
func (e *Engine) ranges(file *core.Record) hls.RangeReader {
	return func(ctx context.Context, offset, length int64) ([]byte, error) {
		if length <= 0 {
			return nil, nil
		}
		header := http.Header{"Range": {fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)}}
		response, err := e.OpenFile(ctx, file, header, false)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		switch response.StatusCode {
		case http.StatusRequestedRangeNotSatisfiable:
			return nil, nil
		case http.StatusOK, http.StatusPartialContent:
		default:
			return nil, fmt.Errorf("the share answered %d", response.StatusCode)
		}
		// A share that ignores a range from the start answers the whole file:
		// only what was asked for is read of it.
		b, err := io.ReadAll(io.LimitReader(response.Body, length))
		if err != nil && len(b) < int(length) {
			return nil, err
		}
		return b, nil
	}
}

// keepFile writes a file's record under its library's lock, from the record
// as it is now.
func (e *Engine) keepFile(file *core.Record, apply func(*core.Record)) error {
	lock := e.writes(file.GetString("library"))
	lock.Lock()
	defer lock.Unlock()
	fresh, err := e.app.FindRecordById(FilesCollection, file.Id)
	if err != nil {
		return err
	}
	apply(fresh)
	return e.app.Save(fresh)
}
