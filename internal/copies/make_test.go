package copies

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"foyer/internal/media"
	"foyer/internal/probe"
)

// loopback serves a file as Foyer's loopback does: by ranges, to ffmpeg.
type loopback struct {
	server *httptest.Server
	path   string
}

func (l *loopback) Grant(string, time.Duration) (string, func(), error) {
	return l.server.URL + l.path, func() {}, nil
}
func (l *loopback) Loopback() string { return strings.TrimPrefix(l.server.URL, "http://") }

func tools(t *testing.T) (string, string) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not on the PATH")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not on the PATH")
	}
	return ffmpeg, ffprobe
}

// film makes so many seconds of a 720p Matroska film: H.264, AC-3 5.1 in
// English, an English commentary and AAC in German, English and German
// subtitles, and two chapters — as mkvmerge would, with its statistics.
func film(t *testing.T, ffmpeg string, seconds string) []byte {
	t.Helper()
	dir := t.TempDir()
	srt := filepath.Join(dir, "subs.srt")
	if err := os.WriteFile(srt, []byte("1\n00:00:01,000 --> 00:00:03,000\nHello there\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	chapters := filepath.Join(dir, "chapters.txt")
	if err := os.WriteFile(chapters, []byte(";FFMETADATA1\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=0\nEND=4000\ntitle=Opening\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=4000\nEND=8000\ntitle=Ending\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "film.mkv")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=24", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-i", srt, "-i", srt, "-i", chapters, "-t", seconds,
		"-map", "0:v", "-map", "1:a", "-map", "1:a", "-map", "1:a", "-map", "2:s", "-map", "3:s", "-map_chapters", "4",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a:0", "ac3", "-ac:a:0", "6", "-c:a:1", "ac3", "-ac:a:1", "2", "-c:a:2", "aac", "-ac:a:2", "2", "-c:s", "srt",
		"-metadata:s:a:0", "language=eng", "-metadata:s:a:1", "language=eng", "-metadata:s:a:1", "title=Commentary",
		"-metadata:s:a:2", "language=deu", "-metadata:s:s:0", "language=eng", "-metadata:s:s:1", "language=deu",
		"-metadata:s:v:0", "BPS-eng=9999999", "-metadata:s:a:0", "NUMBER_OF_BYTES=12345", out)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, output)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// shared serves a file's bytes by ranges, as the loopback does.
func shared(t *testing.T, b []byte) *loopback {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/g/film.mkv" {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, "film.mkv", time.Time{}, bytes.NewReader(b))
	}))
	t.Cleanup(server.Close)
	return &loopback{server: server, path: "/g/film.mkv"}
}

// probed is what ffprobe reads of the file, as Foyer keeps it.
func probed(t *testing.T, ffprobe string, l *loopback) media.Version {
	t.Helper()
	raw, err := probe.Run(context.Background(), ffprobe, l.server.URL+l.path, l.Loopback())
	if err != nil {
		t.Fatal(err)
	}
	v, err := probe.Map(raw, "film.mkv")
	if err != nil {
		t.Fatal(err)
	}
	v.File = "file1"
	return v
}

func manager(t *testing.T, ffmpeg string, l *loopback, root string) (*Manager, context.CancelFunc) {
	t.Helper()
	m := New(Options{
		Root:     root,
		Settings: func() Settings { return Settings{Tool: ffmpeg, Effort: "fast", AtOnce: 1, KeepDays: 14} },
		Grants:   l,
		Sign:     func(b []byte) ([]byte, error) { return bytes.Repeat([]byte{1}, 32), nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); m.Close(); <-done })
	return m, cancel
}

func until(t *testing.T, m *Manager, r Request, state string) Status {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		s := m.Ask(r)
		if s.State == state {
			return s
		}
		if s.State == Failed || time.Now().After(deadline) {
			t.Fatalf("waiting for %s: %+v", state, s)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// A copy is made once, of what the file is: a smaller picture in AV1, a
// sound for each language in Opus, the subtitles and chapters, and nothing
// of mkvmerge's statistics; kept, handed out, then let go of in time.
func TestACopyIsMadeOnceKeptAndLetGoOf(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	l := shared(t, film(t, ffmpeg, "10"))
	v := probed(t, ffprobe, l)
	root := t.TempDir()
	m, _ := manager(t, ffmpeg, l, root)
	can, ok := m.Abilities(context.Background())
	if !ok || !can.Encoders["libsvtav1"] || !can.Encoders["libopus"] {
		t.Skipf("this ffmpeg makes no AV1 or Opus: %+v", can)
	}
	encode := Encode{Video: "av1", Preference: "size", MaxHeight: 360, Audio: "opus", Container: "mkv"}
	recipe, err := Plan(v, 0, encode, can)
	if err != nil {
		t.Fatal(err)
	}
	r := Request{Key: Key("file1", v.Size, "", encode.For(v)), File: "file1", Source: v, Recipe: recipe, Title: "Some Film", User: "Alex"}
	if s := m.Ask(r); s.State != Queued {
		t.Fatalf("first asked: %+v", s)
	}
	done := until(t, m, r, Done)
	if done.Made == nil || done.Made.By != "Alex" || done.Made.Title != "Some Film" {
		t.Fatalf("made: %+v", done.Made)
	}
	path, made, ok := m.Path(r.Key)
	if !ok || made.Size <= 0 || filepath.Base(path) != r.Key+".mkv" {
		t.Fatalf("kept at %q: %+v", path, made)
	}

	out, err := exec.Command(ffprobe, "-v", "error", "-show_streams", "-show_chapters", "-of", "json", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var said struct {
		Streams []struct {
			Type     string            `json:"codec_type"`
			Codec    string            `json:"codec_name"`
			Width    int               `json:"width"`
			Height   int               `json:"height"`
			PixFmt   string            `json:"pix_fmt"`
			Channels int               `json:"channels"`
			Tags     map[string]string `json:"tags"`
		} `json:"streams"`
		Chapters []any `json:"chapters"`
	}
	if err := json.Unmarshal(out, &said); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range said.Streams {
		for tag := range s.Tags {
			if strings.HasPrefix(tag, "BPS") || strings.HasPrefix(tag, "NUMBER_OF") {
				t.Errorf("%s kept %s", s.Type, tag)
			}
		}
		switch s.Type {
		case "video":
			got = append(got, s.Codec+" "+itoa(int64(s.Width))+"x"+itoa(int64(s.Height))+" "+s.PixFmt)
		case "audio":
			got = append(got, s.Codec+" "+itoa(int64(s.Channels))+" "+s.Tags["language"])
		default:
			got = append(got, s.Codec+" "+s.Tags["language"])
		}
	}
	// The commentary is gone; English and German stay, each once.
	if want := "av1 640x360 yuv420p10le|opus 6 eng|opus 2 deu|subrip eng|subrip deu"; strings.Join(got, "|") != want {
		t.Errorf("the copy holds %q", strings.Join(got, "|"))
	}
	if len(said.Chapters) != 2 {
		t.Errorf("%d chapters", len(said.Chapters))
	}
	if made.Version.Video.Codec != "av1" || made.Version.Size != made.Size || len(made.Version.Audio) != 2 || made.Version.Audio[0].Default != true {
		t.Errorf("what it is: %+v", made.Version)
	}

	// Kept: another asking is the same copy, at once.
	again := m.Ask(r)
	if again.State != Done || again.Made.Size != made.Size {
		t.Errorf("asked again: %+v", again)
	}
	if kept := m.Kept(); len(kept) != 1 || kept[0].Key != r.Key {
		t.Errorf("kept %+v", kept)
	}
	// Nothing half made is left.
	if parts, _ := filepath.Glob(filepath.Join(root, "*.part")); len(parts) > 0 {
		t.Errorf("left %v", parts)
	}
	// Not asked for in its days, it goes.
	m.mu.Lock()
	made.Asked = time.Now().Add(-15 * 24 * time.Hour)
	if err := m.keep(made); err != nil {
		t.Fatal(err)
	}
	m.mu.Unlock()
	m.prune(0)
	if _, _, ok := m.Path(r.Key); ok {
		t.Error("kept past its days")
	}
	if left, _ := os.ReadDir(root); len(left) != 0 {
		t.Errorf("left %d files", len(left))
	}
}

// A copy that cannot be made says why — nothing of the loopback's address
// in it — and is tried again only once asked for after a while.
func TestACopyThatCannotBeMadeSaysSoAndWaitsToBeAskedAgain(t *testing.T) {
	ffmpeg, _ := tools(t)
	l := shared(t, []byte("not a film"))
	l.path = "/g/missing.mkv"
	m, _ := manager(t, ffmpeg, l, t.TempDir())
	v := otto()
	can, ok := m.Abilities(context.Background())
	if !ok {
		t.Skip("this ffmpeg makes no copies")
	}
	recipe, err := Plan(v, 0, asLoge, can)
	if err != nil {
		t.Fatal(err)
	}
	r := Request{Key: Key("file2", v.Size, "", asLoge), File: "file2", Source: v, Recipe: recipe}
	m.Ask(r)
	failed := until(t, m, r, Failed)
	if failed.Err == "" || strings.Contains(failed.Err, l.Loopback()) {
		t.Errorf("said %q", failed.Err)
	}
	if s := m.Ask(r); s.State != Failed {
		t.Errorf("asked again at once: %+v", s)
	}
	m.mu.Lock()
	m.jobs[r.Key].failedAt = time.Now().Add(-2 * retryAfter)
	m.mu.Unlock()
	if s := m.Ask(r); s.State != Queued {
		t.Errorf("asked again later: %+v", s)
	}
}

// A copy whose ffmpeg the system stopped — the kernel, out of memory, says
// nothing on its stderr — says so, never "ffmpeg stopped".
func TestACopyTheSystemStoppedSaysWhy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no SIGKILL here")
	}
	tool := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nkill -KILL $$\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	m, _ := manager(t, tool, shared(t, []byte("a film")), t.TempDir())
	r := Request{Key: "00112233445566778899aabbccddeeff", File: "file4", Source: media.Version{Video: &media.Video{Width: 1920, Height: 1080}},
		Recipe: Recipe{Encode: asLoge, Video: VideoPlan{Codec: "av1", Width: 1920, Height: 1080, BitDepth: 10, CRF: 30}}}
	m.Ask(r)
	failed := until(t, m, r, Failed)
	if !strings.HasPrefix(failed.Err, "the system stopped ffmpeg, most likely for want of memory: a copy like this takes about ") {
		t.Errorf("said %q", failed.Err)
	}
}

// A copy that came out no smaller than its file — an estimate is an
// estimate — is let go of, offered no more, and remembered as such; one a
// fifth smaller is kept.
func TestACopyNoSmallerThanItsFileIsLetGo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no shell here")
	}
	// ffmpeg's stand-in writes 5,000 bytes to the last thing it is given.
	tool := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nfor a; do out=$a; done\nhead -c 5000 /dev/zero > \"$out\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	m, _ := manager(t, tool, shared(t, []byte("a film")), root)
	ask := func(key string, size int64) Status {
		r := Request{Key: key, File: "file5", Source: media.Version{Size: size, Video: &media.Video{Width: 1280, Height: 720}},
			Recipe: Recipe{Encode: asLoge, Video: VideoPlan{Codec: "av1", Width: 1280, Height: 720, BitDepth: 10, CRF: 30}}}
		m.Ask(r)
		return until(t, m, r, Done)
	}

	bigger := ask("0123456789abcdef0123456789abcdef", 6000)
	if bigger.Made == nil || !bigger.Made.NoSmaller || bigger.Made.Name != "" {
		t.Fatalf("made %+v", bigger.Made)
	}
	if _, _, ok := m.Path(bigger.Made.Key); ok || !m.NoSmaller(bigger.Made.Key) || len(m.Kept()) != 0 {
		t.Error("a copy no smaller is handed out or shown as kept")
	}
	if left, _ := filepath.Glob(filepath.Join(root, "0123456789abcdef0123456789abcdef*")); len(left) != 1 || filepath.Ext(left[0]) != ".json" {
		t.Errorf("left %v", left)
	}

	smaller := ask("fedcba9876543210fedcba9876543210", 100_000)
	if _, made, ok := m.Path(smaller.Made.Key); !ok || made.NoSmaller || made.Size != 5000 || m.NoSmaller(made.Key) {
		t.Errorf("kept %+v", made)
	}

	// Let go of in its days, the record leaves nothing behind.
	m.mu.Lock()
	gone := *bigger.Made
	gone.Asked = time.Now().Add(-15 * 24 * time.Hour)
	if err := m.keep(&gone); err != nil {
		t.Fatal(err)
	}
	m.mu.Unlock()
	m.prune(0)
	if m.NoSmaller(gone.Key) {
		t.Error("kept past its days")
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("the folder went with it: %v", err)
	}
}

// A copy stopped while it is made leaves nothing behind.
func TestACopyStoppedLeavesNothing(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	l := shared(t, film(t, ffmpeg, "120"))
	v := probed(t, ffprobe, l)
	root := t.TempDir()
	m, _ := manager(t, ffmpeg, l, root)
	can, ok := m.Abilities(context.Background())
	if !ok {
		t.Skip("this ffmpeg makes no copies")
	}
	encode := Encode{Video: "av1", Preference: "quality", Audio: "opus", Container: "mkv"}
	recipe, err := Plan(v, 0, encode, can)
	if err != nil {
		t.Fatal(err)
	}
	r := Request{Key: Key("file3", v.Size, "", encode.For(v)), File: "file3", Source: v, Recipe: recipe}
	m.Ask(r)
	until(t, m, r, Running)
	if making := m.Making(); len(making) != 1 || making[0].State != Running {
		t.Fatalf("making %+v", making)
	}
	if !m.Stop(r.Key) {
		t.Fatal("nothing to stop")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		m.mu.Lock()
		running := m.running
		m.mu.Unlock()
		if running == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ffmpeg went on")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if left, _ := os.ReadDir(root); len(left) != 0 {
		t.Errorf("left %d files", len(left))
	}
	if _, _, ok := m.Path(r.Key); ok || len(m.Making()) != 0 {
		t.Error("a stopped copy kept")
	}
}

// A copy its Maker makes — a site's renditions fetched and put together —
// waits on the network, so it runs beside a film being made anew, never
// behind it: said by its time left, kept with what the Maker says it is,
// and nothing of its folder left. A folder an earlier run left goes at start.
func TestAFetchedCopyNeverWaitsBehindOneMadeAnew(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "0123456789abcdef0123456789abcdef-1.part")
	if err := os.MkdirAll(left, 0o700); err != nil {
		t.Fatal(err)
	}
	m := New(Options{
		Root:     root,
		Settings: func() Settings { return Settings{Tool: "ffmpeg", AtOnce: 1, KeepDays: 14} },
		Sign:     func(b []byte) ([]byte, error) { return bytes.Repeat([]byte{1}, 32), nil },
	})
	// The one place for a film made anew is taken.
	m.running = 1
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); m.Close(); <-done })

	release := make(chan struct{})
	var tool string
	r := Request{
		Key: "fedcba9876543210fedcba9876543210", File: "youtube:x3TJFoQWCHA", Title: "The River, a Film",
		Source: media.Version{DurationMs: 600_000},
		Recipe: Recipe{Encode: Encode{Video: "vp9", MaxHeight: 1080, Audio: "opus", Container: "mkv"}, Bytes: 10_000},
		Make: func(ctx context.Context, dir, ffmpeg string, progress func(done, speed float64)) (string, media.Version, error) {
			tool = ffmpeg
			progress(0.5, 1000)
			select {
			case <-release:
			case <-ctx.Done():
				return "", media.Version{}, ctx.Err()
			}
			made := filepath.Join(dir, "video.mkv")
			if err := os.WriteFile(made, []byte("both, put together"), 0o600); err != nil {
				return "", media.Version{}, err
			}
			return made, media.Version{Container: "mkv", Video: &media.Video{Codec: "vp9", Height: 1080}}, nil
		},
	}
	until(t, m, r, Running)
	deadline := time.Now().Add(5 * time.Second)
	for {
		making := m.Making()
		if len(making) == 1 && making[0].Progress == 0.5 {
			if making[0].Speed != 0 || making[0].EtaMs != 5000 || making[0].Encode.Video != "vp9" {
				t.Errorf("making %+v", making[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("making %+v", making)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(left); !os.IsNotExist(err) {
		t.Error("an earlier run's folder was kept")
	}
	close(release)
	s := until(t, m, r, Done)
	if tool != "ffmpeg" || s.Made.Name != r.Key+".mkv" || s.Made.Size != 18 || s.Made.Version.Size != 18 || s.Made.Version.Video.Codec != "vp9" || s.Made.Encode.MaxHeight != 1080 {
		t.Errorf("made %+v with %q", s.Made, tool)
	}
	entries, _ := os.ReadDir(root)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, " ") != r.Key+".json "+r.Key+".mkv" {
		t.Errorf("kept %q", names)
	}
}
