package hls

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// cue is one subtitle, on the file's own clock.
type cue struct {
	startMs, endMs int64
	text           string
}

// readCues reads the whole cue blocks in a WebVTT file ffmpeg is still
// writing, and how far they reach. ffmpeg writes a cue in one go — a blank
// line, its times, its text, a line's end — so what ends at a line's end is
// whole, and the next cue's blank line comes with it.
func readCues(b []byte) ([]cue, int) {
	var cues []cue
	read := 0
	for read < len(b) {
		end := bytes.Index(b[read:], []byte("\n\n"))
		next := read + end + 2
		if end < 0 {
			if b[len(b)-1] != '\n' {
				return cues, read
			}
			end, next = len(b)-read, len(b)
		}
		block := strings.TrimSpace(string(b[read : read+end]))
		read = next
		lines := strings.SplitN(block, "\n", 2)
		if len(lines) < 2 {
			continue
		}
		// A cue's identifier, where ffmpeg writes one, comes before its times.
		timing, text := lines[0], lines[1]
		if !strings.Contains(timing, "-->") {
			more := strings.SplitN(text, "\n", 2)
			if len(more) < 2 {
				continue
			}
			timing, text = more[0], more[1]
		}
		from, to, ok := strings.Cut(timing, "-->")
		if !ok {
			continue
		}
		start, ok1 := vttTime(strings.TrimSpace(from))
		fields := strings.Fields(to)
		if len(fields) == 0 {
			continue
		}
		stop, ok2 := vttTime(fields[0])
		if ok1 && ok2 && text != "" {
			cues = append(cues, cue{startMs: start, endMs: stop, text: text})
		}
	}
	return cues, read
}

// vttTime reads 01:02:03.456 or 02:03.456.
func vttTime(s string) (int64, bool) {
	whole, frac, ok := strings.Cut(s, ".")
	if !ok || len(frac) != 3 {
		return 0, false
	}
	ms, err := strconv.Atoi(frac)
	if err != nil {
		return 0, false
	}
	var total int64
	for _, part := range strings.Split(whole, ":") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return 0, false
		}
		total = total*60 + int64(n)
	}
	return total*1000 + int64(ms), true
}

func vttStamp(ms int64) string {
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3_600_000, ms/60_000%60, ms/1000%60, ms%1000)
}

// vttSegment is a segment's subtitles: the cues that begin in it, on the
// file's own clock — which is the remux's.
func vttSegment(cues []cue, s Segment) []byte {
	var b strings.Builder
	b.WriteString("WEBVTT\nX-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:0\n\n")
	for _, c := range cues {
		if c.startMs >= s.StartMs && c.startMs < s.EndMs {
			fmt.Fprintf(&b, "%s --> %s\n%s\n\n", vttStamp(c.startMs), vttStamp(c.endMs), c.text)
		}
	}
	return []byte(b.String())
}
