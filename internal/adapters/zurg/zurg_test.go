package zurg_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"foyer/internal/adapters"
	"foyer/internal/adapters/zurg"
	"foyer/internal/adapters/zurg/zurgtest"
)

var added = time.Date(2026, 1, 25, 20, 43, 7, 0, time.UTC)

// library is a Zurg with a film and a season pack — names invented, shaped
// like the household's — the pack's files under folders of their own.
func library(t *testing.T) (*zurgtest.Fake, *zurg.Client) {
	t.Helper()
	fake := zurgtest.New()
	fake.Dir("__all__", added)
	fake.File("movies/Some.Film.2022.1080p.BluRay.x264-GRP/Some.Film.2022.1080p.BluRay.x264-GRP.mkv", []byte("film"), added)
	fake.File("shows/[Group] Some Show (01-26) [1080p]/Season 01/[Group] Some Show - 01.mkv", []byte("one"), added.Add(time.Hour))
	fake.File("shows/[Group] Some Show (01-26) [1080p]/Season 01/[Group] Some Show - 02.mkv", []byte("two"), added.Add(time.Hour))
	fake.File("shows/[Group] Some Show (01-26) [1080p]/Extras #1/Making of 100% & more.mkv", []byte("extra"), added)
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	client, err := zurg.New(server.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return fake, client
}

func TestListsTheFoldersAtZurgsRoot(t *testing.T) {
	_, client := library(t)
	entries, err := client.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.Dir {
			names = append(names, e.Name)
		}
	}
	if !slices.Equal(names, []string{"__all__", "movies", "shows"}) {
		t.Fatalf("folders %v", names)
	}
}

func TestListsAFolderWithItsDates(t *testing.T) {
	_, client := library(t)
	entries, err := client.List(context.Background(), "movies")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].Dir || entries[0].Name != "Some.Film.2022.1080p.BluRay.x264-GRP" {
		t.Fatalf("entries %+v", entries)
	}
}

func TestWalksATorrentsFoldersWhateverTheirNames(t *testing.T) {
	_, client := library(t)
	files, err := client.Walk(context.Background(), "shows", "[Group] Some Show (01-26) [1080p]", 3)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	slices.Sort(paths)
	want := []string{
		"[Group] Some Show (01-26) [1080p]/Extras #1/Making of 100% & more.mkv",
		"[Group] Some Show (01-26) [1080p]/Season 01/[Group] Some Show - 01.mkv",
		"[Group] Some Show (01-26) [1080p]/Season 01/[Group] Some Show - 02.mkv",
	}
	if !slices.Equal(paths, want) {
		t.Fatalf("paths %q", paths)
	}
	if files[0].Size == 0 || files[0].Modified.IsZero() {
		t.Fatalf("file %+v", files[0])
	}
}

// Zurg itself writes a folder's children as their bare escaped names — a
// colon left as it is — its own entry as a path without /dav/, and its dates
// in UTC where HTTP says GMT.
func TestReadsZurgsOwnListing(t *testing.T) {
	const listing = `<?xml version="1.0" encoding="utf-8"?><d:multistatus xmlns:d="DAV:"><d:response>
	<d:href>/shows/</d:href>
	<d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype><d:getlastmodified>Sat, 10 Oct 2026 21:42:26 UTC</d:getlastmodified></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>
</d:response><d:response>
	<d:href>Some%20Show%20%28Season%203%29%20%5B1080p%5D</d:href>
	<d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype><d:getlastmodified>Fri, 31 Jul 2026 19:22:23 UTC</d:getlastmodified></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>
</d:response><d:response>
	<d:href>Re:Start%20-%20S01E01.mkv</d:href>
	<d:propstat><d:prop><d:getcontentlength>351878464</d:getcontentlength><d:getlastmodified>Fri, 31 Jul 2026 00:44:00 UTC</d:getlastmodified><d:resourcetype></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>
</d:response></d:multistatus>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/dav/shows/" {
			t.Errorf("asked for %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, listing)
	}))
	defer server.Close()
	client, err := zurg.New(server.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := client.List(context.Background(), "shows")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]zurg.Entry{}
	for _, e := range entries {
		got[e.Name] = e
	}
	if len(got) != 2 {
		t.Fatalf("entries %+v", entries)
	}
	if e := got["Some Show (Season 3) [1080p]"]; !e.Dir || !e.Modified.Equal(time.Date(2026, 7, 31, 19, 22, 23, 0, time.UTC)) {
		t.Errorf("the pack %+v", e)
	}
	if e := got["Re:Start - S01E01.mkv"]; e.Dir || e.Size != 351878464 || !e.Modified.Equal(time.Date(2026, 7, 31, 0, 44, 0, 0, time.UTC)) {
		t.Errorf("the file %+v", e)
	}
}

// A pack holding an episode in two of its folders is listed with the name
// twice, and Zurg serves one of them by it: one name is one file, its size
// unknown where the copies differ.
func TestANameListedTwiceIsOneFile(t *testing.T) {
	const listing = `<?xml version="1.0" encoding="utf-8"?><d:multistatus xmlns:d="DAV:">
<d:response><d:href>/shows/Pack/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
<d:response><d:href>Pack%20-%2001.mkv</d:href><d:propstat><d:prop><d:getcontentlength>100</d:getcontentlength><d:resourcetype></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
<d:response><d:href>Pack%20-%2001.mkv</d:href><d:propstat><d:prop><d:getcontentlength>300</d:getcontentlength><d:resourcetype></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
<d:response><d:href>Pack%20-%2002.mkv</d:href><d:propstat><d:prop><d:getcontentlength>200</d:getcontentlength><d:resourcetype></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
<d:response><d:href>Pack%20-%2002.mkv</d:href><d:propstat><d:prop><d:getcontentlength>200</d:getcontentlength><d:resourcetype></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, listing)
	}))
	defer server.Close()
	client, err := zurg.New(server.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	files, err := client.Walk(context.Background(), "shows", "Pack", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Path != "Pack/Pack - 01.mkv" || files[0].Size != 0 || files[1].Size != 200 {
		t.Fatalf("files %+v", files)
	}
}

// The pretend Zurg answering as Zurg does is read as well: its folders, a
// torrent walked, a part opened.
func TestReadsAZurgThatAnswersAsZurgDoes(t *testing.T) {
	fake, client := library(t)
	fake.AsZurg = true
	folders, err := client.List(context.Background(), "")
	if err != nil || len(folders) != 3 {
		t.Fatalf("folders %+v %v", folders, err)
	}
	files, err := client.Walk(context.Background(), "shows", "[Group] Some Show (01-26) [1080p]", 3)
	if err != nil || len(files) != 3 || files[0].Modified.IsZero() {
		t.Fatalf("files %+v %v", files, err)
	}
	response, err := client.Open(context.Background(), "movies", "Some.Film.2022.1080p.BluRay.x264-GRP/Some.Film.2022.1080p.BluRay.x264-GRP.mkv", http.Header{"Range": {"bytes=1-2"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusPartialContent || string(body) != "il" || response.ContentLength != -1 {
		t.Fatalf("%d %q, length %d", response.StatusCode, body, response.ContentLength)
	}
}

// Zurg names its entries by absolute addresses or paths, escaped its own way:
// whatever the spelling, a name is its own segment.
func TestReadsAListingWhateverItsHrefsLookLike(t *testing.T) {
	const listing = `<?xml version="1.0"?>
<D:multistatus xmlns:D="DAV:">
 <D:response><D:href>/dav/shows/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
 <D:response><D:href>http://zurg:9999/dav/shows/Spaced%20Out%20%5B1080p%5D/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype><D:getlastmodified>Sun, 25 Jan 2026 20:43:07 GMT</D:getlastmodified></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
 <D:response><D:href>/dav/shows/Caf%C3%A9 &amp; Bar (2019)/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
 <D:response><D:href>/dav/shows/a%2Fb/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
 <D:response><D:href>/dav/shows/loose.mkv</D:href><D:propstat><D:prop><D:resourcetype/><D:getcontentlength>1234</D:getcontentlength></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat><D:propstat><D:prop><D:getetag/></D:prop><D:status>HTTP/1.1 404 Not Found</D:status></D:propstat></D:response>
</D:multistatus>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" || r.Header.Get("Depth") != "1" || r.URL.Path != "/dav/shows/" {
			t.Errorf("%s %s depth %s", r.Method, r.URL.Path, r.Header.Get("Depth"))
		}
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, listing)
	}))
	defer server.Close()
	client, err := zurg.New(server.URL+"/dav", "", "")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := client.List(context.Background(), "shows")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]zurg.Entry{}
	for _, e := range entries {
		got[e.Name] = e
	}
	if len(got) != 3 {
		t.Fatalf("entries %+v", entries)
	}
	if e := got["Spaced Out [1080p]"]; !e.Dir || !e.Modified.Equal(time.Date(2026, 1, 25, 20, 43, 7, 0, time.UTC)) {
		t.Fatalf("spaced %+v", e)
	}
	if e := got["Café & Bar (2019)"]; !e.Dir {
		t.Fatalf("café %+v", e)
	}
	if e := got["loose.mkv"]; e.Dir || e.Size != 1234 {
		t.Fatalf("loose %+v", e)
	}
}

func TestARefusedSignInIsKeptAndAsksNothingMore(t *testing.T) {
	fake := zurgtest.New()
	fake.User, fake.Password = "zurg", "right"
	fake.Dir("movies", added)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		fake.ServeHTTP(w, r)
	}))
	defer server.Close()
	wrong, _ := zurg.New(server.URL, "zurg", "wrong")
	if _, err := wrong.List(context.Background(), ""); !errors.Is(err, zurg.ErrRefused) {
		t.Fatalf("got %v", err)
	}
	if _, err := wrong.List(context.Background(), "movies"); !errors.Is(err, zurg.ErrRefused) || requests != 1 {
		t.Fatalf("again: %v after %d requests", err, requests)
	}
	right, _ := zurg.New(server.URL, "zurg", "right")
	if entries, err := right.List(context.Background(), ""); err != nil || len(entries) != 1 {
		t.Fatalf("right: %v %v", entries, err)
	}
}

func TestOpensAPartOfAFile(t *testing.T) {
	fake, client := library(t)
	_ = fake
	header := http.Header{"Range": {"bytes=1-2"}}
	response, err := client.Open(context.Background(), "movies", "Some.Film.2022.1080p.BluRay.x264-GRP/Some.Film.2022.1080p.BluRay.x264-GRP.mkv", header, false)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusPartialContent || string(body) != "il" || response.Header.Get("Content-Range") != "bytes 1-2/4" {
		t.Fatalf("%d %q %q", response.StatusCode, body, response.Header.Get("Content-Range"))
	}
}

// Zurg answers a part with 200 and a Content-Range at times; a player
// expects 206.
func TestAPartAnsweredAs200WithItsRangeIsA206(t *testing.T) {
	fake, client := library(t)
	fake.Quirk = true
	response, err := client.Open(context.Background(), "movies", "Some.Film.2022.1080p.BluRay.x264-GRP/Some.Film.2022.1080p.BluRay.x264-GRP.mkv", http.Header{"Range": {"bytes=2-"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusPartialContent || string(body) != "lm" {
		t.Fatalf("%d %q", response.StatusCode, body)
	}
}

func TestAWholeFileAnsweredForAPartWillNotDo(t *testing.T) {
	fake, client := library(t)
	fake.IgnoreRange = true
	_, err := client.Open(context.Background(), "movies", "Some.Film.2022.1080p.BluRay.x264-GRP/Some.Film.2022.1080p.BluRay.x264-GRP.mkv", http.Header{"Range": {"bytes=2-"}}, false)
	if !errors.Is(err, zurg.ErrRangeIgnored) {
		t.Fatalf("got %v", err)
	}
}

func TestAFileZurgNoLongerHasIsNotFound(t *testing.T) {
	_, client := library(t)
	if _, err := client.Open(context.Background(), "movies", "gone/gone.mkv", http.Header{}, false); !errors.Is(err, zurg.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestTheAdaptersTestNamesZurgsFolders(t *testing.T) {
	fake := zurgtest.New()
	fake.Dir("movies", added)
	fake.Dir("shows", added)
	server := httptest.NewServer(fake)
	defer server.Close()
	result := zurg.Adapter().Test(context.Background(), adapters.Settings{Values: map[string]any{"url": server.URL}})
	if !result.OK || !strings.Contains(result.Message, "2 folders") {
		t.Fatalf("result %+v", result)
	}
	refused := zurg.Adapter().Test(context.Background(), adapters.Settings{Values: map[string]any{"url": "http://127.0.0.1:1"}})
	if refused.OK {
		t.Fatalf("an address with nothing behind it: %+v", refused)
	}
}
