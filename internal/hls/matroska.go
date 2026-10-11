package hls

import (
	"context"
	"encoding/binary"
	"math"
)

// Matroska's element ids, as its specification numbers them.
const (
	mkvEBML           = 0x1A45DFA3
	mkvSegment        = 0x18538067
	mkvSeekHead       = 0x114D9B74
	mkvSeek           = 0x4DBB
	mkvSeekID         = 0x53AB
	mkvSeekPosition   = 0x53AC
	mkvInfo           = 0x1549A966
	mkvTimestampScale = 0x2AD7B1
	mkvDuration       = 0x4489
	mkvTracks         = 0x1654AE6B
	mkvTrackEntry     = 0xAE
	mkvTrackNumber    = 0xD7
	mkvTrackType      = 0x83
	mkvCodecID        = 0x86
	mkvCodecPrivate   = 0x63A2
	mkvDefaultDur     = 0x23E383
	mkvVideo          = 0xE0
	mkvPixelWidth     = 0xB0
	mkvPixelHeight    = 0xBA
	mkvBlockAddition  = 0x41E4
	mkvBlockAddIDType = 0x41E7
	mkvBlockAddIDData = 0x41ED
	mkvCues           = 0x1C53BB6B
	mkvCuePoint       = 0xBB
	mkvCueTime        = 0xB3
	mkvCuePositions   = 0xB7
	mkvCueTrack       = 0xF7
	mkvCluster        = 0x1F43B675
)

const (
	// headRead is the first read: the header, the SeekHead, and in most
	// files Info and Tracks too.
	headRead = 256 << 10
	// maxElement bounds what one level-1 element may ask to be read.
	maxElement = 64 << 20
	// unknownSize is an element whose size its writer never knew: a live
	// recording's Segment or Cluster.
	unknownSize = -1
)

// mkvTrack is a video track, as Tracks describes it.
type mkvTrack struct {
	number      uint64
	codecID     string
	private     []byte
	width       int
	height      int
	frameNs     uint64
	dolbyVision int
}

// readMatroska reads where a Matroska file's keyframes lie from its Cues,
// found through the SeekHead — at the end of the file, as most writers put
// them, or wherever else.
func readMatroska(ctx context.Context, read RangeReader) (Index, error) {
	head, err := read(ctx, 0, headRead)
	if err != nil {
		return Index{}, err
	}
	id, dataAt, size, ok := element(head, 0)
	if !ok || id != mkvEBML || size == unknownSize {
		return Index{}, ErrNoIndex
	}
	id, segment, _, ok := element(head, dataAt+size)
	if !ok || id != mkvSegment {
		return Index{}, ErrNoIndex
	}

	m := matroska{ctx: ctx, read: read, segment: segment, scale: 1_000_000, seeks: map[uint32][]int64{}, seen: map[uint32]bool{}, done: map[int64]bool{}}
	// The level-1 elements before the first Cluster, as far as the first
	// read reaches: whatever is whole in it is read from it.
	for at := segment; at < int64(len(head)); {
		id, data, size, ok := element(head, at)
		if !ok || size == unknownSize || id == mkvCluster {
			break
		}
		end := data + size
		if end <= int64(len(head)) {
			m.level1(id, head[data:end], at-segment)
		} else if id == mkvSeekHead || id == mkvInfo || id == mkvTracks || id == mkvCues {
			m.seeks[id] = append(m.seeks[id], at-segment)
		}
		at = end
	}
	for _, id := range []uint32{mkvInfo, mkvTracks} {
		if !m.seen[id] {
			if err := m.follow(id); err != nil {
				return Index{}, err
			}
		}
	}
	if m.video == nil {
		return Index{}, ErrNoIndex
	}
	if !m.seen[mkvCues] {
		if err := m.follow(mkvCues); err != nil {
			return Index{}, err
		}
		// A SeekHead may name another, at the end, that names the Cues.
		for _, pos := range m.seeks[mkvSeekHead] {
			if m.seen[mkvCues] || m.done[pos] {
				continue
			}
			if err := m.at(pos); err != nil {
				return Index{}, err
			}
			if err := m.follow(mkvCues); err != nil {
				return Index{}, err
			}
		}
	}
	if len(m.keyframes) == 0 {
		return Index{}, ErrNoIndex
	}
	codec := codecOf(m.video.codecID, m.video.private)
	if codec == "" {
		return Index{}, ErrNoIndex
	}
	idx := Index{
		Keyframes:  m.keyframes,
		DurationMs: int64(math.Round(m.duration * float64(m.scale) / 1e6)),
		Video:      Video{Codec: codec, Width: m.video.width, Height: m.video.height, DolbyVision: m.video.dolbyVision},
	}
	if m.video.frameNs > 0 {
		idx.Video.FrameRate = math.Round(1e12/float64(m.video.frameNs)) / 1000
	}
	return idx, nil
}

// matroska is a file being read: what its level-1 elements said so far.
type matroska struct {
	ctx       context.Context
	read      RangeReader
	segment   int64
	scale     uint64
	duration  float64
	video     *mkvTrack
	cues      []byte
	seeks     map[uint32][]int64
	seen      map[uint32]bool
	done      map[int64]bool
	keyframes []int64
}

// level1 takes in a level-1 element read whole, at pos within the Segment.
func (m *matroska) level1(id uint32, body []byte, pos int64) {
	m.done[pos] = true
	switch id {
	case mkvSeekHead:
		children(body, func(id uint32, seek []byte) {
			if id != mkvSeek {
				return
			}
			var target uint32
			var at int64 = -1
			children(seek, func(id uint32, b []byte) {
				switch id {
				case mkvSeekID:
					target = uint32(uintOf(b))
				case mkvSeekPosition:
					at = int64(uintOf(b))
				}
			})
			if target != 0 && at >= 0 && !m.done[at] {
				m.seeks[target] = append(m.seeks[target], at)
			}
		})
	case mkvInfo:
		m.seen[id] = true
		children(body, func(id uint32, b []byte) {
			switch id {
			case mkvTimestampScale:
				if s := uintOf(b); s > 0 {
					m.scale = s
				}
			case mkvDuration:
				m.duration = floatOf(b)
			}
		})
	case mkvTracks:
		m.seen[id] = true
		children(body, func(id uint32, entry []byte) {
			if id != mkvTrackEntry || m.video != nil {
				return
			}
			if t, ok := videoTrack(entry); ok {
				m.video = &t
			}
		})
	case mkvCues:
		m.seen[id] = true
		m.cues = body
	}
	if m.seen[mkvCues] && m.video != nil && m.keyframes == nil {
		m.keyframes = cueTimes(m.cues, m.video.number, m.scale)
	}
}

// follow reads the elements of an id the SeekHeads named and the first read
// did not hold.
func (m *matroska) follow(id uint32) error {
	for _, pos := range m.seeks[id] {
		if m.seen[id] {
			return nil
		}
		if err := m.at(pos); err != nil {
			return err
		}
	}
	return nil
}

// at reads the level-1 element at pos within the Segment, and takes it in.
func (m *matroska) at(pos int64) error {
	if m.done[pos] {
		return nil
	}
	head, err := m.read(m.ctx, m.segment+pos, 12)
	if err != nil {
		return err
	}
	id, data, size, ok := element(head, 0)
	if !ok || size == unknownSize || size > maxElement {
		m.done[pos] = true
		return nil
	}
	body, err := readAll(m.ctx, m.read, m.segment+pos+data, size)
	if err != nil {
		return err
	}
	m.level1(id, body, pos)
	return nil
}

// videoTrack reads a TrackEntry that is a picture's.
func videoTrack(entry []byte) (mkvTrack, bool) {
	var t mkvTrack
	video := false
	children(entry, func(id uint32, b []byte) {
		switch id {
		case mkvTrackNumber:
			t.number = uintOf(b)
		case mkvTrackType:
			video = uintOf(b) == 1
		case mkvCodecID:
			t.codecID = string(trimNul(b))
		case mkvCodecPrivate:
			t.private = b
		case mkvDefaultDur:
			t.frameNs = uintOf(b)
		case mkvVideo:
			children(b, func(id uint32, b []byte) {
				switch id {
				case mkvPixelWidth:
					t.width = int(uintOf(b))
				case mkvPixelHeight:
					t.height = int(uintOf(b))
				}
			})
		case mkvBlockAddition:
			var kind uint64
			var extra []byte
			children(b, func(id uint32, b []byte) {
				switch id {
				case mkvBlockAddIDType:
					kind = uintOf(b)
				case mkvBlockAddIDData:
					extra = b
				}
			})
			// dvcC or dvvC: a Dolby Vision decoder configuration.
			if (kind == 0x64766343 || kind == 0x64767643) && len(extra) >= 3 {
				t.dolbyVision = int(extra[2] >> 1)
			}
		}
	})
	return t, video && t.number > 0
}

// cueTimes are the times the Cues point at the picture's keyframes, in
// milliseconds.
func cueTimes(cues []byte, track, scale uint64) []int64 {
	times := []int64{}
	children(cues, func(id uint32, point []byte) {
		if id != mkvCuePoint {
			return
		}
		var at uint64
		has, ours := false, false
		children(point, func(id uint32, b []byte) {
			switch id {
			case mkvCueTime:
				at, has = uintOf(b), true
			case mkvCuePositions:
				children(b, func(id uint32, b []byte) {
					if id == mkvCueTrack && uintOf(b) == track {
						ours = true
					}
				})
			}
		})
		if has && ours {
			times = append(times, int64((at*scale+500_000)/1_000_000))
		}
	})
	return times
}

// children calls fn with each child element of body, in order, stopping at
// the first that does not fit.
func children(body []byte, fn func(id uint32, b []byte)) {
	for at := int64(0); at < int64(len(body)); {
		id, data, size, ok := element(body, at)
		if !ok || size == unknownSize || data+size > int64(len(body)) {
			return
		}
		fn(id, body[data:data+size])
		at = data + size
	}
}

// element reads the header of the element at at: its id, where its data
// begins, and its size — unknownSize for one that does not say.
func element(b []byte, at int64) (id uint32, data, size int64, ok bool) {
	if at < 0 || at >= int64(len(b)) {
		return 0, 0, 0, false
	}
	first := b[at]
	idLen := int64(1)
	for mask := byte(0x80); idLen <= 4 && first&mask == 0; mask >>= 1 {
		idLen++
	}
	if idLen > 4 || at+idLen > int64(len(b)) {
		return 0, 0, 0, false
	}
	for i := int64(0); i < idLen; i++ {
		id = id<<8 | uint32(b[at+i])
	}
	at += idLen
	if at >= int64(len(b)) {
		return 0, 0, 0, false
	}
	first = b[at]
	sizeLen := int64(1)
	for mask := byte(0x80); sizeLen <= 8 && first&mask == 0; mask >>= 1 {
		sizeLen++
	}
	if sizeLen > 8 || at+sizeLen > int64(len(b)) {
		return 0, 0, 0, false
	}
	value := uint64(first & (0xFF >> sizeLen))
	all := value == uint64(0xFF>>sizeLen)
	for i := int64(1); i < sizeLen; i++ {
		value = value<<8 | uint64(b[at+i])
		all = all && b[at+i] == 0xFF
	}
	if all {
		return id, at + sizeLen, unknownSize, true
	}
	if value > math.MaxInt64/2 {
		return 0, 0, 0, false
	}
	return id, at + sizeLen, int64(value), true
}

func uintOf(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

func floatOf(b []byte) float64 {
	switch len(b) {
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b)))
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(b))
	}
	return 0
}

func trimNul(b []byte) []byte {
	for len(b) > 0 && b[len(b)-1] == 0 {
		b = b[:len(b)-1]
	}
	return b
}
