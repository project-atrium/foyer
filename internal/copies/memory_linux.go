//go:build linux

package copies

import "os"

// left is the memory a copy may still take here: what the kernel says is
// available, or less where Foyer's own cgroup — a container's limit, a
// service's MemoryMax — allows less. -1 where nothing says.
func left() int64 {
	n := int64(-1)
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		n = memAvailable(string(b))
	}
	if b, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		if path, ok := unifiedPath(string(b)); ok {
			if room := cgroupRoom("/sys/fs/cgroup", path); room >= 0 && (n < 0 || room < n) {
				n = room
			}
		}
	}
	return n
}
