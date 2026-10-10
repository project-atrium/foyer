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

// A tool gets the PATH and libva's settings, and nothing else of Foyer's:
// ffmpeg would send even a loopback read through http_proxy.
func TestAToolGetsNothingOfFoyersEnvironmentButTheGPUs(t *testing.T) {
	env, err := exec.LookPath("env")
	if err != nil {
		t.Skip("no env here")
	}
	t.Setenv("http_proxy", "http://proxy.invalid:3128")
	t.Setenv("FOYER_ADMIN_PASSWORD", "secret")
	t.Setenv("LIBVA_DRIVER_NAME", "iHD")
	out, err := Command(context.Background(), env).Output()
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, "LIBVA_DRIVER_NAME=iHD") || !strings.Contains(got, "PATH=") {
		t.Errorf("missing what it needs: %q", got)
	}
	if strings.Contains(got, "proxy.invalid") || strings.Contains(got, "secret") {
		t.Errorf("Foyer's environment leaked: %q", got)
	}
}
