package hls

import (
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// tools are ffmpeg and ffprobe, or the test is skipped: they make the files
// these tests read, as a real share would hold them.
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

// make writes a file of a picture and a sound, args choosing how.
func makeFile(t *testing.T, ffmpeg, name string, seconds int, args ...string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), name)
	base := []string{"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=24000/1001",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", strconv.Itoa(seconds)}
	cmd := exec.Command(ffmpeg, append(append(base, args...), out)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, output)
	}
	return out
}

// keyframesOf are the picture's keyframes as ffprobe reads them, in
// milliseconds: what the index must say without reading the file through.
func keyframesOf(t *testing.T, ffprobe, file string) []int64 {
	t.Helper()
	out, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0", "-show_entries", "packet=pts_time,flags", "-of", "csv=p=0", file).Output()
	if err != nil {
		t.Fatal(err)
	}
	var times []int64
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(line, ",")
		if len(parts) < 2 || !strings.Contains(parts[1], "K") {
			continue
		}
		s, err := strconv.ParseFloat(parts[0], 64)
		if err != nil {
			t.Fatalf("a pts of %q", line)
		}
		times = append(times, int64(math.Round(s*1000)))
	}
	return times
}

// reader reads a file by ranges, as the share is read, and counts what it
// was asked for.
func reader(t *testing.T, file string) (RangeReader, *atomic.Int64, int64) {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	var read atomic.Int64
	return func(_ context.Context, offset, length int64) ([]byte, error) {
		if offset >= info.Size() {
			return nil, nil
		}
		b := make([]byte, min(length, info.Size()-offset))
		n, err := f.ReadAt(b, offset)
		read.Add(int64(n))
		if err != nil && n < len(b) {
			return nil, err
		}
		return b[:n], nil
	}, &read, info.Size()
}

func sameTimes(t *testing.T, got, want []int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d keyframes, ffprobe reads %d: %v, %v", len(got), len(want), head(got), head(want))
	}
	for i := range got {
		if d := got[i] - want[i]; d < -1 || d > 1 {
			t.Fatalf("keyframe %d at %d ms, ffprobe reads %d: %v, %v", i, got[i], want[i], head(got), head(want))
		}
	}
}

func head(times []int64) []int64 { return times[:min(len(times), 6)] }

func TestAMatroskaFilesKeyframesAreItsCues(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	for _, c := range []struct {
		name string
		args []string
	}{
		// ffmpeg writes the Cues at the end, with a SeekHead at the start.
		{"cues at the end", []string{"-c:v", "libx264", "-preset", "ultrafast", "-g", "48", "-bf", "2", "-c:a", "aac"}},
		// mkvmerge's way, and ffmpeg's with room kept: the Cues before the Clusters.
		{"cues at the front", []string{"-c:v", "libx264", "-preset", "ultrafast", "-g", "36", "-bf", "2", "-c:a", "aac", "-reserve_index_space", "64k"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			file := makeFile(t, ffmpeg, "film.mkv", 90, c.args...)
			read, bytes, size := reader(t, file)
			idx, err := ReadIndex(context.Background(), read, "film.mkv", size)
			if err != nil {
				t.Fatal(err)
			}
			sameTimes(t, idx.Keyframes, keyframesOf(t, ffprobe, file))
			if idx.DurationMs < 89_000 || idx.DurationMs > 91_000 || idx.Video.Width != 640 || idx.Video.Height != 360 ||
				!strings.HasPrefix(idx.Video.Codec, "avc1.") || math.Abs(idx.Video.FrameRate-23.976) > 0.01 {
				t.Errorf("the index %+v", idx.Video)
			}
			// The header and the Cues: a few hundred kilobytes, never the file.
			if read := bytes.Load(); read > 512<<10 || read >= size/2 {
				t.Errorf("read %d bytes of %d", read, size)
			}
		})
	}
}

func TestAnMP4sKeyframesAreItsSyncSamples(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	for _, c := range []struct {
		name, codec string
		args        []string
	}{
		// The movie box after the media, B-frames and the edit list they bring.
		{"moov at the end", "avc1.", []string{"-c:v", "libx264", "-preset", "ultrafast", "-g", "48", "-bf", "2", "-c:a", "aac"}},
		{"moov at the start", "avc1.", []string{"-c:v", "libx264", "-preset", "ultrafast", "-g", "30", "-bf", "3", "-c:a", "aac", "-movflags", "+faststart"}},
		{"HEVC, tagged for Apple", "hvc1.", []string{"-c:v", "libx265", "-preset", "ultrafast", "-x265-params", "keyint=48:bframes=2:log-level=error", "-tag:v", "hvc1", "-c:a", "aac"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			file := makeFile(t, ffmpeg, "film.mp4", 60, c.args...)
			read, bytes, size := reader(t, file)
			idx, err := ReadIndex(context.Background(), read, "film.mp4", size)
			if err != nil {
				t.Fatal(err)
			}
			sameTimes(t, idx.Keyframes, keyframesOf(t, ffprobe, file))
			if idx.DurationMs < 59_000 || idx.DurationMs > 61_000 || !strings.HasPrefix(idx.Video.Codec, c.codec) || idx.Video.Height != 360 {
				t.Errorf("the index %+v, %d ms", idx.Video, idx.DurationMs)
			}
			if read := bytes.Load(); read > 512<<10 || read >= size/2 {
				t.Errorf("read %d bytes of %d", read, size)
			}
		})
	}
}

// A Matroska file written to a pipe could not go back for its Cues: it
// plays as it is, in a player that opens it, never remuxed blind.
func TestAFileWithoutAnIndexHasNone(t *testing.T) {
	ffmpeg, _ := tools(t)
	out := filepath.Join(t.TempDir(), "piped.mkv")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25",
		"-t", "20", "-c:v", "libx264", "-preset", "ultrafast", "-f", "matroska", "pipe:1")
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = f
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	read, _, size := reader(t, out)
	if _, err := ReadIndex(context.Background(), read, "piped.mkv", size); !errors.Is(err, ErrNoIndex) {
		t.Errorf("a file without Cues: %v", err)
	}
	if _, err := ReadIndex(context.Background(), read, "film.avi", size); !errors.Is(err, ErrNoIndex) {
		t.Errorf("a container this does not read: %v", err)
	}
}

// A share that fails is no answer, and says so: the index is asked again
// later, never kept as none.
func TestAShareThatFailsIsNoAnswer(t *testing.T) {
	broken := errors.New("the share broke")
	read := func(context.Context, int64, int64) ([]byte, error) { return nil, broken }
	for _, name := range []string{"film.mkv", "film.mp4"} {
		if _, err := ReadIndex(context.Background(), read, name, 1<<30); !errors.Is(err, broken) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestCodecNamesAreTheConfigurationsOwn(t *testing.T) {
	for _, c := range []struct {
		kind   string
		config []byte
		want   string
	}{
		{"avc", []byte{1, 0x64, 0x00, 0x28, 0xFF}, "avc1.640028"},
		{"avc", []byte{1, 0x4D, 0x40, 0x1F}, "avc1.4D401F"},
		// Main 10, level 4, the constraint byte 0x90 kept and the zeros after it dropped.
		{"hevc", []byte{1, 0x02, 0x20, 0, 0, 0, 0x90, 0, 0, 0, 0, 0, 120}, "hvc1.2.4.L120.90"},
		// Main 10 at 5.1, high tier: the profile compatibility flags reversed.
		{"hevc", []byte{1, 0x22, 0x20, 0, 0, 0, 0xB0, 0, 0, 0, 0, 0, 153}, "hvc1.2.4.H153.B0"},
		{"av1", []byte{0x81, 0x08, 0x4C}, "av01.0.08M.10"},
		{"av1", []byte{0x81, 0x0D, 0x00}, "av01.0.13M.08"},
		{"avc", []byte{0}, ""},
		{"hevc", []byte{1, 2}, ""},
	} {
		if got := codecName(c.kind, c.config); got != c.want {
			t.Errorf("%s %x: %q, want %q", c.kind, c.config, got, c.want)
		}
	}
}
