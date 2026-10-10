// Package zurgtest is a pretend Zurg: a WebDAV tree under /dav/, as Zurg
// serves its library, for tests and for `dev/fakezurg`. It answers PROPFIND
// at depths 0 and 1, and GET and HEAD with ranges.
package zurgtest

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

type node struct {
	dir      bool
	modified time.Time
	content  []byte
	children map[string]*node
}

// Fake is the tree and how it answers.
type Fake struct {
	// User and Password, when set, are the Basic sign-in it asks for.
	User, Password string
	// Quirk answers a part with 200 and a Content-Range, as Zurg does at
	// times.
	Quirk bool
	// IgnoreRange answers every GET whole.
	IgnoreRange bool

	mu        sync.Mutex
	root      *node
	propfinds map[string]int
}

func New() *Fake {
	return &Fake{root: &node{dir: true, children: map[string]*node{}}, propfinds: map[string]int{}}
}

func segments(path string) []string {
	var parts []string
	for _, s := range strings.Split(path, "/") {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return parts
}

// at finds a node by its path, making its folders on the way when make is
// set.
func (f *Fake) at(path string, make bool) *node {
	n := f.root
	for _, s := range segments(path) {
		child, ok := n.children[s]
		if !ok {
			if !make {
				return nil
			}
			child = &node{dir: true, children: map[string]*node{}}
			n.children[s] = child
		}
		n = child
	}
	return n
}

// Dir makes a folder, and its parents.
func (f *Fake) Dir(path string, modified time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.at(path, true).modified = modified
}

// File puts a file at path, making its folders. Its size is its content's.
func (f *Fake) File(path string, content []byte, modified time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := segments(path)
	parent := f.at(strings.Join(parts[:len(parts)-1], "/"), true)
	parent.children[parts[len(parts)-1]] = &node{content: content, modified: modified}
}

// Remove takes a file or a folder away.
func (f *Fake) Remove(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := segments(path)
	if parent := f.at(strings.Join(parts[:len(parts)-1], "/"), false); parent != nil {
		delete(parent.children, parts[len(parts)-1])
	}
}

// Propfinds is how often a folder was listed.
func (f *Fake) Propfinds(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.propfinds[strings.Join(segments(path), "/")]
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if f.User != "" || f.Password != "" {
		user, password, ok := r.BasicAuth()
		if !ok || user != f.User || password != f.Password {
			w.Header().Set("WWW-Authenticate", `Basic realm="zurg"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/dav")
	if !ok {
		http.NotFound(w, r)
		return
	}
	path := strings.Join(segments(rest), "/")
	f.mu.Lock()
	n := f.at(path, false)
	f.mu.Unlock()
	if n == nil {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case "PROPFIND":
		f.list(w, r, path, n)
	case http.MethodGet, http.MethodHead:
		if n.dir {
			http.Error(w, "a folder", http.StatusMethodNotAllowed)
			return
		}
		if f.IgnoreRange {
			r.Header.Del("Range")
		}
		var writer http.ResponseWriter = w
		if f.Quirk {
			writer = quirk{w}
		}
		http.ServeContent(writer, r, path, n.modified, bytes.NewReader(n.content))
	default:
		http.Error(w, "not here", http.StatusMethodNotAllowed)
	}
}

// quirk answers a part as 200, keeping its Content-Range.
type quirk struct{ http.ResponseWriter }

func (q quirk) WriteHeader(status int) {
	if status == http.StatusPartialContent {
		status = http.StatusOK
	}
	q.ResponseWriter.WriteHeader(status)
}

func href(path string, dir bool) string {
	parts := segments(path)
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	h := "/dav/" + strings.Join(parts, "/")
	if dir && len(parts) > 0 {
		h += "/"
	}
	return h
}

func (f *Fake) list(w http.ResponseWriter, r *http.Request, path string, n *node) {
	f.mu.Lock()
	f.propfinds[path]++
	f.mu.Unlock()

	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><d:multistatus xmlns:d="DAV:">`)
	entry := func(path string, n *node) {
		b.WriteString("<d:response><d:href>")
		_ = xml.EscapeText(&b, []byte(href(path, n.dir)))
		b.WriteString("</d:href><d:propstat><d:prop>")
		if n.dir {
			b.WriteString("<d:resourcetype><d:collection/></d:resourcetype>")
		} else {
			fmt.Fprintf(&b, "<d:resourcetype/><d:getcontentlength>%d</d:getcontentlength>", len(n.content))
		}
		if !n.modified.IsZero() {
			fmt.Fprintf(&b, "<d:getlastmodified>%s</d:getlastmodified>", n.modified.UTC().Format(http.TimeFormat))
		}
		b.WriteString("</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>")
	}
	f.mu.Lock()
	entry(path, n)
	if r.Header.Get("Depth") != "0" && n.dir {
		names := make([]string, 0, len(n.children))
		for name := range n.children {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			child := path + "/" + name
			if path == "" {
				child = name
			}
			entry(child, n.children[name])
		}
	}
	f.mu.Unlock()
	b.WriteString("</d:multistatus>")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = w.Write(b.Bytes())
}
