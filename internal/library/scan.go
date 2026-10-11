package library

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"

	"foyer/internal/adapters/webdav"
	"foyer/internal/parse"
)

const (
	// grace is how long a file the share stopped listing is kept: Zurg's
	// repairs take files away for a while.
	grace = 72 * time.Hour
	// walkDepth is how deep an entry's folders are walked.
	walkDepth = 3
)

// ErrScanning is a scan asked for while one runs: the running one is enough.
var ErrScanning = errors.New("this library is being scanned already")

// Scan reads a library from the share and brings its collections up to
// what it holds: new entries walked and their files named — an entry is a
// torrent on Zurg, a folder or a file elsewhere — files it stopped listing
// let go after a grace period, artwork fetched. A full scan walks every
// entry; otherwise only new and changed ones.
func (e *Engine) Scan(ctx context.Context, libID string, full bool) error {
	lib, err := e.app.FindRecordById(LibrariesCollection, libID)
	if err != nil {
		return err
	}
	if !e.claim(lib.Id) {
		return ErrScanning
	}
	defer e.unclaim(lib.Id)
	run := e.status.begin(lib.Id, lib.GetString("name"), full)
	err = e.scan(ctx, lib, full, run)
	run.finish(err)
	if e.scanned != nil {
		e.scanned()
	}

	lib, findErr := e.app.FindRecordById(LibrariesCollection, libID)
	if findErr != nil {
		return errors.Join(err, findErr)
	}
	now := types.NowDateTime()
	lib.Set("last_scan_at", now)
	if full && err == nil {
		lib.Set("last_full_scan_at", now)
	}
	message := ""
	if err != nil {
		message = err.Error()
	}
	lib.Set("scan_error", message)
	return errors.Join(err, e.app.Save(lib))
}

func (e *Engine) claim(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.scanning[id] {
		return false
	}
	e.scanning[id] = true
	return true
}

func (e *Engine) unclaim(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.scanning, id)
}

// writes is a library's lock on its records: a scan, the artwork it fetches
// and a probe's results each load, change and save, and must not cross.
func (e *Engine) writes(id string) *sync.Mutex {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.locks[id] == nil {
		e.locks[id] = &sync.Mutex{}
	}
	return e.locks[id]
}

type walked struct {
	entry webdav.Entry
	files []webdav.File
	err   error
}

func (e *Engine) scan(ctx context.Context, lib *core.Record, full bool, run *run) error {
	client, err := e.shareOf(lib)
	if err != nil {
		return err
	}
	folder := lib.GetString("folder")
	run.phase("listing")
	entries, err := client.List(ctx, folder)
	if err != nil {
		return err
	}
	records, err := e.app.FindAllRecords(EntriesCollection, dbx.HashExp{"library": lib.Id})
	if err != nil {
		return err
	}
	known := map[string]*core.Record{}
	for _, t := range records {
		known[t.GetString("name")] = t
	}
	listed := map[string]bool{}
	for _, entry := range entries {
		listed[entry.Name] = true
	}
	now := e.now().UTC()

	// A share lists nothing while it starts, and Zurg less while it repairs:
	// what it stopped listing is let go only when it says so twice.
	var vanished []*core.Record
	for name, t := range known {
		if !listed[name] && t.GetString("missing_since") == "" {
			vanished = append(vanished, t)
		}
	}
	if len(entries) == 0 && len(known) > 0 {
		return errors.New("the share listed nothing in this folder; nothing is taken away until it lists it again")
	}
	if len(known) >= 10 && 2*len(vanished) > len(known) && !e.confirmVanish(lib.Id, now) {
		run.note(fmt.Sprintf("The share stopped listing %d of %d entries; nothing is taken away until the next scan agrees", len(vanished), len(known)))
		vanished = nil
	}

	var walk []webdav.Entry
	for _, entry := range entries {
		t, ok := known[entry.Name]
		switch {
		case !ok, full, t.GetString("missing_since") != "", t.GetString("error") != "",
			!entry.Modified.Equal(t.GetDateTime("modified").Time()):
			walk = append(walk, entry)
		}
	}
	run.update(func(p *Progress) { p.Entries = len(entries) })

	run.phase("walking")
	plausible := plausibleDates(entries, now)
	results := make([]walked, len(walk))
	var wg sync.WaitGroup
	for i, entry := range walk {
		wg.Go(func() {
			w := walked{entry: entry}
			if entry.Dir {
				w.files, w.err = client.Walk(ctx, folder, entry.Name, walkDepth)
			} else {
				w.files = []webdav.File{{Path: entry.Name, Size: entry.Size, Modified: entry.Modified}}
			}
			results[i] = w
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}

	run.phase("recording")
	lock := e.writes(lib.Id)
	for _, w := range results {
		lock.Lock()
		added, err := e.record(lib, known[w.entry.Name], w, plausible, now)
		lock.Unlock()
		if err != nil {
			// One entry that will not be kept holds the library back no more:
			// it is told, and the next scan tries it again.
			run.note(fmt.Sprintf("%s was not kept: %v", w.entry.Name, err))
			e.app.Logger().Warn("An entry was not kept", "library", lib.GetString("name"), "entry", w.entry.Name, "error", err.Error())
			continue
		}
		run.update(func(p *Progress) { p.Walked++; p.NewFiles += added })
	}
	if len(vanished) > 0 {
		lock.Lock()
		err := e.app.RunInTransaction(func(tx core.App) error {
			for _, t := range vanished {
				t.Set("missing_since", now)
				if err := tx.Save(t); err != nil {
					return err
				}
				files, err := tx.FindAllRecords(FilesCollection, dbx.HashExp{"entry": t.Id})
				if err != nil {
					return err
				}
				for _, f := range files {
					if f.GetString("missing_since") == "" {
						f.Set("missing_since", now)
						if err := tx.Save(f); err != nil {
							return err
						}
					}
				}
			}
			return nil
		})
		lock.Unlock()
		if err != nil {
			return err
		}
		run.update(func(p *Progress) { p.Missing += len(vanished) })
	}

	run.phase("identifying")
	if err := e.placeWaiting(ctx, lib, full, run); err != nil {
		return err
	}
	run.phase("cleaning up")
	if err := e.sweep(lib, now, run); err != nil {
		return err
	}
	run.phase("artwork")
	e.fetchArtwork(ctx, lib, run)
	return nil
}

// confirmVanish says whether the share dropped most of a library twice, ten
// minutes or more apart.
func (e *Engine) confirmVanish(id string, now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	first, ok := e.vanishing[id]
	if !ok || now.Sub(first) > 24*time.Hour {
		e.vanishing[id] = now
		return false
	}
	if now.Sub(first) < 10*time.Minute {
		return false
	}
	delete(e.vanishing, id)
	return true
}

// plausibleDates says whether a listing's dates are its entries' own: not
// all one second — the share's clock, not when anything came — where there
// are many. Zurg's are when Real-Debrid added each torrent.
func plausibleDates(entries []webdav.Entry, now time.Time) bool {
	if len(entries) < 10 {
		return true
	}
	counts := map[int64]int{}
	most := 0
	for _, e := range entries {
		n := counts[e.Modified.Unix()] + 1
		counts[e.Modified.Unix()] = n
		most = max(most, n)
	}
	return most*10 < len(entries)*9
}

// plausible says whether a date can be when something was added.
func plausible(t, now time.Time) bool {
	return !t.IsZero() && t.Year() >= 2008 && t.Before(now.Add(24*time.Hour))
}

// record keeps one entry as the share listed it: its files, new ones waiting
// to be named and probed, ones it no longer lists marked missing. It answers
// how many files were new.
func (e *Engine) record(lib, existing *core.Record, w walked, datesPlausible bool, now time.Time) (int, error) {
	added := 0
	err := e.app.RunInTransaction(func(tx core.App) error {
		t := existing
		if t == nil {
			collection, err := tx.FindCollectionByNameOrId(EntriesCollection)
			if err != nil {
				return err
			}
			t = core.NewRecord(collection)
			t.Set("library", lib.Id)
			t.Set("name", w.entry.Name)
			t.Set("loose", !w.entry.Dir)
			t.Set("first_seen", now)
		}
		if !w.entry.Modified.IsZero() {
			t.Set("modified", w.entry.Modified)
		}
		t.Set("missing_since", "")
		message := ""
		if w.err != nil {
			message = w.err.Error()
		}
		t.Set("error", message)
		if err := tx.Save(t); err != nil {
			return err
		}
		if w.err != nil {
			return nil
		}

		existing, err := tx.FindAllRecords(FilesCollection, dbx.HashExp{"entry": t.Id})
		if err != nil {
			return err
		}
		byPath := map[string]*core.Record{}
		for _, f := range existing {
			byPath[f.GetString("path")] = f
		}
		siblings := map[string][]string{}
		for _, f := range w.files {
			dir := path.Dir(f.Path)
			siblings[dir] = append(siblings[dir], path.Base(f.Path))
		}
		collection, err := tx.FindCollectionByNameOrId(FilesCollection)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, f := range w.files {
			name := path.Base(f.Path)
			if !parse.IsVideo(name) {
				continue
			}
			placed := parse.Path(f.Path, siblings[path.Dir(f.Path)])
			if placed.Sample {
				continue
			}
			seen[f.Path] = true
			r := byPath[f.Path]
			if r == nil {
				r = core.NewRecord(collection)
				r.Set("library", lib.Id)
				r.Set("entry", t.Id)
				r.Set("path", f.Path)
				r.Set("name", name)
				r.Set("role", "pending")
				r.Set("probe_state", "pending")
				when, source := t.GetDateTime("first_seen").Time(), "seen"
				switch {
				case datesPlausible && plausible(w.entry.Modified, now):
					when, source = w.entry.Modified, "folder"
				case plausible(f.Modified, now):
					when, source = f.Modified, "file"
				}
				r.Set("added", when)
				r.Set("added_source", source)
				added++
			} else if r.GetInt64("size") != f.Size {
				r.Set("probe_state", "pending")
				r.Set("probe_attempts", 0)
			}
			r.Set("size", f.Size)
			if !f.Modified.IsZero() {
				r.Set("modified", f.Modified)
			}
			r.Set("missing_since", "")
			r.Set("parsed", placed)
			r.Set("label", label(placed.Release))
			if err := tx.Save(r); err != nil {
				return err
			}
		}
		for p, r := range byPath {
			if !seen[p] && r.GetString("missing_since") == "" {
				r.Set("missing_since", now)
				if err := tx.Save(r); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return added, err
}

// label is what tells a title's versions apart, as their names say it:
// "Uncropped · 480p", "2160p · Dolby Vision · Remux".
func label(r parse.Release) string {
	var parts []string
	if r.Edition != "" {
		parts = append(parts, r.Edition)
	}
	if r.Resolution != 0 {
		res := fmt.Sprintf("%dp", r.Resolution)
		if r.Upscaled {
			res += " upscale"
		}
		parts = append(parts, res)
	}
	for _, h := range r.HDR {
		switch h {
		case "dolby-vision":
			parts = append(parts, "Dolby Vision")
		case "hdr10+":
			parts = append(parts, "HDR10+")
		case "hdr10":
			parts = append(parts, "HDR")
		}
		break
	}
	if r.Remux {
		parts = append(parts, "Remux")
	}
	return strings.Join(parts, " · ")
}
