// Package copies makes smaller copies of a library's files for devices to
// keep: a download made to a device's own settings — AV1 and Opus in
// Matroska by default — by ffmpeg on this server, made once for every device
// and user that asks for the same, and kept a while after it was last asked
// for. The file on the share is never touched: it is read through the
// loopback, as ffprobe reads it.
package copies

import (
	"slices"
	"strconv"
	"strings"

	"foyer/internal/media"
)

// Original is the option of the file as it is.
const Original = "original"

// What a device may ask a copy to be made as, as Loge names them.
var (
	VideoCodecs = []string{"av1", "hevc", "h264"}
	Preferences = []string{"size", "balanced", "quality"}
	AudioCodecs = []string{"opus", "aac"}
	Containers  = []string{"mkv", "mp4"}
)

// Encode is what a device asks a copy to be made as: its download settings.
type Encode struct {
	Video      string `json:"video"`
	Preference string `json:"preference"`
	// MaxHeight is the tallest picture worth keeping; none, the file's own.
	MaxHeight int `json:"maxHeight,omitzero"`
	// MaxBitrate is a ceiling in bits a second, where the copy aims for a
	// quality rather than a size.
	MaxBitrate int64 `json:"maxBitrate,omitzero"`
	// HDR keeps it, where the file has it; off, it is tone-mapped to SDR
	// where this server's ffmpeg can.
	HDR       bool   `json:"hdr"`
	Audio     string `json:"audio"`
	Container string `json:"container"`
}

// Valid says whether a device's settings are ones a copy can be made to.
func (e Encode) Valid() bool {
	return slices.Contains(VideoCodecs, e.Video) && slices.Contains(Preferences, e.Preference) &&
		slices.Contains(AudioCodecs, e.Audio) && slices.Contains(Containers, e.Container) &&
		(e.MaxHeight == 0 || e.MaxHeight >= 144 && e.MaxHeight <= 4320) &&
		(e.MaxBitrate == 0 || e.MaxBitrate >= 200_000 && e.MaxBitrate <= 400_000_000)
}

// For is the settings as they matter for one file: a ceiling it already
// fits under is none, and HDR it does not have is not kept. Two devices
// asking differently for what comes out the same share one copy.
func (e Encode) For(v media.Version) Encode {
	if v.Video == nil {
		return e
	}
	if e.MaxHeight > 0 && v.Video.Height > 0 && v.Video.Height <= e.MaxHeight && v.Video.Width <= boxWidth(e.MaxHeight) {
		e.MaxHeight = 0
	}
	if v.Video.HDR == "" {
		e.HDR = false
	}
	return e
}

// ID is the option a copy made to these settings is offered as. It names
// the settings, so the download that follows asks for the same: the copy's
// own key is the file's, and the settings'.
func (e Encode) ID() string {
	hdr := "sdr"
	if e.HDR {
		hdr = "hdr"
	}
	return strings.Join([]string{"copy", e.Video, e.Preference, strconv.Itoa(e.MaxHeight), strconv.FormatInt(e.MaxBitrate, 10), hdr, e.Audio, e.Container}, ".")
}

// ParseID reads an option back into the settings it names.
func ParseID(id string) (Encode, bool) {
	parts := strings.Split(id, ".")
	if len(parts) != 8 || parts[0] != "copy" || (parts[5] != "sdr" && parts[5] != "hdr") {
		return Encode{}, false
	}
	height, err1 := strconv.Atoi(parts[3])
	bitrate, err2 := strconv.ParseInt(parts[4], 10, 64)
	if err1 != nil || err2 != nil {
		return Encode{}, false
	}
	e := Encode{Video: parts[1], Preference: parts[2], MaxHeight: height, MaxBitrate: bitrate, HDR: parts[5] == "hdr", Audio: parts[6], Container: parts[7]}
	return e, e.Valid() && e.ID() == id
}

// boxWidth is how wide a picture may be within a height's class: 1080 is
// 1920 across, so a scope film's 1920 by 800 is 1080p and stays as it is.
func boxWidth(height int) int {
	return (height*16/9 + 1) &^ 1
}
