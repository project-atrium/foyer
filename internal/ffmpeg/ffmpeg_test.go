package ffmpeg

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"foyer/internal/adapters"
)

func TestDetectsTheToolsAndAMissingDevice(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	d := Detect(context.Background(), Tools{FFmpeg: "ffmpeg", FFprobe: "ffprobe", Device: "/nonexistent/renderD128"})
	if d.FFmpeg == "" || d.FFprobe == "" || d.Present || len(d.VAAPI) != 0 {
		t.Fatalf("detection %+v", d)
	}
	if !strings.Contains(d.Summary(), "no render device at /nonexistent/renderD128") {
		t.Fatalf("summary %q", d.Summary())
	}
}

func TestSaysWhatIsNotInstalled(t *testing.T) {
	d := Detect(context.Background(), Tools{FFmpeg: "/nonexistent/ffmpeg", FFprobe: "/nonexistent/ffprobe"})
	if d.FFmpeg != "" || len(d.Errors) != 2 || !strings.Contains(d.Errors[0], "not installed") {
		t.Fatalf("detection %+v", d)
	}
	result := Adapter().Test(context.Background(), adapters.Settings{Values: map[string]any{"ffprobe": "/nonexistent/ffprobe", "ffmpeg": "/nonexistent/ffmpeg"}})
	if result.OK {
		t.Fatalf("result %+v", result)
	}
}

func TestTheGPUIsSummedUpInALine(t *testing.T) {
	d := Detection{FFprobe: "7.1", FFmpeg: "7.1", Device: "/dev/dri/renderD128", Present: true,
		VAAPI: []Encoder{{Codec: "h264", OK: true}, {Codec: "hevc", OK: true}}, QSV: []Encoder{{Codec: "h264", Error: "no"}}}
	if got := d.Summary(); got != "ffprobe 7.1 · ffmpeg 7.1 · /dev/dri/renderD128: VAAPI H264, HEVC" {
		t.Fatalf("summary %q", got)
	}
}
