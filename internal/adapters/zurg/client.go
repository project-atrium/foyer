// Package zurg reads Zurg — a Real-Debrid library served over WebDAV — the
// way a media server's scanner would: its folders, the torrents in each, the
// files in a torrent, and a file's bytes. Zurg's folders, the ones its config
// sorts torrents into, exist only in this view, so WebDAV is the way in.
package zurg

import (
	"bytes"
	"context"
	"encoding/xml"
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

var (
	// ErrRefused is Zurg turning down the username or password. It stays
	// refused until they change.
	ErrRefused = errors.New("Zurg did not accept the username or password")
	// ErrNotFound is a folder or file Zurg does not have, or no longer has.
	ErrNotFound = errors.New("Zurg has no such folder or file")
	// ErrUnavailable is Zurg failing, or not answering.
	ErrUnavailable = errors.New("Zurg cannot be reached")
	// ErrRangeIgnored is a file answered whole when only a part was asked
	// for: passed on, a player would start again from the beginning.
	ErrRangeIgnored = errors.New("Zurg answered with the whole file where a part was asked for")
)

const (
	// atOnce bounds Zurg's listings: it answers from memory, but a scan walks
	// thousands of folders.
	atOnce      = 4
	listTimeout = 60 * time.Second
	// maxListing is what one listing may weigh.
	maxListing = 64 << 20
)

// Entry is one thing in a folder.
type Entry struct {
	Name     string
	Dir      bool
	Size     int64
	Modified time.Time
}

// File is a file under a library folder: its path from the folder, every
// segment as Zurg names it.
type File struct {
	Path     string
	Size     int64
	Modified time.Time
}

// Client is one Zurg, reached with one username and password.
type Client struct {
	base     *url.URL
	user     string
	password string
	lists    *http.Client
	streams  *http.Client
	gate     chan struct{}

	refused sync.Mutex
	latched bool
}

// New takes Zurg's address as the household knows it — `http://zurg:9999`,
// or its WebDAV address itself — and reaches its WebDAV under `/dav/`.
func New(address, user, password string) (*Client, error) {
	base, err := url.Parse(strings.TrimSpace(address))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, errors.New("Zurg's address must be an http or https address")
	}
	base.Path = strings.TrimRight(base.Path, "/")
	if !strings.HasSuffix(base.Path, "/dav") {
		base.Path += "/dav"
	}
	base.Path += "/"
	base.RawQuery, base.Fragment, base.User = "", "", nil
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConnsPerHost: 16,
		// A file's bytes are passed on as they are: Go would otherwise ask for
		// gzip and undo it, and a Range would no longer be one.
		DisableCompression:    true,
		ResponseHeaderTimeout: 60 * time.Second,
	}
	return &Client{
		base:     base,
		user:     user,
		password: password,
		lists:    &http.Client{Transport: transport, Timeout: listTimeout},
		streams:  &http.Client{Transport: transport},
		gate:     make(chan struct{}, atOnce),
	}, nil
}

// address is a path under the WebDAV root, every segment escaped. A trailing
// slash marks a folder.
func (c *Client) address(path string, dir bool) string {
	segments := split(path)
	escaped := make([]string, len(segments))
	for i, s := range segments {
		escaped[i] = url.PathEscape(s)
	}
	at := c.base.EscapedPath() + strings.Join(escaped, "/")
	if dir && len(segments) > 0 {
		at += "/"
	}
	return c.base.Scheme + "://" + c.base.Host + at
}

// split is a path's segments, as named.
func split(path string) []string {
	var segments []string
	for _, s := range strings.Split(path, "/") {
		if s != "" {
			segments = append(segments, s)
		}
	}
	return segments
}

// decoded is an escaped path's segments, each unescaped on its own, so a
// name holding an escaped slash stays one segment.
func decoded(escaped string) ([]string, bool) {
	var segments []string
	for _, s := range strings.Split(escaped, "/") {
		if s == "" {
			continue
		}
		name, err := url.PathUnescape(s)
		if err != nil {
			return nil, false
		}
		segments = append(segments, name)
	}
	return segments, true
}

const propfind = `<?xml version="1.0" encoding="utf-8"?>` +
	`<d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/><d:getcontentlength/><d:getlastmodified/></d:prop></d:propfind>`

type multistatus struct {
	Responses []struct {
		Href     string `xml:"href"`
		Propstat []struct {
			Status string `xml:"status"`
			Prop   struct {
				ResourceType struct {
					Collection *struct{} `xml:"collection"`
				} `xml:"resourcetype"`
				Length   string `xml:"getcontentlength"`
				Modified string `xml:"getlastmodified"`
			} `xml:"prop"`
		} `xml:"propstat"`
	} `xml:"response"`
}

// List is what a folder holds: "" is Zurg's root, with its folders. The
// folder's own entry is left out, and so is a name that cannot be a path's
// segment.
func (c *Client) List(ctx context.Context, dir string) ([]Entry, error) {
	if c.isRefused() {
		return nil, ErrRefused
	}
	select {
	case c.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.gate }()

	address := c.address(dir, true)
	request, err := http.NewRequestWithContext(ctx, "PROPFIND", address, strings.NewReader(propfind))
	if err != nil {
		return nil, ErrUnavailable
	}
	request.Header.Set("Depth", "1")
	request.Header.Set("Content-Type", "application/xml; charset=utf-8")
	c.authorize(request)
	response, err := c.lists.Do(request)
	if err != nil {
		return nil, unwrap(err)
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		c.refuse()
		return nil, ErrRefused
	case response.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case response.StatusCode != http.StatusMultiStatus:
		return nil, fmt.Errorf("%w (%d)", ErrUnavailable, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxListing))
	if err != nil {
		return nil, ErrUnavailable
	}
	var status multistatus
	if err := xml.NewDecoder(bytes.NewReader(body)).Decode(&status); err != nil {
		return nil, fmt.Errorf("%w: its listing could not be read", ErrUnavailable)
	}
	// Zurg names each entry by its whole path. The folder's own entry ends in
	// the folder's segments; every other ends in one more, its name.
	self := split(dir)
	var entries []Entry
	for _, r := range status.Responses {
		href, err := url.Parse(strings.TrimSpace(r.Href))
		if err != nil {
			continue
		}
		segments, ok := decoded(href.EscapedPath())
		if !ok || len(segments) == 0 || endsWith(segments, self) && len(segments) <= len(self)+len(split(c.base.Path)) {
			continue
		}
		name := segments[len(segments)-1]
		if !endsWith(segments[:len(segments)-1], self) || strings.Contains(name, "/") || name == "." || name == ".." {
			continue
		}
		entry := Entry{Name: name}
		for _, p := range r.Propstat {
			if p.Status != "" && !strings.Contains(p.Status, " 200") {
				continue
			}
			if p.Prop.ResourceType.Collection != nil {
				entry.Dir = true
			}
			if n, err := strconv.ParseInt(strings.TrimSpace(p.Prop.Length), 10, 64); err == nil {
				entry.Size = n
			}
			if t, err := http.ParseTime(strings.TrimSpace(p.Prop.Modified)); err == nil {
				entry.Modified = t.UTC()
			}
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// endsWith reports whether segments end with tail.
func endsWith(segments, tail []string) bool {
	if len(tail) > len(segments) {
		return false
	}
	for i := range tail {
		if segments[len(segments)-len(tail)+i] != tail[i] {
			return false
		}
	}
	return true
}

// Walk is every file under a folder's entry, depth folders deep at most:
// Zurg lists a torrent's files flat — a ` (id)` added where two share a
// name — or under their own folders, as configured.
func (c *Client) Walk(ctx context.Context, folder, entry string, depth int) ([]File, error) {
	var files []File
	var walk func(dir, prefix string, left int) error
	walk = func(dir, prefix string, left int) error {
		entries, err := c.List(ctx, dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			path := prefix + e.Name
			if e.Dir {
				if left > 0 {
					if err := walk(dir+"/"+e.Name, path+"/", left-1); err != nil {
						return err
					}
				}
				continue
			}
			files = append(files, File{Path: path, Size: e.Size, Modified: e.Modified})
		}
		return nil
	}
	if err := walk(folder+"/"+entry, entry+"/", depth); err != nil {
		return nil, err
	}
	return files, nil
}

// Open asks Zurg for a file's bytes — the part a Range names, or all of
// them — following Zurg wherever it sends the request. Zurg answers a part
// with 200 and a Content-Range at times; that is a 206 here. A part asked for
// and the whole file answered is ErrRangeIgnored.
func (c *Client) Open(ctx context.Context, folder, path string, header http.Header, head bool) (*http.Response, error) {
	if c.isRefused() {
		return nil, ErrRefused
	}
	method := http.MethodGet
	if head {
		method = http.MethodHead
	}
	request, err := http.NewRequestWithContext(ctx, method, c.address(folder+"/"+path, false), nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	for _, name := range []string{"Range", "If-Range", "If-None-Match", "If-Modified-Since"} {
		if v := header.Get(name); v != "" {
			request.Header.Set(name, v)
		}
	}
	request.Header.Set("Accept-Encoding", "identity")
	c.authorize(request)
	response, err := c.streams.Do(request)
	if err != nil {
		return nil, unwrap(err)
	}
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		response.Body.Close()
		c.refuse()
		return nil, ErrRefused
	case response.StatusCode == http.StatusNotFound:
		response.Body.Close()
		return nil, ErrNotFound
	case response.StatusCode == http.StatusOK && response.Header.Get("Content-Range") != "" && request.Header.Get("Range") != "":
		response.StatusCode, response.Status = http.StatusPartialContent, "206 Partial Content"
	case response.StatusCode == http.StatusOK && rangeStart(request.Header.Get("Range")) > 0:
		response.Body.Close()
		return nil, ErrRangeIgnored
	case response.StatusCode >= 500:
		response.Body.Close()
		return nil, fmt.Errorf("%w (%d)", ErrUnavailable, response.StatusCode)
	}
	return response, nil
}

// rangeStart is where a `bytes=N-` range begins; 0 for none, or one from the
// end.
func rangeStart(value string) int64 {
	spec, ok := strings.CutPrefix(strings.TrimSpace(value), "bytes=")
	if !ok {
		return 0
	}
	first, _, _ := strings.Cut(spec, ",")
	start, _, _ := strings.Cut(first, "-")
	n, err := strconv.ParseInt(strings.TrimSpace(start), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func (c *Client) authorize(r *http.Request) {
	if c.user != "" || c.password != "" {
		r.SetBasicAuth(c.user, c.password)
	}
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

// unwrap drops the address Go's url.Error would print: it may hold a path a
// log has no business with.
func unwrap(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if errors.Is(urlErr.Err, context.Canceled) || errors.Is(urlErr.Err, context.DeadlineExceeded) {
			return urlErr.Err
		}
		return fmt.Errorf("%w: %w", ErrUnavailable, urlErr.Err)
	}
	return ErrUnavailable
}
