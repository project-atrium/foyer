// Command fakezurg is a pretend Zurg on this machine, to run Foyer against
// without Real-Debrid: the torrents of a folder of .zurgtorrent files —
// Zurg's own records of them — sorted into Zurg's default folders, and every
// file the same few seconds of generated video.
//
//	go run ./dev/fakezurg [-addr 127.0.0.1:9999] [-user u -password p] <folder>
//
// It reads names, sizes and dates from the records, and nothing else: their
// links and hashes are Real-Debrid's, and never leave the files.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"foyer/internal/adapters/webdav/webdavtest"
	"foyer/internal/parse"
)

// record is what fakezurg reads of a .zurgtorrent.
type record struct {
	Name          string    `json:"Name"`
	Added         time.Time `json:"Added"`
	SelectedFiles map[string]struct {
		Path     string `json:"path"`
		Bytes    int64  `json:"bytes"`
		Selected int    `json:"selected"`
		Ended    string `json:"Ended"`
		State    string `json:"State"`
	} `json:"SelectedFiles"`
}

// file is one of a torrent's files as Zurg lists it: flat, by the name its
// record keys it by.
type file struct {
	name  string
	bytes int64
	ended time.Time
}

type torrent struct {
	name  string
	added time.Time
	files []file
}

func main() {
	addr := flag.String("addr", "127.0.0.1:9999", "where to listen")
	user := flag.String("user", "", "the Basic sign-in's user, if any")
	password := flag.String("password", "", "and its password")
	sample := flag.String("sample", "", "a video to serve for every file, instead of one made with ffmpeg")
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "usage: fakezurg [flags] <folder of .zurgtorrent files>")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}

	torrents, err := read(flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	content, err := video(*sample)
	if err != nil {
		log.Fatal(err)
	}

	fake := webdavtest.New()
	fake.User, fake.Password, fake.AsZurg = *user, *password, true
	counts := map[string]int{}
	for _, folder := range []string{"__all__", "anime", "shows", "movies"} {
		fake.Dir(folder, time.Time{})
	}
	for _, t := range torrents {
		folder := folderOf(t)
		counts[folder]++
		for _, dir := range []string{"__all__", folder} {
			fake.Dir(dir+"/"+t.name, t.added)
			files := t.files
			if dir == "movies" {
				files = biggest(files)
			}
			for _, f := range files {
				fake.File(dir+"/"+t.name+"/"+f.name, content, f.ended)
			}
		}
	}
	log.Printf("fakezurg: %d torrents — anime %d, shows %d, movies %d — at http://%s/dav/, every file %d bytes",
		len(torrents), counts["anime"], counts["shows"], counts["movies"], *addr, len(content))
	server := &http.Server{Addr: *addr, Handler: fake, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(server.ListenAndServe())
}

func read(dir string) ([]torrent, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.zurgtorrent"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no .zurgtorrent files in %s", dir)
	}
	var torrents []torrent
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var r record
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		t := torrent{name: r.Name, added: r.Added}
		for name, f := range r.SelectedFiles {
			if f.Selected != 1 || !strings.HasPrefix(f.State, "ok") {
				continue
			}
			ended, _ := time.Parse(time.RFC3339, f.Ended)
			t.files = append(t.files, file{name: name, bytes: f.Bytes, ended: ended})
		}
		slices.SortFunc(t.files, func(a, b file) int { return strings.Compare(a.name, b.name) })
		if t.name != "" && len(t.files) > 0 {
			torrents = append(torrents, t)
		}
	}
	return torrents, nil
}

var crc = regexp.MustCompile(`\b[a-fA-F0-9]{8}\b`)

// folderOf is the folder Zurg's default config puts a torrent in, by the
// first rule that takes it: a CRC in a name is anime, a file numbered as an
// episode is shows, and the rest is movies.
func folderOf(t torrent) string {
	if crc.MatchString(t.name) {
		return "anime"
	}
	for _, f := range t.files {
		if crc.MatchString(f.name) {
			return "anime"
		}
	}
	for _, f := range t.files {
		r := parse.Name(f.name)
		if len(r.Episodes) > 0 || len(r.Absolute) > 0 || len(r.Seasons) > 0 {
			return "shows"
		}
	}
	return "movies"
}

// biggest is what Zurg's movies folder shows of a torrent: only its biggest
// file.
func biggest(files []file) []file {
	if len(files) == 0 {
		return nil
	}
	return []file{slices.MaxFunc(files, func(a, b file) int { return int(a.bytes - b.bytes) })}
}

// video is what every file holds: the sample given, or four seconds of 1080p
// made with ffmpeg — Japanese audio, English subtitles and two chapters, so
// a probe has something to read.
func video(sample string) ([]byte, error) {
	if sample != "" {
		return os.ReadFile(sample)
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, errors.New("ffmpeg is not on the PATH: install it, or pass -sample with a video")
	}
	dir, err := os.MkdirTemp("", "fakezurg")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	subtitles := filepath.Join(dir, "en.srt")
	chapters := filepath.Join(dir, "chapters.txt")
	out := filepath.Join(dir, "sample.mkv")
	if err := os.WriteFile(subtitles, []byte("1\n00:00:00,500 --> 00:00:03,500\nA pretend Zurg.\n"), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(chapters, []byte(";FFMETADATA1\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=0\nEND=2000\ntitle=Opening\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=2000\nEND=4000\ntitle=Part A\n"), 0o600); err != nil {
		return nil, err
	}
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-i", subtitles, "-f", "ffmetadata", "-i", chapters,
		"-map", "0:v", "-map", "1:a", "-map", "2:s", "-map_chapters", "3", "-t", "4",
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "40", "-pix_fmt", "yuv420p", "-c:a", "aac", "-ac", "2", "-c:s", "srt",
		"-metadata:s:a:0", "language=jpn", "-metadata:s:s:0", "language=eng", out)
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %w: %s", err, output)
	}
	return os.ReadFile(out)
}
