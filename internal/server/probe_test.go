package server_test

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"foyer/internal/library"
)

// mkv is a two-second Matroska file made with ffmpeg's own encoders, with
// German audio and a Japanese subtitle.
func mkv(t *testing.T) []byte {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " is not installed")
		}
	}
	dir := t.TempDir()
	srt, out := filepath.Join(dir, "s.srt"), filepath.Join(dir, "s.mkv")
	_ = os.WriteFile(srt, []byte("1\n00:00:00,000 --> 00:00:01,000\nやあ\n"), 0o600)
	cmd := exec.Command("ffmpeg", "-hide_banner", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=s=1920x1080:d=1", "-f", "lavfi", "-i", "sine=d=1", "-i", srt,
		"-map", "0:v", "-map", "1:a", "-map", "2:s", "-c:v", "mpeg4", "-c:a", "aac", "-c:s", "srt",
		"-metadata:s:a:0", "language=ger", "-metadata:s:s:0", "language=jpn", out)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, output)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestFFprobeSaysWhatEachFileIs(t *testing.T) {
	data := mkv(t)
	s := scanLibrary(t)
	s.zurg.File("movies/Some.Film.2022.1080p.BluRay.x264-GRP/Some.Film.2022.1080p.BluRay.x264-GRP.mkv", data, monday)
	if got := s.call(http.MethodPut, "/api/foyer/admin/adapters/ffmpeg", s.admin, map[string]any{"enabled": true, "values": map[string]any{"concurrency": 2}}); got.status != http.StatusOK {
		t.Fatalf("ffmpeg: %d %v", got.status, got.body)
	}
	if err := s.server.Streams.StartTwin(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.server.Streams.StopTwin(context.Background()) })

	// The newest first, a few at a time: until none is left to try now.
	for range 5 {
		if _, err := s.server.Probes.Once(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	file, err := s.app.FindFirstRecordByFilter(library.FilesCollection, "name = 'Some.Film.2022.1080p.BluRay.x264-GRP.mkv'")
	if err != nil {
		t.Fatal(err)
	}
	if file.GetString("probe_state") != "done" || file.GetString("probed_at") == "" {
		t.Fatalf("the real file: %s %q", file.GetString("probe_state"), file.GetString("probe_error"))
	}
	film := s.title("movies", "Some Film")
	var languages, subtitles []string
	_ = film.UnmarshalJSONField("audio_languages", &languages)
	_ = film.UnmarshalJSONField("subtitle_languages", &subtitles)
	if !slices.Contains(languages, "de") || !slices.Contains(subtitles, "ja") || !slices.Contains(film.GetStringSlice("encoding"), "mpeg4") {
		t.Fatalf("the film's lists: audio %v, subtitles %v, encoding %v", languages, subtitles, film.GetStringSlice("encoding"))
	}

	// A file that is no video is the file's own failure: tried again later,
	// and its error says no address.
	other, err := s.app.FindFirstRecordByFilter(library.FilesCollection, "name = 'Twin.Title.mkv'")
	if err != nil {
		t.Fatal(err)
	}
	if other.GetString("probe_state") != "pending" || other.GetInt("probe_attempts") != 1 || other.GetString("probe_next_at") == "" {
		t.Fatalf("a file that is no video: %s %d %q", other.GetString("probe_state"), other.GetInt("probe_attempts"), other.GetString("probe_next_at"))
	}
	if said := other.GetString("probe_error"); said == "" || strings.Contains(said, "127.0.0.1") || strings.Contains(said, "http://") {
		t.Fatalf("its error: %q", said)
	}
}
