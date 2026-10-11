//go:build !unix

package hls

// pause is not done where there is no SIGSTOP: a run goes on to the end, its
// segments pruned behind the player all the same.
func (r *run) pause() {}

func (r *run) resume() { r.paused = false }
