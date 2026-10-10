package stream

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// twin serves files to the tools Foyer runs itself — ffprobe now, ffmpeg
// later — on 127.0.0.1 alone, by addresses that open one file for minutes.
// The tools never see the share's address or its password, in their
// arguments or in what they print; and nothing they read passes PocketBase's
// limiter or its request log.
type twin struct {
	listener net.Listener
	server   *http.Server
	mu       sync.Mutex
	grants   map[string]grant
}

type grant struct {
	file    string
	expires time.Time
}

// StartTwin opens the loopback listener. Started beside `serve` only.
func (s *Streamer) StartTwin() error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	t := &twin{listener: listener, grants: map[string]grant{}}
	t.server = &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "", http.StatusMethodNotAllowed)
			return
		}
		token := strings.TrimPrefix(r.URL.Path, "/g/")
		t.mu.Lock()
		g, ok := t.grants[token]
		t.mu.Unlock()
		if !ok || time.Now().After(g.expires) {
			http.Error(w, "", http.StatusForbidden)
			return
		}
		record, err := s.app.FindRecordById(s.collection, g.file)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		_ = Proxy(w, r, record.GetString("name"), func(ctx context.Context, header http.Header, head bool) (*http.Response, error) {
			return s.open(ctx, record, header, head)
		})
	})}
	s.twin = t
	go func() { _ = t.server.Serve(listener) }()
	return nil
}

// StopTwin closes the loopback listener.
func (s *Streamer) StopTwin(ctx context.Context) error {
	if s.twin == nil {
		return nil
	}
	return s.twin.server.Shutdown(ctx)
}

// Grant is a loopback address that opens one file for ttl, and the way to
// close it sooner.
func (s *Streamer) Grant(file string, ttl time.Duration) (string, func(), error) {
	t := s.twin
	if t == nil {
		return "", nil, errors.New("the loopback is not open")
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token := hex.EncodeToString(raw)
	t.mu.Lock()
	now := time.Now()
	for k, g := range t.grants {
		if now.After(g.expires) {
			delete(t.grants, k)
		}
	}
	t.grants[token] = grant{file: file, expires: now.Add(ttl)}
	t.mu.Unlock()
	revoke := func() {
		t.mu.Lock()
		delete(t.grants, token)
		t.mu.Unlock()
	}
	return "http://" + t.listener.Addr().String() + "/g/" + token, revoke, nil
}

// Loopback is the loopback's host and port, so what a tool prints can be
// cleared of it.
func (s *Streamer) Loopback() string {
	if s.twin == nil {
		return ""
	}
	return s.twin.listener.Addr().String()
}
