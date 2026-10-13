package iptv

import (
	"context"
	"errors"
	"sync"
)

// ErrBehind is a viewer that fell so far behind that what it had still to
// read is gone: a stalled player, dropped rather than let hold the others.
var ErrBehind = errors.New("the viewer fell behind the stream")

// broadcast is one upstream's bytes for every viewer of it: the last few
// seconds kept, each viewer reading at its own pace from where it joined,
// none waiting for another.
type broadcast struct {
	mu sync.Mutex
	// chunks are the bytes kept, oldest first; first is the number of the
	// oldest, counted from the stream's start.
	chunks [][]byte
	first  int64
	held   int
	most   int
	done   bool
	err    error
	// wake is closed, and made anew, whenever a chunk comes or the stream
	// ends.
	wake chan struct{}
}

func newBroadcast(most int) *broadcast {
	return &broadcast{most: most, wake: make(chan struct{})}
}

// write keeps a chunk, letting the oldest go past what is held.
func (b *broadcast) write(p []byte) {
	chunk := append([]byte(nil), p...)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		return
	}
	b.chunks = append(b.chunks, chunk)
	b.held += len(chunk)
	for b.held > b.most && len(b.chunks) > 1 {
		b.held -= len(b.chunks[0])
		b.chunks[0] = nil
		b.chunks = b.chunks[1:]
		b.first++
	}
	close(b.wake)
	b.wake = make(chan struct{})
}

// end is the stream's end, and why: every viewer reads what is left, then
// that.
func (b *broadcast) end(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		return
	}
	b.done, b.err = true, err
	close(b.wake)
	b.wake = make(chan struct{})
}

// ended says whether the stream has ended.
func (b *broadcast) ended() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.done
}

// join is where a new viewer starts reading: lead bytes back from the
// newest, so a player has something to start with at once, and finds a
// picture to begin on soon after.
func (b *broadcast) join(lead int) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	at, back := b.first+int64(len(b.chunks)), 0
	for i := len(b.chunks) - 1; i >= 0 && back < lead; i-- {
		back += len(b.chunks[i])
		at--
	}
	return at
}

// next is the chunk numbered at, waiting for it to come; the stream's end
// as io.EOF or its error once every chunk is read, and ErrBehind where it
// is gone already.
func (b *broadcast) next(ctx context.Context, at int64) ([]byte, error) {
	for {
		b.mu.Lock()
		switch {
		case at < b.first:
			b.mu.Unlock()
			return nil, ErrBehind
		case at < b.first+int64(len(b.chunks)):
			chunk := b.chunks[at-b.first]
			b.mu.Unlock()
			return chunk, nil
		case b.done:
			err := b.err
			b.mu.Unlock()
			if err == nil {
				err = errEnded
			}
			return nil, err
		}
		wake := b.wake
		b.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// errEnded is a stream the portal ended of itself.
var errEnded = errors.New("the stream ended")
