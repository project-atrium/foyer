// Package tmdb asks The Movie Database what a film or a series is, with the
// household's own key: Loge's matcher, ported, and the details a library
// keeps — names, dates, ids, seasons and artwork.
package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// BaseURL and ImageURL are TMDB's; tests point a client elsewhere.
	BaseURL  = "https://api.themoviedb.org/3"
	ImageURL = "https://image.tmdb.org/t/p"

	timeout = 15 * time.Second
	// TMDB counts requests per address, around fifty a second; Foyer stays
	// well under it, a few at a time.
	atOnce    = 4
	perSecond = 20
	// What one answer may weigh: a series' twenty seasons in one call are
	// a few megabytes.
	maxBody = 32 << 20
)

var (
	// ErrRefused is a key TMDB did not accept. It stays refused until the key
	// changes: asking again with it only adds to the count.
	ErrRefused = errors.New("TMDB did not accept the key")
	// ErrNoKey is no key at all.
	ErrNoKey = errors.New("TMDB needs a key")
	// ErrNotFound is a title TMDB does not have.
	ErrNotFound = errors.New("TMDB has no such title")
	// ErrBusy is TMDB asking for fewer requests, still, after waiting.
	ErrBusy = errors.New("TMDB is being asked for too much at once")
	// ErrUnavailable is TMDB failing, or not answering.
	ErrUnavailable = errors.New("TMDB cannot be reached")
)

// Client is one key's way to TMDB. Make a new one when the key changes.
type Client struct {
	key    string
	bearer bool
	base   string
	images string
	http   *http.Client

	gate chan struct{}
	pace sync.Mutex
	next time.Time
	// backoff is the first wait before asking again after a failure.
	backoff time.Duration

	refused sync.Mutex
	latched bool
}

type Option func(*Client)

// WithBase points the client at another address, for tests.
func WithBase(api, images string) Option {
	return func(c *Client) { c.base, c.images = strings.TrimRight(api, "/"), strings.TrimRight(images, "/") }
}

// WithBackoff sets the first wait after a failure, for tests.
func WithBackoff(d time.Duration) Option { return func(c *Client) { c.backoff = d } }

// New makes a client for the key: a Read Access Token — a JWT, three parts
// with dots between — goes as a bearer header; an API key, 32 hex digits, in
// the query, which never reaches a log.
func New(key string, options ...Option) *Client {
	key = strings.TrimSpace(key)
	c := &Client{
		key:     key,
		bearer:  strings.Contains(key, "."),
		base:    BaseURL,
		images:  ImageURL,
		http:    &http.Client{Timeout: timeout},
		gate:    make(chan struct{}, atOnce),
		backoff: time.Second,
	}
	for _, option := range options {
		option(c)
	}
	return c
}

// ImageURL is where an image TMDB names by its path is, in the size asked
// for: `w780`, `w1280`, `original`.
func (c *Client) ImageURL(size, path string) string {
	return c.images + "/" + size + path
}

// get asks TMDB for a path and reads its answer into out. A 429 is waited
// out — as long as TMDB says, else a growing pause — and a failure on TMDB's
// side asked again twice. A refused key is never asked with again.
func (c *Client) get(ctx context.Context, path string, params url.Values, out any) error {
	if c.key == "" {
		return ErrNoKey
	}
	if c.isRefused() {
		return ErrRefused
	}
	query := url.Values{}
	for k, v := range params {
		query[k] = v
	}
	if !c.bearer {
		query.Set("api_key", c.key)
	}
	address := c.base + path
	if len(query) > 0 {
		address += "?" + query.Encode()
	}

	wait := c.backoff
	busy, failed := 0, 0
	for {
		status, body, header, err := c.send(ctx, address)
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failed++
			if failed > 2 {
				return fmt.Errorf("%w: %w", ErrUnavailable, err)
			}
		case status == http.StatusUnauthorized:
			c.refuse()
			return ErrRefused
		case status == http.StatusNotFound:
			return ErrNotFound
		case status == http.StatusTooManyRequests:
			busy++
			if busy > 3 {
				return ErrBusy
			}
			if after := retryAfter(header.Get("Retry-After")); after >= 0 {
				wait = after
			}
		case status >= 500:
			failed++
			if failed > 2 {
				return fmt.Errorf("%w (%d)", ErrUnavailable, status)
			}
		case status >= 400:
			return fmt.Errorf("TMDB refused the request (%d)", status)
		default:
			if err := json.Unmarshal(body, out); err != nil {
				return fmt.Errorf("%w: its answer could not be read", ErrUnavailable)
			}
			return nil
		}
		if err := sleep(ctx, wait); err != nil {
			return err
		}
		wait = min(wait*2, 30*time.Second)
	}
}

// send makes one request, a few at a time and no faster than TMDB likes. Its
// error never holds the address, which may hold the key.
func (c *Client) send(ctx context.Context, address string) (int, []byte, http.Header, error) {
	if err := c.paced(ctx); err != nil {
		return 0, nil, nil, err
	}
	select {
	case c.gate <- struct{}{}:
	case <-ctx.Done():
		return 0, nil, nil, ctx.Err()
	}
	defer func() { <-c.gate }()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return 0, nil, nil, errors.New("the request could not be made")
	}
	request.Header.Set("Accept", "application/json")
	if c.bearer {
		request.Header.Set("Authorization", "Bearer "+c.key)
	}
	response, err := c.http.Do(request)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return 0, nil, nil, urlErr.Err
		}
		return 0, nil, nil, errors.New("no answer")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody))
	if err != nil {
		return 0, nil, nil, errors.New("the answer was cut short")
	}
	return response.StatusCode, body, response.Header, nil
}

// paced waits for this request's turn: perSecond a second at most.
func (c *Client) paced(ctx context.Context) error {
	c.pace.Lock()
	now := time.Now()
	at := c.next
	if at.Before(now) {
		at = now
	}
	c.next = at.Add(time.Second / perSecond)
	c.pace.Unlock()
	return sleep(ctx, time.Until(at))
}

func (c *Client) isRefused() bool {
	c.refused.Lock()
	defer c.refused.Unlock()
	return c.latched
}

func (c *Client) refuse() {
	c.refused.Lock()
	c.latched = true
	c.refused.Unlock()
}

// retryAfter reads a Retry-After of seconds; -1 when there is none.
func retryAfter(value string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds < 0 {
		return -1
	}
	return min(time.Duration(seconds)*time.Second, 30*time.Second)
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Check asks TMDB whether it takes the key, once.
func (c *Client) Check(ctx context.Context) error {
	var answer struct {
		Success bool `json:"success"`
	}
	return c.get(ctx, "/authentication", nil, &answer)
}
