package library

import (
	"slices"
	"sync"
	"time"
)

// Progress is how a library's scan stands: kept in memory only, and shown on
// the dashboard while it runs.
type Progress struct {
	Library    string     `json:"library"`
	Name       string     `json:"name"`
	Full       bool       `json:"full"`
	Phase      string     `json:"phase"`
	Running    bool       `json:"running"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Entries    int        `json:"entries"`
	Walked     int        `json:"walked"`
	NewFiles   int        `json:"newFiles"`
	Missing    int        `json:"missing"`
	Removed    int        `json:"removed"`
	Titles     int        `json:"titles"`
	Artwork    int        `json:"artwork"`
	Error      string     `json:"error,omitempty"`
	Notes      []string   `json:"notes,omitempty"`
}

type status struct {
	mu   sync.Mutex
	runs map[string]*Progress
}

// run is one scan's handle on its progress.
type run struct {
	s *status
	p *Progress
}

func (s *status) begin(id, name string, full bool) *run {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs == nil {
		s.runs = map[string]*Progress{}
	}
	p := &Progress{Library: id, Name: name, Full: full, Running: true, StartedAt: time.Now().UTC(), Phase: "starting"}
	s.runs[id] = p
	return &run{s: s, p: p}
}

func (r *run) update(fn func(p *Progress)) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	fn(r.p)
}

func (r *run) phase(name string) { r.update(func(p *Progress) { p.Phase = name }) }

// note keeps something a superuser should know, the last few.
func (r *run) note(text string) {
	r.update(func(p *Progress) {
		p.Notes = append(p.Notes, text)
		if len(p.Notes) > 20 {
			p.Notes = p.Notes[len(p.Notes)-20:]
		}
	})
}

func (r *run) finish(err error) {
	r.update(func(p *Progress) {
		now := time.Now().UTC()
		p.Running, p.FinishedAt, p.Phase = false, &now, "done"
		if err != nil {
			p.Error = err.Error()
		}
	})
}

// snapshot is every library's last or running scan.
func (s *status) snapshot() []Progress {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Progress, 0, len(s.runs))
	for _, p := range s.runs {
		copy := *p
		copy.Notes = slices.Clone(p.Notes)
		out = append(out, copy)
	}
	slices.SortFunc(out, func(a, b Progress) int { return b.StartedAt.Compare(a.StartedAt) })
	return out
}
