package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"
)

// InnerTube is the site's own API, as its web pages call it, signed out:
// searches, channels, playlists and a video's page. Always in English —
// what a video's "3 days ago" says is read as English — for the household's
// region.
type InnerTube struct {
	// Site is the site's address; a test points it elsewhere.
	Site   string
	client *http.Client
	region func() string
	now    func() time.Time

	mu      sync.Mutex
	version string
	visitor string
	until   time.Time
}

// fallbackVersion is the web client's version when the site's page will not
// say it: the site still answers an older one.
const fallbackVersion = "2.20261009.01.00"

// ErrRefused is the site refusing Foyer's question.
var ErrRefused = errors.New("the site refused")

// NewInnerTube asks the site as its web pages do, for region's videos.
func NewInnerTube(client *http.Client, region func() string) *InnerTube {
	return &InnerTube{Site: "https://www.youtube.com", client: client, region: region, now: time.Now}
}

var (
	clientVersion = regexp.MustCompile(`"INNERTUBE_CLIENT_VERSION":"([^"]+)"`)
	visitorData   = regexp.MustCompile(`"VISITOR_DATA":"([^"]+)"`)
)

// ident is the web client's version and this visitor's data, read from the
// site's own page once a day.
func (it *InnerTube) ident(ctx context.Context) (string, string) {
	it.mu.Lock()
	if it.now().Before(it.until) {
		defer it.mu.Unlock()
		return it.version, it.visitor
	}
	it.mu.Unlock()
	version, visitor := fallbackVersion, ""
	if page, err := it.fetch(ctx, it.Site+"/?hl=en"); err == nil {
		if m := clientVersion.FindSubmatch(page); m != nil {
			version = string(m[1])
		}
		if m := visitorData.FindSubmatch(page); m != nil {
			visitor = string(m[1])
		}
	}
	it.mu.Lock()
	defer it.mu.Unlock()
	// A page that would not say is asked again within the hour, not the day.
	it.version, it.visitor = version, visitor
	if visitor != "" {
		it.until = it.now().Add(24 * time.Hour)
	} else {
		it.until = it.now().Add(time.Hour)
	}
	return version, visitor
}

func (it *InnerTube) fetch(ctx context.Context, link string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", agent)
	req.Header.Set("Accept-Language", "en")
	resp, err := it.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w (%d)", ErrRefused, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// call asks one of the API's endpoints — search, browse, next — and answers
// what it said, as JSON.
func (it *InnerTube) call(ctx context.Context, endpoint string, body map[string]any) (any, error) {
	version, visitor := it.ident(ctx)
	client := map[string]any{"clientName": "WEB", "clientVersion": version, "hl": "en", "gl": it.region()}
	if visitor != "" {
		client["visitorData"] = visitor
	}
	body["context"] = map[string]any{"client": client}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, it.Site+"/youtubei/v1/"+endpoint+"?prettyPrint=false", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", agent)
	req.Header.Set("Origin", "https://www.youtube.com")
	req.Header.Set("X-Youtube-Client-Name", "1")
	req.Header.Set("X-Youtube-Client-Version", version)
	resp, err := it.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w (%d)", ErrRefused, resp.StatusCode)
	}
	var answer any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&answer); err != nil {
		return nil, fmt.Errorf("the site's answer could not be read: %w", err)
	}
	return answer, nil
}
