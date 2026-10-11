package hls

import (
	"context"
	"encoding/binary"
	"math"
)

const (
	// maxMoov bounds the movie box read whole: a long film's sample tables
	// are a few megabytes.
	maxMoov = 128 << 20
	// maxSamples bounds the samples walked: a day of 120 frames a second.
	maxSamples = 10_000_000
	// everyFrameApart is how far apart keyframes are kept from a picture
	// whose every frame is one: a cut needs no finer choice.
	everyFrameApart = 1000
)

// readMP4 reads where an MP4's keyframes lie from its movie box — at the
// start, or after the media, as most writers put it — and its picture's
// sync samples, decode times, composition offsets and edit list.
func readMP4(ctx context.Context, read RangeReader, size int64) (Index, error) {
	for at := int64(0); size <= 0 || at+8 <= size; {
		h, err := read(ctx, at, 16)
		if err != nil {
			return Index{}, err
		}
		if len(h) < 8 {
			break
		}
		boxSize, header := int64(binary.BigEndian.Uint32(h)), int64(8)
		switch {
		case boxSize == 1 && len(h) >= 16:
			boxSize, header = int64(binary.BigEndian.Uint64(h[8:])), 16
		case boxSize == 0 && size > 0:
			boxSize = size - at
		}
		if boxSize < header {
			break
		}
		if string(h[4:8]) == "moov" {
			if boxSize-header > maxMoov {
				return Index{}, ErrNoIndex
			}
			body, err := readAll(ctx, read, at+header, boxSize-header)
			if err != nil {
				return Index{}, err
			}
			return movie(body)
		}
		at += boxSize
	}
	return Index{}, ErrNoIndex
}

// movie reads a movie box: its first picture track that is one of the
// codecs this package names.
func movie(moov []byte) (Index, error) {
	var movieScale uint32
	var found *Index
	boxes(moov, func(kind string, b []byte) {
		switch kind {
		case "mvhd":
			if len(b) >= 20 && b[0] == 1 && len(b) >= 24 {
				movieScale = binary.BigEndian.Uint32(b[20:24])
			} else if len(b) >= 16 {
				movieScale = binary.BigEndian.Uint32(b[12:16])
			}
		case "trak":
			if found == nil {
				if idx, ok := track(b, movieScale); ok {
					found = &idx
				}
			}
		}
	})
	if found == nil {
		return Index{}, ErrNoIndex
	}
	return *found, nil
}

// mp4Track is what a track's boxes say, as far as an index needs.
type mp4Track struct {
	video       bool
	scale       uint32
	duration    uint64
	codec       string
	width       int
	height      int
	dolbyVision int
	stts        []byte
	ctts        []byte
	ctts1       bool
	stss        []byte
	hasStss     bool
	emptyEdit   uint64
	mediaStart  int64
}

func track(trak []byte, movieScale uint32) (Index, bool) {
	t := mp4Track{mediaStart: 0}
	boxes(trak, func(kind string, b []byte) {
		switch kind {
		case "edts":
			boxes(b, func(kind string, b []byte) {
				if kind == "elst" {
					t.editList(b)
				}
			})
		case "mdia":
			boxes(b, func(kind string, b []byte) {
				switch kind {
				case "mdhd":
					if len(b) >= 32 && b[0] == 1 {
						t.scale, t.duration = binary.BigEndian.Uint32(b[20:24]), binary.BigEndian.Uint64(b[24:32])
					} else if len(b) >= 20 {
						t.scale, t.duration = binary.BigEndian.Uint32(b[12:16]), uint64(binary.BigEndian.Uint32(b[16:20]))
					}
				case "hdlr":
					t.video = len(b) >= 12 && string(b[8:12]) == "vide"
				case "minf":
					boxes(b, func(kind string, b []byte) {
						if kind == "stbl" {
							t.sampleTable(b)
						}
					})
				}
			})
		}
	})
	if !t.video || t.codec == "" || t.scale == 0 || len(t.stts) < 8 {
		return Index{}, false
	}
	keyframes, frameDelta, ok := t.keyframes(movieScale)
	if !ok || len(keyframes) == 0 {
		return Index{}, false
	}
	idx := Index{
		Keyframes:  keyframes,
		DurationMs: int64(math.Round(float64(t.duration) * 1000 / float64(t.scale))),
		Video:      Video{Codec: t.codec, Width: t.width, Height: t.height, DolbyVision: t.dolbyVision},
	}
	if frameDelta > 0 {
		idx.Video.FrameRate = math.Round(float64(t.scale)*1000/float64(frameDelta)) / 1000
	}
	return idx, true
}

// editList reads where the presentation begins in the media: after any
// empty edit's delay, at the first edit's media time.
func (t *mp4Track) editList(b []byte) {
	if len(b) < 8 {
		return
	}
	v1 := b[0] == 1
	n := binary.BigEndian.Uint32(b[4:8])
	at := 8
	for i := uint32(0); i < n; i++ {
		var duration uint64
		var start int64
		if v1 {
			if at+20 > len(b) {
				return
			}
			duration, start = binary.BigEndian.Uint64(b[at:]), int64(binary.BigEndian.Uint64(b[at+8:]))
			at += 20
		} else {
			if at+12 > len(b) {
				return
			}
			duration, start = uint64(binary.BigEndian.Uint32(b[at:])), int64(int32(binary.BigEndian.Uint32(b[at+4:])))
			at += 12
		}
		if start == -1 {
			t.emptyEdit += duration
			continue
		}
		t.mediaStart = start
		return
	}
}

// sampleTable keeps the sample table's boxes an index reads.
func (t *mp4Track) sampleTable(stbl []byte) {
	boxes(stbl, func(kind string, b []byte) {
		switch kind {
		case "stsd":
			t.sampleEntry(b)
		case "stts":
			t.stts = b
		case "ctts":
			t.ctts, t.ctts1 = b, len(b) > 0 && b[0] == 1
		case "stss":
			t.stss, t.hasStss = b, true
		}
	})
}

// sampleEntry reads the first sample description: the picture's size and
// its codec's configuration.
func (t *mp4Track) sampleEntry(stsd []byte) {
	if len(stsd) < 16 {
		return
	}
	entry := stsd[8:]
	size := int(binary.BigEndian.Uint32(entry))
	if size < 86 || size > len(entry) {
		return
	}
	kind := string(entry[4:8])
	t.width, t.height = int(binary.BigEndian.Uint16(entry[32:34])), int(binary.BigEndian.Uint16(entry[34:36]))
	family := ""
	switch kind {
	case "avc1", "avc3":
		family = "avc"
	case "hvc1", "hev1", "dvh1", "dvhe":
		family = "hevc"
	case "av01":
		family = "av1"
	}
	boxes(entry[86:size], func(child string, b []byte) {
		switch child {
		case "avcC", "hvcC", "av1C":
			if family != "" && t.codec == "" {
				t.codec = codecName(family, b)
			}
		case "dvcC", "dvvC":
			if len(b) >= 3 {
				t.dolbyVision = int(b[2] >> 1)
			}
		}
	})
}

// keyframes walks the samples once: each sync sample's presentation time in
// milliseconds, and the commonest frame's length in the track's scale.
func (t *mp4Track) keyframes(movieScale uint32) ([]int64, uint32, bool) {
	runs := func(b []byte) (uint32, []byte) {
		if len(b) < 8 {
			return 0, nil
		}
		return binary.BigEndian.Uint32(b[4:8]), b[8:]
	}
	sttsCount, stts := runs(t.stts)
	cttsCount, ctts := runs(t.ctts)
	syncCount, sync := runs(t.stss)
	if uint64(len(stts)) < uint64(sttsCount)*8 || uint64(len(ctts)) < uint64(cttsCount)*8 || uint64(len(sync)) < uint64(syncCount)*4 {
		return nil, 0, false
	}
	shift := t.mediaStart
	if movieScale > 0 && t.emptyEdit > 0 {
		shift -= int64(t.emptyEdit * uint64(t.scale) / uint64(movieScale))
	}
	toMs := func(ts int64) int64 { return int64(math.Round(float64(ts-shift) * 1000 / float64(t.scale))) }

	var frameDelta uint32
	if sttsCount > 0 {
		frameDelta = binary.BigEndian.Uint32(stts[4:8])
	}
	keyframes := []int64{}
	var dts int64
	sample := uint32(1)
	syncAt := uint32(0)
	cttsRun, cttsLeft := uint32(0), uint32(0)
	if cttsCount > 0 {
		cttsLeft = binary.BigEndian.Uint32(ctts[0:4])
	}
	offset := func() int64 {
		if cttsCount == 0 {
			return 0
		}
		for cttsLeft == 0 && cttsRun+1 < cttsCount {
			cttsRun++
			cttsLeft = binary.BigEndian.Uint32(ctts[cttsRun*8:])
		}
		raw := binary.BigEndian.Uint32(ctts[cttsRun*8+4:])
		if cttsLeft > 0 {
			cttsLeft--
		}
		// Version 0 says unsigned; writers put negative offsets there all the same.
		return int64(int32(raw))
	}
	for run := uint32(0); run < sttsCount; run++ {
		count, delta := binary.BigEndian.Uint32(stts[run*8:]), binary.BigEndian.Uint32(stts[run*8+4:])
		for i := uint32(0); i < count; i++ {
			if sample > maxSamples {
				return nil, 0, false
			}
			pts := dts + offset()
			keep := false
			if t.hasStss {
				for syncAt < syncCount && binary.BigEndian.Uint32(sync[syncAt*4:]) < sample {
					syncAt++
				}
				keep = syncAt < syncCount && binary.BigEndian.Uint32(sync[syncAt*4:]) == sample
			} else {
				ms := toMs(pts)
				keep = len(keyframes) == 0 || ms-keyframes[len(keyframes)-1] >= everyFrameApart
			}
			if keep {
				keyframes = append(keyframes, max(0, toMs(pts)))
			}
			dts += int64(delta)
			sample++
		}
	}
	return keyframes, frameDelta, true
}

// boxes calls fn with each box in b, in order, stopping at the first that
// does not fit.
func boxes(b []byte, fn func(kind string, body []byte)) {
	for at := 0; at+8 <= len(b); {
		size, header := int(binary.BigEndian.Uint32(b[at:])), 8
		if size == 1 {
			if at+16 > len(b) {
				return
			}
			size64 := binary.BigEndian.Uint64(b[at+8:])
			if size64 > uint64(len(b)-at) {
				return
			}
			size, header = int(size64), 16
		} else if size == 0 {
			size = len(b) - at
		}
		if size < header || at+size > len(b) {
			return
		}
		fn(string(b[at+4:at+8]), b[at+header:at+size])
		at += size
	}
}
