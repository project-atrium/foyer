//go:build unix

package copies

import (
	"os/exec"
	"syscall"
)

// free is what the disk holding dir has left, -1 where it cannot say.
func free(dir string) int64 {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(dir, &fs); err != nil {
		return -1
	}
	return int64(fs.Bavail) * int64(fs.Bsize)
}

// lower runs ffmpeg after whatever else wants the processor: a player's
// stream, a scan.
func lower(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Setpriority(syscall.PRIO_PROCESS, cmd.Process.Pid, 10)
	}
}
