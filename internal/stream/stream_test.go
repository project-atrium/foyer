package stream

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// serving is what an opener answers: the file, served with ranges as a
// source serves it.
func serving(content []byte) func(context.Context, http.Header, bool) (*http.Response, error) {
	return func(ctx context.Context, header http.Header, head bool) (*http.Response, error) {
		method := http.MethodGet
		if head {
			method = http.MethodHead
		}
		r := httptest.NewRequest(method, "/x.mkv", nil).WithContext(ctx)
		r.Header = header.Clone()
		w := httptest.NewRecorder()
		http.ServeContent(w, r, "x.mkv", time.Time{}, bytes.NewReader(content))
		return w.Result(), nil
	}
}

func proxied(open func(context.Context, http.Header, bool) (*http.Response, error)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = Proxy(w, r, "film.mkv", open)
	}))
}

func TestPassesAPartOn(t *testing.T) {
	server := proxied(serving([]byte("0123456789")))
	defer server.Close()
	request, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	request.Header.Set("Range", "bytes=2-5")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusPartialContent || string(body) != "2345" ||
		response.Header.Get("Content-Range") != "bytes 2-5/10" || response.Header.Get("Accept-Ranges") != "bytes" ||
		response.Header.Get("Content-Type") != "video/x-matroska" {
		t.Fatalf("%d %q %v", response.StatusCode, body, response.Header)
	}
}

func TestAnswersHeadWithItsHeadersAlone(t *testing.T) {
	server := proxied(serving([]byte("0123456789")))
	defer server.Close()
	response, err := http.Head(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.ContentLength != 10 {
		t.Fatalf("%d %d", response.StatusCode, response.ContentLength)
	}
}

func TestSaysWhatTheSourceCouldNot(t *testing.T) {
	for want, err := range map[int]error{http.StatusNotFound: ErrGone, http.StatusBadGateway: ErrSource} {
		server := proxied(func(context.Context, http.Header, bool) (*http.Response, error) { return nil, err })
		response, getErr := http.Get(server.URL)
		server.Close()
		if getErr != nil || response.StatusCode != want {
			t.Errorf("%v: %v %v", err, response, getErr)
		}
	}
}

// PocketBase's server reads and writes with five-minute deadlines. A film
// streamed past them must go on: here the deadlines are 300 milliseconds,
// and a slow source takes a second and a half.
func TestAStreamOutlastsTheServersDeadlines(t *testing.T) {
	chunk := bytes.Repeat([]byte("x"), 64<<10)
	slow := func(ctx context.Context, header http.Header, head bool) (*http.Response, error) {
		reader, writer := io.Pipe()
		go func() {
			defer writer.Close()
			for range 16 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(100 * time.Millisecond):
				}
				if _, err := writer.Write(chunk); err != nil {
					return
				}
			}
		}()
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: reader}, nil
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = Proxy(w, r, "film.mkv", slow)
	}))
	server.Config.ReadTimeout, server.Config.WriteTimeout = 300*time.Millisecond, 300*time.Millisecond
	server.Start()
	defer server.Close()
	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || len(body) != 16*len(chunk) {
		t.Fatalf("%d bytes, %v", len(body), err)
	}
}

func TestAPlayerThatLeavesStopsTheSource(t *testing.T) {
	stopped := make(chan struct{})
	endless := func(ctx context.Context, header http.Header, head bool) (*http.Response, error) {
		reader, writer := io.Pipe()
		go func() {
			defer close(stopped)
			for {
				select {
				case <-ctx.Done():
					writer.CloseWithError(ctx.Err())
					return
				default:
				}
				if _, err := writer.Write([]byte(strings.Repeat("x", 1024))); err != nil {
					return
				}
			}
		}()
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: reader}, nil
	}
	server := proxied(endless)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadFull(response.Body, make([]byte, 4096))
	cancel()
	response.Body.Close()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the source went on after the player left")
	}
}
