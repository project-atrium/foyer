package copies

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"foyer/internal/media"
)

// SVT-AV1 takes its own parallelism where the memory left holds it beside
// the rest of the server, and its least where it would not — as on a VM of
// 2 GB, where it was stopped making a 1080p copy at 1.4 GB.
func TestACopyIsMadeLeanWhereTheServerHasLittleMemoryLeft(t *testing.T) {
	const mib = 1 << 20
	hd := VideoPlan{Codec: "av1", Width: 1920, Height: 1080, BitDepth: 10}
	film := &media.Video{Width: 1920, Height: 1080}
	for _, c := range []struct {
		name     string
		plan     VideoPlan
		left     int64
		lean     bool
		min, max int64
	}{
		{"nothing says", hd, -1, false, 1600 * mib, 1900 * mib},
		{"a 2 GB VM", hd, 1413 * mib, true, 900 * mib, 1100 * mib},
		{"a 4 GB VM", hd, 3300 * mib, false, 1600 * mib, 1900 * mib},
		{"720p on a 3 GB VM", VideoPlan{Codec: "av1", Width: 1280, Height: 720}, 2300 * mib, false, 1000 * mib, 1400 * mib},
		{"2160p on a 4 GB VM", VideoPlan{Codec: "av1", Width: 3840, Height: 2160}, 3300 * mib, true, 2700 * mib, 3300 * mib},
		{"x265 is never held", VideoPlan{Codec: "hevc", Width: 1920, Height: 1080}, 100 * mib, false, 0, 0},
	} {
		lean, need := parallelism(c.plan, film, c.left)
		if lean != c.lean || need < c.min || need > c.max {
			t.Errorf("%s: lean %v, needs %d MiB", c.name, lean, need/mib)
		}
	}
}

// A copy the system stopped says it most likely ran short of memory, and
// what it takes, where that is known.
func TestACopyTheSystemStoppedSaysItRanShortOfMemory(t *testing.T) {
	for need, want := range map[[2]int64]string{
		{1 << 30, 900 << 20}: "the system stopped ffmpeg, most likely for want of memory: a copy like this takes about 1.0 GB, and the server had 0.9 GB free",
		{1 << 30, -1}:        "the system stopped ffmpeg, most likely for want of memory: a copy like this takes about 1.0 GB",
		{0, 2 << 30}:         "the system stopped ffmpeg, most likely for want of memory: the server had 2.0 GB free",
		{0, -1}:              "the system stopped ffmpeg, most likely for want of memory",
	} {
		if said := starved(need[0], need[1]); said != want {
			t.Errorf("said %q", said)
		}
	}
}

// What is left is the kernel's MemAvailable, or the tightest cgroup limit
// above Foyer's own less what it holds — its page cache, which the kernel
// gives back first, left out.
func TestTheMemoryLeftIsTheKernelsOrTheTightestCgroups(t *testing.T) {
	if got := memAvailable("MemTotal:        2005052 kB\nMemFree:          1310000 kB\nMemAvailable:    1447000 kB\n"); got != 1447000<<10 {
		t.Errorf("MemAvailable read as %d", got)
	}
	if got := memAvailable("MemTotal: 1 kB\n"); got != -1 {
		t.Errorf("no MemAvailable read as %d", got)
	}
	if path, ok := unifiedPath("12:memory:/docker/abc\n0::/system.slice/foyer.service\n"); !ok || path != "/system.slice/foyer.service" {
		t.Errorf("the path %q %v", path, ok)
	}
	if _, ok := unifiedPath("12:memory:/docker/abc\n"); ok {
		t.Error("a cgroup v1 line read as v2")
	}

	root := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// No limit anywhere: the root has no memory.max, a slice says max.
	write("system.slice/memory.max", "max\n")
	if room := cgroupRoom(root, "/system.slice/foyer.service"); room != -1 {
		t.Errorf("unlimited read as %d", room)
	}
	// A service's MemoryMax of 2 GiB, 1.5 GiB used of which 0.5 GiB cache.
	write("system.slice/foyer.service/memory.max", "2147483648\n")
	write("system.slice/foyer.service/memory.current", "1610612736\n")
	write("system.slice/foyer.service/memory.stat", "anon 1073741824\nfile 536870912\ninactive_file 536870912\n")
	if room := cgroupRoom(root, "/system.slice/foyer.service"); room != 1<<30 {
		t.Errorf("the service's room %d", room)
	}
	// A slice above it that allows less wins.
	write("system.slice/memory.max", "1610612736\n")
	write("system.slice/memory.current", "1342177280\n")
	if room := cgroupRoom(root, "/system.slice/foyer.service"); room != 256<<20 {
		t.Errorf("the slice's room %d", room)
	}
	// Inside a container of its own, the group is the root.
	container := t.TempDir()
	if err := os.WriteFile(filepath.Join(container, "memory.max"), []byte("1073741824"), 0o600); err != nil {
		t.Fatal(err)
	}
	if room := cgroupRoom(container, "/"); room != 1<<30 {
		t.Errorf("the container's room %d", room)
	}
	if !strings.HasSuffix(gb(3<<29), "1.5 GB") {
		t.Errorf("gb %q", gb(3<<29))
	}
}
