// Package ffmpeg is the ffmpeg and ffprobe Foyer runs: where they are, which
// versions, and whether the GPU — a render device such as
// /dev/dri/renderD128 — can encode with them. ffprobe says what each file is
// now; ffmpeg's transcoding comes later, and this is how the household knows
// beforehand that the GPU is reachable.
package ffmpeg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"foyer/internal/adapters"
)

// Key is this adapter's key in the registry.
const Key = "ffmpeg"

// Tools are the binaries, and the render device to try.
type Tools struct {
	FFmpeg  string
	FFprobe string
	Device  string
}

// FromSettings reads the FFmpeg adapter's settings.
func FromSettings(s adapters.Settings) Tools {
	t := Tools{FFmpeg: s.String("ffmpeg"), FFprobe: s.String("ffprobe"), Device: s.String("device")}
	if t.FFmpeg == "" {
		t.FFmpeg = "ffmpeg"
	}
	if t.FFprobe == "" {
		t.FFprobe = "ffprobe"
	}
	return t
}

// Command is a tool run with nothing of Foyer's environment but its PATH
// and libva's LIBVA_ settings: ffmpeg reads http_proxy, and would send even
// a loopback read through it, while LIBVA_DRIVER_NAME is how a GPU's driver
// is chosen where libva guesses wrong.
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "LIBVA_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// run runs a tool for a short answer: its output, or what it said as an
// error, cut short.
func run(ctx context.Context, timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var out, errOut bytes.Buffer
	cmd := Command(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &limited{&out, 1 << 20}, &limited{&errOut, 16 << 10}
	if err := cmd.Run(); err != nil {
		said := strings.TrimSpace(errOut.String())
		if len(said) > 500 {
			said = said[:500]
		}
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%s is not installed here", filepath.Base(name))
		}
		if said == "" {
			said = err.Error()
		}
		return "", errors.New(said)
	}
	return out.String(), nil
}

// limited keeps the first max bytes written to it, and drops the rest.
type limited struct {
	buf *bytes.Buffer
	max int
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room > 0 {
		l.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

var versionLine = regexp.MustCompile(`version (\S+)`)

func version(ctx context.Context, name string) (string, error) {
	out, err := run(ctx, 10*time.Second, name, "-hide_banner", "-version")
	if err != nil {
		return "", err
	}
	if m := versionLine.FindStringSubmatch(out); m != nil {
		return m[1], nil
	}
	return strings.SplitN(out, "\n", 2)[0], nil
}

// Encoder is one encode tried on the GPU.
type Encoder struct {
	Codec string `json:"codec"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Detection is what is installed, and what the GPU did when asked.
type Detection struct {
	FFmpeg   string    `json:"ffmpeg,omitempty"`
	FFprobe  string    `json:"ffprobe,omitempty"`
	HWAccels []string  `json:"hwaccels"`
	Device   string    `json:"device,omitempty"`
	Present  bool      `json:"present"`
	VAAPI    []Encoder `json:"vaapi,omitempty"`
	QSV      []Encoder `json:"qsv,omitempty"`
	Errors   []string  `json:"errors,omitempty"`
}

// Detect finds the tools and tries the render device: a few null frames
// encoded with VAAPI, and with QSV on Intel, each as the transcoder will ask.
func Detect(ctx context.Context, t Tools) Detection {
	d := Detection{HWAccels: []string{}, Device: t.Device}
	var err error
	if d.FFprobe, err = version(ctx, t.FFprobe); err != nil {
		d.Errors = append(d.Errors, "ffprobe: "+err.Error())
	}
	if d.FFmpeg, err = version(ctx, t.FFmpeg); err != nil {
		d.Errors = append(d.Errors, "ffmpeg: "+err.Error())
		return d
	}
	if out, err := run(ctx, 10*time.Second, t.FFmpeg, "-hide_banner", "-hwaccels"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasSuffix(line, ":") {
				d.HWAccels = append(d.HWAccels, line)
			}
		}
	}
	if t.Device == "" {
		return d
	}
	if _, err := os.Stat(t.Device); err != nil {
		return d
	}
	d.Present = true
	source := []string{"-f", "lavfi", "-i", "testsrc2=s=256x144:d=0.2"}
	for _, codec := range []string{"h264", "hevc"} {
		args := append([]string{"-hide_banner", "-v", "error", "-init_hw_device", "vaapi=va:" + t.Device, "-filter_hw_device", "va"}, source...)
		args = append(args, "-vf", "format=nv12,hwupload", "-c:v", codec+"_vaapi", "-frames:v", "5", "-f", "null", "-")
		_, err := run(ctx, 20*time.Second, t.FFmpeg, args...)
		d.VAAPI = append(d.VAAPI, encoder(codec, err))
	}
	for _, codec := range []string{"h264", "hevc"} {
		args := append([]string{"-hide_banner", "-v", "error", "-init_hw_device", "vaapi=va:" + t.Device, "-init_hw_device", "qsv=qs@va", "-filter_hw_device", "qs"}, source...)
		args = append(args, "-vf", "format=nv12,hwupload=extra_hw_frames=16", "-c:v", codec+"_qsv", "-frames:v", "5", "-f", "null", "-")
		_, err := run(ctx, 20*time.Second, t.FFmpeg, args...)
		d.QSV = append(d.QSV, encoder(codec, err))
	}
	return d
}

func encoder(codec string, err error) Encoder {
	if err != nil {
		return Encoder{Codec: codec, Error: err.Error()}
	}
	return Encoder{Codec: codec, OK: true}
}

// Summary is a detection in a line, as the Adapters page shows it.
func (d Detection) Summary() string {
	var parts []string
	if d.FFprobe != "" {
		parts = append(parts, "ffprobe "+d.FFprobe)
	}
	if d.FFmpeg != "" {
		parts = append(parts, "ffmpeg "+d.FFmpeg)
	}
	switch {
	case d.Device == "":
	case !d.Present:
		parts = append(parts, "no render device at "+d.Device)
	default:
		gpu := []string{}
		for _, set := range []struct {
			name     string
			encoders []Encoder
		}{{"VAAPI", d.VAAPI}, {"QSV", d.QSV}} {
			var ok []string
			for _, e := range set.encoders {
				if e.OK {
					ok = append(ok, strings.ToUpper(e.Codec))
				}
			}
			if len(ok) > 0 {
				gpu = append(gpu, set.name+" "+strings.Join(ok, ", "))
			}
		}
		if len(gpu) == 0 {
			parts = append(parts, d.Device+" encodes nothing")
		} else {
			parts = append(parts, d.Device+": "+strings.Join(gpu, "; "))
		}
	}
	if len(parts) == 0 {
		return "Nothing found."
	}
	return strings.Join(parts, " · ")
}

// Adapter is FFmpeg as the Adapters page shows it. Switched on, ffprobe says
// what each file in the library is.
func Adapter() adapters.Adapter {
	return adapters.Adapter{
		Key:         Key,
		ID:          "tools/ffmpeg",
		Category:    adapters.Tools,
		Single:      true,
		Name:        "FFmpeg",
		Description: "ffprobe reads what each file is — its picture, its audio and subtitle tracks, its chapters — through Foyer, a few at a time. ffmpeg makes smaller copies for downloads, and will transcode on the GPU.",
		Fields: []adapters.Field{
			{Key: "ffprobe", Label: "ffprobe", Type: adapters.Text, Default: "ffprobe", Help: "Its path, or its name on the PATH."},
			{Key: "ffmpeg", Label: "ffmpeg", Type: adapters.Text, Default: "ffmpeg", Help: "Its path, or its name on the PATH."},
			{Key: "device", Label: "Render device", Type: adapters.Text, Default: "/dev/dri/renderD128",
				Help: "The GPU, for transcoding to come. In Docker, pass it in with the compose override."},
			{Key: "concurrency", Label: "Files read at once", Type: adapters.Number, Default: 2, Min: 1, Max: 8,
				Help: "Each read asks the share — and Real-Debrid, behind Zurg — for a few megabytes."},
			{Key: "convert", Label: "Convert for players that cannot open a file", Type: adapters.Bool, Default: false,
				Help: "Remuxes a file to HLS for a player that plays its picture but not the file itself — an iPhone's own player and Matroska, a browser. Off, such a player is told to turn this on or to play the file with another: mpv and VLC open it as it is."},
			{Key: "copies", Label: "Make smaller copies for downloads", Type: adapters.Bool, Default: true,
				Help: "A device keeping a film asks for a copy made to its download settings — AV1 and Opus by default, a few GB where the file is tens — made here on the processor, about as long as the film runs or longer, then kept for whoever asks for the same. The copy reads the whole file from the share: behind Zurg, from Real-Debrid."},
			{Key: "copy_effort", Label: "Effort for copies", Type: adapters.Select, Default: "balanced",
				Options: []adapters.Option{{Value: "fast", Label: "Fast — a little bigger"}, {Value: "balanced", Label: "Balanced"}, {Value: "smallest", Label: "Smallest — slowest"}},
				Help:    "How long the processor spends on each copy."},
			{Key: "copies_at_once", Label: "Copies made at once", Type: adapters.Number, Default: 1, Min: 1, Max: 4,
				Help: "Each takes every core it can, below whatever a player needs."},
			{Key: "copies_kept_days", Label: "Keep copies for (days)", Type: adapters.Number, Default: 14, Min: 1, Max: 365,
				Help: "After a copy was last asked for. A device that has one keeps it however long this is."},
			{Key: "copies_room_gb", Label: "Room for copies (GB)", Type: adapters.Number, Default: 100, Min: 0, Max: 100000,
				Help: "The copies asked for least lately go first. 0 for no limit but the disk's, of which 10 GB are always left free."},
		},
		Test: func(ctx context.Context, s adapters.Settings) adapters.Result {
			d := Detect(ctx, FromSettings(s))
			return adapters.Result{OK: d.FFprobe != "", Message: d.Summary(), Details: d}
		},
	}
}
