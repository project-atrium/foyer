package server_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/pocketbase/pocketbase/core"

	"foyer/internal/adapters/webdav/webdavtest"
	"foyer/internal/hls"
	"foyer/internal/library"
)

// video makes a short film with ffmpeg, as a share would hold it: through a
// pipe, with no way back to write its Cues, where indexed is false.
func video(t *testing.T, indexed bool) []byte {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not on the PATH")
	}
	out := filepath.Join(t.TempDir(), "film.mkv")
	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24",
		"-t", "30", "-c:v", "libx264", "-preset", "ultrafast", "-g", "48", "-f", "matroska"}
	cmd := exec.Command(ffmpeg, append(args, out)...)
	if !indexed {
		cmd = exec.Command(ffmpeg, append(args, "pipe:1")...)
		f, err := os.Create(out)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		cmd.Stdout = f
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg: %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fileNamed is a library file's record by its name.
func (h *harness) fileNamed(name string) *core.Record {
	h.t.Helper()
	f, err := h.app.FindFirstRecordByData(library.FilesCollection, "name", name)
	if err != nil {
		h.t.Fatalf("no file %s: %v", name, err)
	}
	return f
}

// A file's keyframes are read from its own index through the share, the
// first time a remux needs them, and kept: never read twice, and kept as
// none for a file that keeps no index — until the file changes.
func TestAFilesKeyframesAreReadOnceAndKept(t *testing.T) {
	h := start(t, options{})
	admin := h.superuser()
	share := webdavtest.New()
	share.AsZurg = true
	indexed, piped := video(t, true), video(t, false)
	share.File("movies/Some.Film.2022.1080p.BluRay.x264-GRP/Some.Film.2022.1080p.BluRay.x264-GRP.mkv", indexed, monday)
	share.Dir("movies/Some.Film.2022.1080p.BluRay.x264-GRP", monday)
	share.File("movies/Piped.Film.2021.1080p.WEB-DL.x264-GRP/Piped.Film.2021.1080p.WEB-DL.x264-GRP.mkv", piped, monday)
	share.Dir("movies/Piped.Film.2021.1080p.WEB-DL.x264-GRP", monday)
	server := httptest.NewServer(share)
	t.Cleanup(server.Close)
	h.share(admin, "Zurg", server.URL+"/dav")
	movies := h.addLibrary(admin, "movies", "movies", "movies")["id"].(string)
	if err := h.server.Library.Scan(context.Background(), movies, true); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	film := h.fileNamed("Some.Film.2022.1080p.BluRay.x264-GRP.mkv")
	idx, err := h.server.Library.Keyframes(ctx, film)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Keyframes) < 14 || idx.Keyframes[0] != 0 || idx.Keyframes[1] != 2000 || idx.Video.Height != 180 || idx.DurationMs < 29_000 {
		t.Fatalf("the index %v %+v %d", idx.Keyframes, idx.Video, idx.DurationMs)
	}
	kept := h.fileNamed("Some.Film.2022.1080p.BluRay.x264-GRP.mkv")
	if kept.GetString("keyframes_state") != "done" {
		t.Fatalf("kept as %q", kept.GetString("keyframes_state"))
	}
	// Kept: the share is not asked again, even gone.
	share.Remove("movies/Some.Film.2022.1080p.BluRay.x264-GRP/Some.Film.2022.1080p.BluRay.x264-GRP.mkv")
	again, err := h.server.Library.Keyframes(ctx, kept)
	if err != nil || len(again.Keyframes) != len(idx.Keyframes) {
		t.Fatalf("read again: %v, %d keyframes", err, len(again.Keyframes))
	}

	pipedFile := h.fileNamed("Piped.Film.2021.1080p.WEB-DL.x264-GRP.mkv")
	if _, err := h.server.Library.Keyframes(ctx, pipedFile); !errors.Is(err, hls.ErrNoIndex) {
		t.Fatalf("a file without Cues: %v", err)
	}
	if got := h.fileNamed("Piped.Film.2021.1080p.WEB-DL.x264-GRP.mkv").GetString("keyframes_state"); got != "none" {
		t.Fatalf("kept as %q", got)
	}
	// Another file under its name — Zurg's repair — is read again.
	share.File("movies/Piped.Film.2021.1080p.WEB-DL.x264-GRP/Piped.Film.2021.1080p.WEB-DL.x264-GRP.mkv", indexed, tuesday)
	if err := h.server.Library.Scan(ctx, movies, true); err != nil {
		t.Fatal(err)
	}
	changed := h.fileNamed("Piped.Film.2021.1080p.WEB-DL.x264-GRP.mkv")
	if changed.GetString("keyframes_state") != "" {
		t.Fatalf("a changed file kept its keyframes: %q", changed.GetString("keyframes_state"))
	}
	if idx, err := h.server.Library.Keyframes(ctx, changed); err != nil || len(idx.Keyframes) == 0 {
		t.Fatalf("the changed file: %v", err)
	}
}
