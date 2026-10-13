//go:build !linux

package copies

// left says nothing outside Linux, where the image runs: the encoder
// chooses for itself.
func left() int64 { return -1 }
