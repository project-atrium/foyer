// Package hls remuxes a file for a player that cannot open it as it is —
// AVPlayer, a browser — into HLS: fragmented MP4 cut at the file's own
// keyframes, its picture copied, its sound copied or turned into one the
// player plays, its text subtitles as WebVTT.
//
// A playlist names every segment before one exists, so where the file's
// keyframes lie is read first, from the file's own index — Matroska's Cues,
// an MP4's sync samples — with a few ranged reads, never by reading the file
// through: behind Zurg, every byte read is Real-Debrid's.
package hls

import (
	"context"
	"errors"
	"path"
	"slices"
	"strings"
)

// IndexVersion is how an index is kept: raise it when what is kept changes,
// and every file's is read again.
const IndexVersion = 1

var (
	// ErrNoIndex is a file that keeps no index of its keyframes, or one this
	// package cannot read: it plays as it is, in a player that opens it.
	ErrNoIndex = errors.New("the file keeps no index of its keyframes")
	// errShort is a file that ends where its own structure says it goes on.
	errShort = errors.New("the file ends early")
)

// RangeReader reads length bytes at offset: fewer only where the file ends.
type RangeReader func(ctx context.Context, offset, length int64) ([]byte, error)

// Index is where a file's keyframes lie, and what a playlist of it needs to
// say of its picture.
type Index struct {
	Version int `json:"version"`
	// Keyframes are the picture's keyframes' presentation times, in
	// milliseconds, in order.
	Keyframes  []int64 `json:"keyframes"`
	DurationMs int64   `json:"durationMs"`
	Video      Video   `json:"video"`
}

// Video is the picture as a master playlist names it.
type Video struct {
	// Codec is its RFC 6381 name — `avc1.640028`, `hvc1.2.4.L150.90` — from
	// the stream's own configuration, as the segments will carry it.
	Codec     string  `json:"codec"`
	Width     int     `json:"width,omitzero"`
	Height    int     `json:"height,omitzero"`
	FrameRate float64 `json:"frameRate,omitzero"`
	// DolbyVision is the Dolby Vision profile the stream carries, 0 for none.
	DolbyVision int `json:"dolbyVision,omitzero"`
}

// ReadIndex reads a file's index: Matroska's Cues, or an MP4's sync
// samples. size is the file's, name says which it is.
func ReadIndex(ctx context.Context, read RangeReader, name string, size int64) (Index, error) {
	var (
		idx Index
		err error
	)
	switch strings.ToLower(path.Ext(name)) {
	case ".mkv", ".webm", ".mk3d":
		idx, err = readMatroska(ctx, read)
	case ".mp4", ".m4v", ".mov":
		idx, err = readMP4(ctx, read, size)
	default:
		return Index{}, ErrNoIndex
	}
	if err != nil {
		return Index{}, err
	}
	slices.Sort(idx.Keyframes)
	idx.Keyframes = slices.Compact(idx.Keyframes)
	if len(idx.Keyframes) == 0 || idx.Video.Codec == "" {
		return Index{}, ErrNoIndex
	}
	if last := idx.Keyframes[len(idx.Keyframes)-1]; idx.DurationMs <= last {
		idx.DurationMs = last + 1000
	}
	idx.Version = IndexVersion
	return idx, nil
}

// readAll reads a whole span, failing where the file ends before it does.
func readAll(ctx context.Context, read RangeReader, offset, length int64) ([]byte, error) {
	b, err := read(ctx, offset, length)
	if err != nil {
		return nil, err
	}
	if int64(len(b)) < length {
		return nil, errShort
	}
	return b, nil
}
