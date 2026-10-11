package hls

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"foyer/internal/media"
)

// loopback serves a file as Foyer's loopback does: by ranges, to ffmpeg.
type loopback struct {
	server  *httptest.Server
	revoked atomic.Int32
}

func (l *loopback) Grant(string, time.Duration) (string, func(), error) {
	return l.server.URL + "/g/film.mkv", func() { l.revoked.Add(1) }, nil
}
func (l *loopback) Loopback() string { return strings.TrimPrefix(l.server.URL, "http://") }

// playings stand in for the registry: every request allowed, its speed kept.
type playings struct {
	mu    sync.Mutex
	speed float64
}

func (p *playings) Open(r *http.Request, _, _, _, _ string, _ time.Time) (context.Context, func(), bool) {
	return r.Context(), func() {}, true
}
func (p *playings) Sent(string, int) {}
func (p *playings) Speed(_ string, speed float64) {
	p.mu.Lock()
	p.speed = speed
	p.mu.Unlock()
}

// film makes ninety seconds of a Matroska film: H.264 with B-frames,
// keyframes two seconds apart, AC-3 5.1 in Japanese and AAC stereo in
// English, and English subtitles at 1 s, 60 s and 85 s.
func film(t *testing.T, ffmpeg string) string {
	t.Helper()
	dir := t.TempDir()
	srt := filepath.Join(dir, "subs.srt")
	if err := os.WriteFile(srt, []byte("1\n00:00:01,000 --> 00:00:04,000\nHello there\n\n2\n00:01:00,000 --> 00:01:03,500\nA minute in\n\n3\n00:01:25,000 --> 00:01:28,000\nNear the end\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "film.mkv")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=24000/1001",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=660:sample_rate=48000",
		"-i", srt, "-t", "90",
		"-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3:s",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "48", "-keyint_min", "48", "-sc_threshold", "0", "-bf", "2", "-pix_fmt", "yuv420p",
		"-c:a:0", "ac3", "-ac:a:0", "6", "-c:a:1", "aac", "-ac:a:1", "2", "-c:s", "srt",
		"-metadata:s:a:0", "language=jpn", "-metadata:s:a:1", "language=eng", "-metadata:s:s:0", "language=eng", out)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, output)
	}
	return out
}

func get(t *testing.T, m *Manager, ticket Ticket, name string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/x/"+name, nil)
	if err := m.serve(w, r, ticket, name); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return w
}

// tfdt is a segment's first fragment's decode time, in its track's scale.
func tfdt(t *testing.T, segment []byte) uint64 {
	t.Helper()
	var at uint64
	found := false
	boxes(segment, func(kind string, b []byte) {
		if kind != "moof" || found {
			return
		}
		boxes(b, func(kind string, b []byte) {
			if kind != "traf" {
				return
			}
			boxes(b, func(kind string, b []byte) {
				if kind == "tfdt" && !found {
					found = true
					if b[0] == 1 {
						at = binary.BigEndian.Uint64(b[4:12])
					} else {
						at = uint64(binary.BigEndian.Uint32(b[4:8]))
					}
				}
			})
		})
	})
	if !found {
		t.Fatal("a segment without a tfdt")
	}
	return at
}

// timescale is a rendition's, from its initialization.
func timescale(t *testing.T, init []byte) uint64 {
	t.Helper()
	var scale uint64
	var walk func(b []byte)
	walk = func(b []byte) {
		boxes(b, func(kind string, b []byte) {
			switch kind {
			case "moov", "trak", "mdia":
				walk(b)
			case "mdhd":
				if b[0] == 1 {
					scale = uint64(binary.BigEndian.Uint32(b[20:24]))
				} else {
					scale = uint64(binary.BigEndian.Uint32(b[12:16]))
				}
			}
		})
	}
	walk(init)
	if scale == 0 {
		t.Fatal("an initialization without a timescale")
	}
	return scale
}

func TestAFileIsRemuxedToHLSAtItsOwnKeyframes(t *testing.T) {
	ffmpegPath, _ := tools(t)
	file := film(t, ffmpegPath)
	served := &loopback{server: httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, file)
	}))}
	t.Cleanup(served.server.Close)
	read, _, size := reader(t, file)
	idx, err := ReadIndex(context.Background(), read, "film.mkv", size)
	if err != nil {
		t.Fatal(err)
	}
	version := media.Version{
		Container: "mkv", DurationMs: idx.DurationMs, Bitrate: 2_000_000,
		Video:     &media.Video{Codec: "h264", Width: 640, Height: 360},
		Audio:     []media.Audio{{Codec: "ac3", Channels: 6, Language: "ja", Default: true}, {Codec: "aac", Channels: 2, Language: "en"}},
		Subtitles: []media.Subtitle{{Format: "srt", Language: "en"}},
	}
	ios := Profile{Protocols: []string{"progressive", "hls"}, Containers: []string{"mp4"}, VideoCodecs: []string{"h264", "hevc"},
		AudioCodecs: []string{"aac", "ac3", "eac3"}, SubtitleFormats: []string{"vtt"}, MaxHeight: 2160}
	decision := Decide(version, ios, &idx)
	if decision.Mode != Remux || len(decision.Plan.Audio) != 2 || !decision.Plan.Audio[0].Copy || decision.Plan.Audio[1].Codec != "ac3" {
		t.Fatalf("the decision %+v", decision)
	}
	key := []byte("a test key")
	plays := &playings{}
	root := t.TempDir()
	m := NewManager(Options{
		Root: filepath.Join(root, "hls"),
		Sign: func(b []byte) ([]byte, error) {
			mac := hmac.New(sha256.New, key)
			mac.Write(b)
			return mac.Sum(nil), nil
		},
		Tool: func() (string, bool) { return ffmpegPath, true }, Grants: served,
		Index:    func(context.Context, string) (Index, error) { return idx, nil },
		Version:  func(string) (media.Version, error) { return version, nil },
		Playings: plays,
	})
	t.Cleanup(m.Close)
	ticket := Ticket{File: "f1", User: "u1", Session: "s1", Expires: time.Now().Add(time.Hour).Unix(), Plan: decision.Plan}

	// The address is the ticket, signed, in the path.
	address, err := m.Address(ticket)
	if err != nil || !strings.HasPrefix(address, Prefix) || !strings.HasSuffix(address, "/main.m3u8") {
		t.Fatalf("address %q %v", address, err)
	}
	sealed := strings.TrimSuffix(strings.TrimPrefix(address, Prefix), "/main.m3u8")
	if back, ok := openTicket(sealed, m.sign, time.Now()); !ok || back.Session != "s1" || len(back.Plan.Audio) != 2 {
		t.Fatalf("the ticket read back: %+v %v", back, ok)
	}
	if _, ok := openTicket(sealed[:len(sealed)-2]+"AA", m.sign, time.Now()); ok {
		t.Error("a forged ticket was taken")
	}
	if _, ok := openTicket(sealed, m.sign, time.Now().Add(2*time.Hour)); ok {
		t.Error("an expired ticket was taken")
	}

	master := get(t, m, ticket, "main.m3u8").Body.String()
	for _, want := range []string{`CODECS="avc1.`, `,ac-3"`, `NAME="ja",LANGUAGE="ja",CHANNELS="6",DEFAULT=YES`, `NAME="en",LANGUAGE="en",CHANNELS="2",DEFAULT=NO`,
		`TYPE=SUBTITLES`, `RESOLUTION=640x360`, `VIDEO-RANGE=SDR`, `AUDIO="audio"`, `SUBTITLES="subs"`} {
		if !strings.Contains(master, want) {
			t.Errorf("the master playlist lacks %s:\n%s", want, master)
		}
	}
	segments := Segments(idx)
	if len(segments) < 14 || segments[1].StartMs < 6000 || segments[1].StartMs > 6100 {
		t.Fatalf("segments %v", segments)
	}
	if body := get(t, m, ticket, "v.m3u8").Body.String(); !strings.Contains(body, `#EXT-X-MAP:URI="v.mp4"`) || strings.Count(body, ".m4s") != len(segments) {
		t.Errorf("the picture's playlist:\n%s", body)
	}

	init := get(t, m, ticket, "v.mp4").Body.Bytes()
	if string(init[4:8]) != "ftyp" || !bytes.Contains(init, []byte("moov")) || bytes.Contains(init, []byte("moof")) {
		t.Fatalf("the initialization: %q", init[:16])
	}
	scale := timescale(t, init)
	for n := range 3 {
		segment := get(t, m, ticket, "v"+itoa(n)+".m4s").Body.Bytes()
		if string(segment[4:8]) != "moof" {
			t.Fatalf("segment %d begins %q", n, segment[4:8])
		}
		// Every segment on the file's own clock, from its own keyframe.
		if got := int64(tfdt(t, segment) * 1000 / scale); got != segments[n].StartMs {
			t.Errorf("segment %d starts at %d ms, the playlist at %d", n, got, segments[n].StartMs)
		}
	}
	audioInit := get(t, m, ticket, "a1.mp4").Body.Bytes()
	audio := get(t, m, ticket, "a1-2.m4s").Body.Bytes()
	if at := int64(tfdt(t, audio) * 1000 / timescale(t, audioInit)); at < segments[2].StartMs-100 || at > segments[2].StartMs+100 {
		t.Errorf("the English sound's segment 2 starts at %d ms, the playlist's at %d", at, segments[2].StartMs)
	}
	if vtt := get(t, m, ticket, "s0-0.vtt").Body.String(); !strings.Contains(vtt, "00:00:01.000 --> 00:00:04.000\nHello there") || !strings.HasPrefix(vtt, "WEBVTT\n") {
		t.Errorf("the first subtitles:\n%s", vtt)
	}

	// Far ahead of the player, the run waits, and its read of the share with it.
	j := m.jobs["s1"]
	waitUntil(t, "the run paused ahead of the player", func() bool {
		j.mu.Lock()
		defer j.mu.Unlock()
		return j.run.paused
	})
	j.mu.Lock()
	if ahead := j.run.produced - j.asked; ahead > aheadMax || ahead < aheadMax-1 {
		t.Errorf("paused %d segments ahead", ahead)
	}
	j.mu.Unlock()

	// A seek far ahead starts again there, on the same clock.
	last := len(segments) - 2
	segment := get(t, m, ticket, "v"+itoa(last)+".m4s").Body.Bytes()
	if got := int64(tfdt(t, segment) * 1000 / scale); got != segments[last].StartMs {
		t.Errorf("after the seek, segment %d starts at %d ms, the playlist at %d", last, got, segments[last].StartMs)
	}
	j.mu.Lock()
	runs := j.runs
	j.mu.Unlock()
	if runs != 2 {
		t.Errorf("%d runs, one each side of the seek", runs)
	}
	minute := SegmentAt(segments, 60_000)
	if vtt := get(t, m, ticket, "s0-"+itoa(minute)+".vtt").Body.String(); minute < last-4 && !strings.Contains(vtt, "A minute in") {
		t.Logf("the minute's subtitles, which no run went through: %q", vtt)
	}
	near := SegmentAt(segments, 85_000)
	if vtt := get(t, m, ticket, "s0-"+itoa(near)+".vtt").Body.String(); !strings.Contains(vtt, "Near the end") {
		t.Errorf("the subtitles near the end:\n%s", vtt)
	}
	plays.mu.Lock()
	if plays.speed <= 0 {
		t.Error("ffmpeg's speed was never told")
	}
	plays.mu.Unlock()

	// Its playing ends: the run stops, the segments go, the loopback closes.
	m.End("s1")
	if _, err := os.Stat(filepath.Join(root, "hls", "s1")); !os.IsNotExist(err) {
		t.Errorf("the segments stayed: %v", err)
	}
	if served.revoked.Load() != 1 {
		t.Errorf("the loopback grant was revoked %d times", served.revoked.Load())
	}
	if m.Running() != 0 {
		t.Error("ffmpeg ran on")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
