package hls

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
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

// paced writes at about 4 MB/s.
type paced struct{ http.ResponseWriter }

func (p paced) Write(b []byte) (int, error) {
	n, err := p.ResponseWriter.Write(b)
	time.Sleep(time.Duration(n) * time.Second / (4 << 20))
	return n, err
}

// film makes a Matroska film of so many seconds: H.264 with B-frames,
// keyframes two seconds apart, AC-3 5.1 in Japanese and AAC stereo in
// English, and English subtitles at 1 s, 60 s and ten seconds from the end.
func film(t *testing.T, ffmpeg string, length int) string {
	t.Helper()
	dir := t.TempDir()
	srt := filepath.Join(dir, "subs.srt")
	end := vttStamp(int64(length-10) * 1000)
	cues := "1\n00:00:01,000 --> 00:00:04,000\nHello there\n\n2\n00:01:00,000 --> 00:01:03,500\nA minute in\n\n3\n" +
		strings.ReplaceAll(end, ".", ",") + " --> " + strings.ReplaceAll(vttStamp(int64(length-7)*1000), ".", ",") + "\nNear the end\n"
	if err := os.WriteFile(srt, []byte(cues), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "film.mkv")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=24000/1001",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=660:sample_rate=48000",
		"-i", srt, "-t", strconv.Itoa(length),
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

// remuxed is a Manager over a film served as the loopback serves it — at a
// share's pace, about 4 MB/s, where paced — and a ticket for the AVPlayer
// it decides for: the film's subtitles in the plan, where with.
type remuxed struct {
	m        *Manager
	ticket   Ticket
	idx      Index
	segments []Segment
	served   *loopback
	plays    *playings
	root     string
}

func remuxOf(t *testing.T, ffmpegPath string, length int, slow, withSubtitles bool) remuxed {
	t.Helper()
	file := film(t, ffmpegPath, length)
	served := &loopback{server: httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slow {
			w = paced{w}
		}
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
		Video: &media.Video{Codec: "h264", Width: 640, Height: 360},
		Audio: []media.Audio{{Codec: "ac3", Channels: 6, Language: "ja", Default: true}, {Codec: "aac", Channels: 2, Language: "en"}},
	}
	if withSubtitles {
		version.Subtitles = []media.Subtitle{{Format: "srt", Language: "en"}}
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
	return remuxed{m: m, ticket: ticket, idx: idx, segments: Segments(idx), served: served, plays: plays, root: root}
}

func TestAFileIsRemuxedToHLSAtItsOwnKeyframes(t *testing.T) {
	ffmpegPath, _ := tools(t)
	x := remuxOf(t, ffmpegPath, 90, false, true)
	m, ticket, segments := x.m, x.ticket, x.segments

	// The address is the ticket, signed, in the path.
	address, err := m.Address(ticket)
	if err != nil || !strings.HasPrefix(address, Prefix) || !strings.HasSuffix(address, "/main.m3u8") {
		t.Fatalf("address %q %v", address, err)
	}
	sealed := strings.TrimSuffix(strings.TrimPrefix(address, Prefix), "/main.m3u8")
	if back, ok := openTicket(sealed, m.sign, time.Now()); !ok || back.Session != "s1" || len(back.Plan.Audio) != 2 {
		t.Fatalf("the ticket read back: %+v %v", back, ok)
	}
	// Forged by a bit of the signature itself: new characters at the end
	// could spell the same bytes, or differ in base64's unused bits alone.
	encoded, sig, _ := strings.Cut(sealed, ".")
	mac, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || len(mac) == 0 {
		t.Fatalf("the ticket's signature %q: %v", sig, err)
	}
	mac[0] ^= 1
	forged := encoded + "." + base64.RawURLEncoding.EncodeToString(mac)
	if forged == sealed {
		t.Fatal("the forgery is the ticket")
	}
	if _, ok := openTicket(forged, m.sign, time.Now()); ok {
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
	if vtt := get(t, m, ticket, "s0-"+itoa(SegmentAt(segments, 80_000))+".vtt").Body.String(); !strings.Contains(vtt, "Near the end") {
		t.Errorf("the subtitles near the end:\n%s", vtt)
	}
	// ffmpeg says its speed every half second, and a reader of its own
	// passes it on: a busy machine may take a moment.
	told := false
	for wait := time.Now().Add(5 * time.Second); !told && time.Now().Before(wait); time.Sleep(20 * time.Millisecond) {
		x.plays.mu.Lock()
		told = x.plays.speed > 0
		x.plays.mu.Unlock()
	}
	if !told {
		t.Error("ffmpeg's speed was never told")
	}

	// Its playing ends: the run stops, the segments go, the loopback closes.
	m.End("s1")
	if _, err := os.Stat(filepath.Join(x.root, "hls", "s1")); !os.IsNotExist(err) {
		t.Errorf("the segments stayed: %v", err)
	}
	if x.served.revoked.Load() != 1 {
		t.Errorf("the loopback grant was revoked %d times", x.served.revoked.Load())
	}
	if m.Running() != 0 {
		t.Error("ffmpeg ran on")
	}
}

// At a share's pace, a run waits once its slowest rendition — the sound made
// anew — is far enough ahead of the player, and a seek past what it made
// starts a new run there, on the same clock. Without subtitles: a subtitle
// output in the same ffmpeg holds back every sound made anew until the
// file's end, when the share is slow (AGENTS.md), which is still to mend.
func TestARemuxWaitsForThePlayerAndStartsAgainOnASeek(t *testing.T) {
	ffmpegPath, _ := tools(t)
	x := remuxOf(t, ffmpegPath, 300, true, false)
	m, ticket, segments := x.m, x.ticket, x.segments
	if len(segments) < 49 {
		t.Fatalf("segments %v", segments)
	}
	init := get(t, m, ticket, "v.mp4").Body.Bytes()
	scale := timescale(t, init)
	for _, name := range []string{"v0.m4s", "v1.m4s", "v2.m4s", "a0-2.m4s", "a1-2.m4s"} {
		if segment := get(t, m, ticket, name).Body.Bytes(); string(segment[4:8]) != "moof" {
			t.Fatalf("%s begins %q", name, segment[4:8])
		}
	}

	j := m.jobs["s1"]
	waitUntil(t, "the run paused ahead of the player", func() bool {
		j.mu.Lock()
		defer j.mu.Unlock()
		return j.run.paused
	})
	j.mu.Lock()
	ahead, made := j.run.slowest(j.kinds()...)-j.asked, j.run.madeOf("v")
	j.mu.Unlock()
	// What ffmpeg finished is read a look late: a few more may come.
	if ahead < aheadMax || ahead > aheadMax+4 {
		t.Errorf("paused %d segments ahead", ahead)
	}
	last := len(segments) - 2
	if last <= made+restartAhead {
		t.Fatalf("the run made %d of %d segments: no seek past it", made, len(segments))
	}
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
	if audio := get(t, m, ticket, "a1-"+itoa(last)+".m4s").Body.Bytes(); string(audio[4:8]) != "moof" {
		t.Errorf("the sound after the seek begins %q", audio[4:8])
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
