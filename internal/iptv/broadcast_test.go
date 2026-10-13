package iptv

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestEveryViewerReadsAtItsOwnPaceAndOneFallingBehindIsDropped(t *testing.T) {
	b := newBroadcast(10)
	early := b.join(0)
	b.write([]byte("abcd"))
	b.write([]byte("efgh"))
	ctx := context.Background()
	if chunk, err := b.next(ctx, early); err != nil || string(chunk) != "abcd" {
		t.Fatalf("%q %v", chunk, err)
	}
	// A viewer joining now starts a little before the newest bytes.
	late := b.join(4)
	if chunk, _ := b.next(ctx, late); string(chunk) != "efgh" {
		t.Fatalf("late %q", chunk)
	}
	// Past what is held, the oldest goes: a viewer still there is behind.
	b.write([]byte("ijkl"))
	if _, err := b.next(ctx, early); !errors.Is(err, ErrBehind) {
		t.Fatalf("early %v", err)
	}
	// One waiting hears the next chunk as it comes, and the end after it.
	got := make(chan string, 2)
	go func() {
		for at := late + 2; ; at++ {
			chunk, err := b.next(ctx, at)
			if err != nil {
				got <- "end"
				return
			}
			got <- string(chunk)
		}
	}()
	time.Sleep(10 * time.Millisecond)
	b.write([]byte("mnop"))
	if g := <-got; g != "mnop" {
		t.Fatalf("waited for %q", g)
	}
	b.end(nil)
	if g := <-got; g != "end" {
		t.Fatalf("after the end %q", g)
	}
}

func TestAJoiningViewerStartsAtAPacket(t *testing.T) {
	packet := append([]byte{0x47}, bytes.Repeat([]byte{1}, 187)...)
	stream := append([]byte{9, 9, 9}, bytes.Repeat(packet, 4)...)
	if got := packets(stream); len(got) != 4*188 || got[0] != 0x47 {
		t.Fatalf("%d bytes from %x", len(got), got[0])
	}
	if got := packets([]byte{1, 2, 3}); len(got) != 3 {
		t.Fatal("a short chunk changed")
	}
}
