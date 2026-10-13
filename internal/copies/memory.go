package copies

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"foyer/internal/media"
)

// What a copy in AV1 takes, measured with SVT-AV1 4.2 making one from a
// 1080p film, ffmpeg's decoding and the rest included: at the encoder's
// least parallelism about 0.6 GB for 720p, 1 GB for 1080p and 2.8 GB for
// 2160p; at its own choice, on eight cores, 0.4 to 0.6 GB more, and three to
// four times as fast. The file it makes is the same to the byte either way,
// so the choice is the server's alone, and names no copy.
const (
	leanBase = 256 << 20
	// leanPerPixel is the encoder's, a pixel of the copy; decodePerPixel the
	// decoder's, a pixel of the file.
	leanPerPixel   = 336
	decodePerPixel = 34
	// fullExtra is what the encoder's own parallelism takes more; spare what
	// the rest of the server is left beside a copy.
	fullExtra = 768 << 20
	spare     = 256 << 20
)

// parallelism says whether a copy is made at SVT-AV1's least parallelism —
// the same file, slower — and what it is thought to take: lean where the
// memory left (-1 where nothing says) would not hold the encoder's own
// choice beside the rest of the server. Only SVT-AV1 is held so: what the
// other encoders make changes with their threads.
func parallelism(v VideoPlan, src *media.Video, left int64) (lean bool, need int64) {
	if v.Codec != "av1" {
		return false, 0
	}
	need = leanBase + leanPerPixel*int64(v.Width)*int64(v.Height)
	if src != nil {
		need += decodePerPixel * int64(src.Width) * int64(src.Height)
	}
	if left >= 0 && left < need+fullExtra+spare {
		return true, need
	}
	return false, need + fullExtra
}

// starved is what a copy the system stopped says: most likely it ran short
// of memory — what it takes, where known, and what the server had.
func starved(need, free int64) string {
	said := "the system stopped ffmpeg, most likely for want of memory"
	switch {
	case need > 0 && free >= 0:
		said += fmt.Sprintf(": a copy like this takes about %s, and the server had %s free", gb(need), gb(free))
	case need > 0:
		said += fmt.Sprintf(": a copy like this takes about %s", gb(need))
	case free >= 0:
		said += fmt.Sprintf(": the server had %s free", gb(free))
	}
	return said
}

func gb(n int64) string {
	return strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64) + " GB"
}

// memAvailable is what /proc/meminfo says is available, in bytes: -1
// without it.
func memAvailable(meminfo string) int64 {
	for _, line := range strings.Split(meminfo, "\n") {
		if rest, ok := strings.CutPrefix(line, "MemAvailable:"); ok {
			if fields := strings.Fields(rest); len(fields) > 0 {
				if kb, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
					return kb << 10
				}
			}
		}
	}
	return -1
}

// unifiedPath is where a process sits in cgroup v2, from /proc/self/cgroup's
// "0::" line: "/" inside a container of its own.
func unifiedPath(cgroup string) (string, bool) {
	for _, line := range strings.Split(cgroup, "\n") {
		if path, ok := strings.CutPrefix(strings.TrimSpace(line), "0::"); ok && strings.HasPrefix(path, "/") {
			return path, true
		}
	}
	return "", false
}

// cgroupRoom is what the tightest limit on path, or a group above it, still
// allows under root: its memory.max less what it uses and could not give
// back — the page cache it can (inactive_file) left out, as the kernel would
// drop it first. -1 where no group is limited.
func cgroupRoom(root, path string) int64 {
	room := int64(-1)
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		dir := filepath.Join(root, p)
		if limit, ok := readBytes(filepath.Join(dir, "memory.max")); ok {
			used, _ := readBytes(filepath.Join(dir, "memory.current"))
			used -= statOf(filepath.Join(dir, "memory.stat"), "inactive_file")
			if r := max(limit-max(used, 0), 0); room < 0 || r < room {
				room = r
			}
		}
		if p == "/" || p == "." {
			return room
		}
	}
}

// readBytes is a cgroup file's one number: none for "max", which is no
// limit, or a file that is not there — the root's.
func readBytes(file string) (int64, bool) {
	b, err := os.ReadFile(file)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n, err == nil
}

// statOf is one key of memory.stat: 0 without it.
func statOf(file, key string) int64 {
	b, err := os.ReadFile(file)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if fields := strings.Fields(line); len(fields) == 2 && fields[0] == key {
			n, _ := strconv.ParseInt(fields[1], 10, 64)
			return n
		}
	}
	return 0
}
