package hls

import (
	"encoding/binary"
	"fmt"
	"math/bits"
	"strings"
)

// codecOf names a Matroska video track's codec as a master playlist does —
// RFC 6381, from the track's own configuration record — or nothing for one
// this package does not remux.
func codecOf(codecID string, config []byte) string {
	switch codecID {
	case "V_MPEG4/ISO/AVC":
		return codecName("avc", config)
	case "V_MPEGH/ISO/HEVC":
		return codecName("hevc", config)
	case "V_AV1":
		return codecName("av1", config)
	}
	return ""
}

// codecName names a picture's codec from its configuration record: avcC,
// hvcC or av1C. HEVC is named `hvc1`, as the segments are tagged, never
// `hev1`: AVPlayer takes only the first.
func codecName(kind string, c []byte) string {
	switch kind {
	case "avc":
		if len(c) < 4 || c[0] != 1 {
			return ""
		}
		return fmt.Sprintf("avc1.%02X%02X%02X", c[1], c[2], c[3])
	case "hevc":
		if len(c) < 13 || c[0] != 1 {
			return ""
		}
		var b strings.Builder
		b.WriteString("hvc1.")
		if space := c[1] >> 6; space > 0 {
			b.WriteByte("ABC"[space-1])
		}
		tier := byte('L')
		if c[1]&0x20 != 0 {
			tier = 'H'
		}
		fmt.Fprintf(&b, "%d.%X.%c%d", c[1]&0x1F, bits.Reverse32(binary.BigEndian.Uint32(c[2:6])), tier, c[12])
		constraints := c[6:12]
		for len(constraints) > 0 && constraints[len(constraints)-1] == 0 {
			constraints = constraints[:len(constraints)-1]
		}
		for _, x := range constraints {
			fmt.Fprintf(&b, ".%X", x)
		}
		return b.String()
	case "av1":
		if len(c) < 3 || c[0]&0x80 == 0 {
			return ""
		}
		tier := byte('M')
		if c[2]&0x80 != 0 {
			tier = 'H'
		}
		depth := 8
		if c[2]&0x40 != 0 {
			depth = 10
			if c[2]&0x20 != 0 {
				depth = 12
			}
		}
		return fmt.Sprintf("av01.%d.%02d%c.%02d", c[1]>>5, c[1]&0x1F, tier, depth)
	}
	return ""
}
