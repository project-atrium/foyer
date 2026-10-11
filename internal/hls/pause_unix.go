//go:build unix

package hls

import "syscall"

// pause holds ffmpeg where it is: a run far enough ahead of the player waits,
// and so does its read of the share.
func (r *run) pause() {
	if r.cmd.Process != nil && r.cmd.Process.Signal(syscall.SIGSTOP) == nil {
		r.paused = true
	}
}

// resume lets a run that waited go on.
func (r *run) resume() {
	if r.paused && r.cmd.Process != nil {
		_ = r.cmd.Process.Signal(syscall.SIGCONT)
	}
	r.paused = false
}
