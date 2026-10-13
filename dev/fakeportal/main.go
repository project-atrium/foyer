// Command fakeportal is a pretend Stalker portal on this machine, to run
// Foyer against without a subscription: a few channels in a few groups, a
// guide around now, and each channel's stream made by ffmpeg as it is asked
// for — a test picture and a tone, as MPEG-TS. Like a real subscription it
// plays as many streams at once as it allows, and a new one past that ends
// the oldest; /debug/streams says what is open.
//
//	go run ./dev/fakeportal [-addr 127.0.0.1:9988] [-link host:port] [-mac 00:1A:79:00:00:01] [-streams 1] [-ffmpeg ffmpeg]
//
// Set it up on Foyer's Adapters page as a Stalker portal at
// http://<addr>/c/ with that MAC address.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type channel struct {
	id, name, group, xmltv string
	number                 int
	// How its picture and tone differ from the others'.
	hue, tone int
}

var groups = [][2]string{{"*", "All"}, {"1", "News"}, {"2", "Sports"}, {"3", "DE ✨ DEUTSCHLAND"}}

var channels = []channel{
	{id: "101", name: "Morning Wire", group: "1", number: 1, hue: 0, tone: 440},
	{id: "102", name: "Stadium One", group: "2", number: 2, hue: 60, tone: 523},
	{id: "103", name: "Late Show", group: "1", number: 3, hue: 120, tone: 587},
	{id: "104", name: "Track and Field", group: "2", number: 4, hue: 180, tone: 659},
	{id: "534", name: "Das Erste", group: "3", number: 5, xmltv: "ard.de", hue: 240, tone: 698},
	{id: "18390:1", name: "A channel whose id holds a colon", group: "3", number: 6, hue: 300, tone: 784},
}

type portal struct {
	addr, mac, ffmpeg string
	allowed           int

	mu      sync.Mutex
	tokens  map[string]bool
	open    []*stream
	asked   map[string]int
	nextNum int
}

type stream struct {
	channel string
	n       int
	cancel  context.CancelFunc
}

func main() {
	addr := flag.String("addr", "127.0.0.1:9988", "where to listen")
	link := flag.String("link", "", "the address its stream links name, where Foyer reaches it by another than -addr — in a container")
	mac := flag.String("mac", "00:1A:79:00:00:01", "the one MAC address it takes")
	allowed := flag.Int("streams", 1, "how many streams it plays at once")
	ffmpeg := flag.String("ffmpeg", "ffmpeg", "ffmpeg's path, or its name on the PATH")
	flag.Parse()
	named := *addr
	if *link != "" {
		named = *link
	}
	p := &portal{addr: named, mac: strings.ToUpper(*mac), ffmpeg: *ffmpeg, allowed: max(1, *allowed), tokens: map[string]bool{}, asked: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/stalker_portal/server/load.php", p.load)
	mux.HandleFunc("/live/", p.live)
	mux.HandleFunc("/debug/streams", p.debug)
	log.Printf("fakeportal at http://%s/c/ — MAC %s, %d stream(s) at once", *addr, p.mac, p.allowed)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func answer(w http.ResponseWriter, js any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"js": js})
}

func random() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (p *portal) load(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	typ, action := q.Get("type"), q.Get("action")
	if typ == "stb" && action == "handshake" {
		token := random()
		p.mu.Lock()
		// One token per box: a handshake ends the one before.
		p.tokens = map[string]bool{token: true}
		p.mu.Unlock()
		answer(w, map[string]any{"token": token})
		return
	}
	p.mu.Lock()
	known := p.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	p.mu.Unlock()
	if !known {
		_, _ = io.WriteString(w, "Authorization failed.")
		return
	}
	switch {
	case typ == "stb" && action == "get_profile":
		status := 0
		if !strings.Contains(r.Header.Get("Cookie"), "mac="+p.mac) {
			status = 1
		}
		answer(w, map[string]any{"id": "1", "name": "fakeportal", "status": status})
	case typ == "itv" && action == "get_genres":
		var out []any
		for _, g := range groups {
			out = append(out, map[string]any{"id": g[0], "title": g[1]})
		}
		answer(w, out)
	case typ == "itv" && action == "get_all_channels":
		answer(w, map[string]any{"total_items": len(channels), "data": rows(channels)})
	case typ == "itv" && action == "get_ordered_list":
		page, _ := strconv.Atoi(q.Get("p"))
		page = max(1, page)
		var matching []channel
		for _, c := range channels {
			if g := q.Get("genre"); g == "" || g == "*" || g == c.group {
				matching = append(matching, c)
			}
		}
		from, to := min(len(matching), (page-1)*14), min(len(matching), page*14)
		answer(w, map[string]any{"total_items": len(matching), "max_page_items": 14, "cur_page": page, "data": rows(matching[from:to])})
	case typ == "itv" && action == "get_epg_info":
		hours, _ := strconv.Atoi(q.Get("period"))
		answer(w, map[string]any{"data": guide(max(1, hours))})
	case action == "create_link":
		// The channel's own id, from its plain command: `ffmpeg http://localhost/ch/101_`.
		_, link, _ := strings.Cut(strings.TrimSpace(q.Get("cmd")), " ")
		id := strings.TrimSuffix(strings.TrimPrefix(link, "http://localhost/ch/"), "_")
		if !slices.ContainsFunc(channels, func(c channel) bool { return c.id == id }) {
			answer(w, map[string]any{"cmd": "", "error": "nothing_to_play"})
			return
		}
		answer(w, map[string]any{"id": id, "cmd": fmt.Sprintf("ffmpeg http://%s/live/%s.ts?play_token=%s", p.addr, id, random()), "error": ""})
	default:
		http.NotFound(w, r)
	}
}

func rows(list []channel) []any {
	out := []any{}
	for _, c := range list {
		row := map[string]any{"id": c.id, "name": c.name, "number": strconv.Itoa(c.number), "cmd": "ffmpeg http://localhost/ch/" + c.id + "_", "tv_genre_id": c.group, "archive": 0}
		if c.xmltv != "" {
			row["xmltv_id"] = c.xmltv
		}
		out = append(out, row)
	}
	return out
}

// guide is half an hour a programme, from two hours ago, for every channel —
// stamped, as a portal on UTC stamps them.
func guide(hours int) map[string]any {
	start := time.Now().UTC().Truncate(30 * time.Minute).Add(-2 * time.Hour)
	out := map[string]any{}
	for _, c := range channels {
		var list []any
		for at := start; at.Before(start.Add(time.Duration(hours+2) * time.Hour)); at = at.Add(30 * time.Minute) {
			list = append(list, map[string]any{
				"ch_id": c.id, "name": fmt.Sprintf("%s at %s", c.name, at.Format("15:04")), "descr": "A programme of the pretend portal's.",
				"start_timestamp": at.Unix(), "stop_timestamp": at.Add(30 * time.Minute).Unix(), "time": at.Format("2006-01-02 15:04:05"),
			})
		}
		out[c.id] = list
	}
	return out
}

// live streams a channel as ffmpeg makes it, for as long as it is read —
// and, past what the subscription allows, ends the oldest stream for it.
func (p *portal) live(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/live/"), ".ts")
	i := slices.IndexFunc(channels, func(c channel) bool { return c.id == id })
	if i < 0 || r.URL.Query().Get("play_token") == "" {
		http.NotFound(w, r)
		return
	}
	c := channels[i]
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	p.mu.Lock()
	p.nextNum++
	s := &stream{channel: id, n: p.nextNum, cancel: cancel}
	p.open = append(p.open, s)
	p.asked[id]++
	for len(p.open) > p.allowed {
		oldest := p.open[0]
		p.open = p.open[1:]
		log.Printf("stream %s #%d ended: %d at once is the most", oldest.channel, oldest.n, p.allowed)
		oldest.cancel()
	}
	log.Printf("stream %s #%d opened, by %s (%d open)", id, s.n, r.UserAgent(), len(p.open))
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		if at := slices.Index(p.open, s); at >= 0 {
			p.open = slices.Delete(p.open, at, at+1)
		}
		log.Printf("stream %s #%d closed (%d open)", id, s.n, len(p.open))
		p.mu.Unlock()
	}()
	cmd := exec.CommandContext(ctx, p.ffmpeg, "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=25",
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=%d:sample_rate=48000", c.tone),
		"-vf", fmt.Sprintf("hue=h=%d", c.hue),
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-g", "50", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "96k", "-f", "mpegts", "pipe:1")
	out, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		http.Error(w, "ffmpeg would not start", http.StatusInternalServerError)
		return
	}
	defer func() { _ = cmd.Wait() }()
	w.Header().Set("Content-Type", "video/mp2t")
	buf := make([]byte, 32<<10)
	for {
		n, err := out.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
		if err != nil {
			return
		}
	}
}

func (p *portal) debug(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	open := map[string]int{}
	for _, s := range p.open {
		open[s.channel]++
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"open": open, "asked": p.asked})
}
