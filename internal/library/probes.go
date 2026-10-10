package library

import (
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

// SaveProbe keeps what ffprobe found of a file — read afresh under its
// library's lock, so a scan's change to it is not lost — and draws its
// title's versions and lists anew. A series' own lists wait for Flush: a
// pack's thousand probes would otherwise draw them a thousand times.
func (e *Engine) SaveProbe(file *core.Record, apply func(*core.Record)) error {
	lock := e.writes(file.GetString("library"))
	lock.Lock()
	defer lock.Unlock()
	fresh, err := e.app.FindRecordById(FilesCollection, file.Id)
	if err != nil {
		return err
	}
	apply(fresh)
	if err := e.app.Save(fresh); err != nil {
		return err
	}
	collection, id := fresh.GetString("item_collection"), fresh.GetString("item_id")
	if id == "" || fresh.GetString("role") != "version" {
		return nil
	}
	item, err := e.app.FindRecordById(collection, id)
	if err != nil {
		return nil
	}
	if err := refresh(e.app, item); err != nil {
		return err
	}
	if err := e.app.Save(item); err != nil {
		return err
	}
	if show := item.GetString("show"); show != "" && strings.HasSuffix(collection, "_episodes") {
		e.mu.Lock()
		if e.dirty == nil {
			e.dirty = map[[2]string]string{}
		}
		e.dirty[[2]string{strings.TrimSuffix(collection, "_episodes"), show}] = fresh.GetString("library")
		e.mu.Unlock()
	}
	return nil
}

// Flush draws anew the lists of every series whose episodes a probe changed.
func (e *Engine) Flush() {
	e.mu.Lock()
	dirty := e.dirty
	e.dirty = nil
	e.mu.Unlock()
	for key, library := range dirty {
		lock := e.writes(library)
		lock.Lock()
		if show, err := e.app.FindRecordById(key[0], key[1]); err == nil {
			if err := refreshShow(e.app, show); err == nil {
				_ = e.app.Save(show)
			}
		}
		lock.Unlock()
	}
}
