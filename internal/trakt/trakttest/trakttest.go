// Package trakttest is a pretend Trakt, answering as the real one does
// where Foyer asks: an app's codes, a user approving one, tokens that
// expire and refresh, the watched lists, history added and taken out.
package trakttest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Episode is one of a series', by TMDB's id for the series.
type Episode struct {
	Show           int64
	Season, Number int
}

// Played is one thing a user watched: how often, last when.
type Played struct {
	Plays int
	Last  time.Time
}

// User is someone on Trakt, and their history.
type User struct {
	Name     string
	Movies   map[int64]Played
	Episodes map[Episode]Played
	// Activity moves with every change to the history.
	Activity time.Time
}

type code struct {
	device, user string
	state        string // pending, approved, denied
	expires      time.Time
	who          string
}

// Trakt is the pretend site, on its own server.
type Trakt struct {
	*httptest.Server
	ID, Secret string
	// ExpiresIn is how long an access token lasts, in seconds.
	ExpiresIn int64
	// NotKnown are TMDB ids of films Trakt does not know.
	NotKnown map[int64]bool
	// Interval is how often a code may be asked about, in seconds.
	Interval int

	mu      sync.Mutex
	now     func() time.Time
	codes   map[string]*code
	access  map[string]string // token → user
	refresh map[string]string
	users   map[string]*User
	revoked []string
	posts   []string
	busy    int
	serial  int
}

// New is a pretend Trakt for an app of id and secret, stopped with the test.
func New(t *testing.T, id, secret string) *Trakt {
	tr := &Trakt{ID: id, Secret: secret, ExpiresIn: 86400, NotKnown: map[int64]bool{}, Interval: 5, now: time.Now,
		codes: map[string]*code{}, access: map[string]string{}, refresh: map[string]string{}, users: map[string]*User{}}
	tr.Server = httptest.NewServer(http.HandlerFunc(tr.serve))
	t.Cleanup(tr.Close)
	return tr
}

// AddUser is someone on Trakt, with no history yet.
func (tr *Trakt) AddUser(name string) *User {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	u := &User{Name: name, Movies: map[int64]Played{}, Episodes: map[Episode]Played{}, Activity: tr.now()}
	tr.users[name] = u
	return u
}

// Watch adds plays to someone's history, as watching elsewhere would.
func (tr *Trakt) Watch(name string, movie int64, plays int, at time.Time) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	u := tr.users[name]
	p := u.Movies[movie]
	u.Movies[movie] = Played{Plays: p.Plays + plays, Last: at}
	u.Activity = tr.now()
}

// WatchEpisode is Watch for an episode.
func (tr *Trakt) WatchEpisode(name string, e Episode, plays int, at time.Time) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	u := tr.users[name]
	p := u.Episodes[e]
	u.Episodes[e] = Played{Plays: p.Plays + plays, Last: at}
	u.Activity = tr.now()
}

// Unwatch takes a film out of someone's history, as Trakt's site would.
func (tr *Trakt) Unwatch(name string, movie int64) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	delete(tr.users[name].Movies, movie)
	tr.users[name].Activity = tr.now()
}

// History is someone's films, as they stand.
func (tr *Trakt) History(name string) map[int64]Played {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	out := map[int64]Played{}
	for id, p := range tr.users[name].Movies {
		out[id] = p
	}
	return out
}

// EpisodePlays is how often someone watched an episode.
func (tr *Trakt) EpisodePlays(name string, e Episode) int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.users[name].Episodes[e].Plays
}

// Approve is someone typing a code on Trakt's site and saying yes.
func (tr *Trakt) Approve(userCode, name string) {
	tr.decide(userCode, name, "approved")
}

// Deny is someone saying no.
func (tr *Trakt) Deny(userCode string) {
	tr.decide(userCode, "", "denied")
}

func (tr *Trakt) decide(userCode, name, state string) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for _, c := range tr.codes {
		if c.user == userCode {
			c.state, c.who = state, name
		}
	}
}

// Expire ends every access token, as a day passing would.
func (tr *Trakt) Expire() {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.access = map[string]string{}
}

// Forget takes back every refresh token: the user ended the app's access.
func (tr *Trakt) Forget() {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.access, tr.refresh = map[string]string{}, map[string]string{}
}

// Busy answers the next n requests 429.
func (tr *Trakt) Busy(n int) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.busy = n
}

// Posts are the history posts Trakt was sent, in order: "add …", "remove …".
func (tr *Trakt) Posts() []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]string(nil), tr.posts...)
}

// Revoked are the access tokens revoked.
func (tr *Trakt) Revoked() []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]string(nil), tr.revoked...)
}

func (tr *Trakt) tokens(name string) map[string]any {
	tr.serial++
	access, refresh := fmt.Sprintf("access-%d", tr.serial), fmt.Sprintf("refresh-%d", tr.serial)
	tr.access[access], tr.refresh[refresh] = name, name
	return map[string]any{"access_token": access, "refresh_token": refresh, "token_type": "bearer", "expires_in": tr.ExpiresIn, "created_at": tr.now().Unix()}
}

type ids struct {
	TMDB int64 `json:"tmdb"`
}

type history struct {
	Movies []struct {
		WatchedAt string `json:"watched_at"`
		IDs       ids    `json:"ids"`
	} `json:"movies"`
	Shows []struct {
		IDs     ids `json:"ids"`
		Seasons []struct {
			Number   int `json:"number"`
			Episodes []struct {
				Number    int    `json:"number"`
				WatchedAt string `json:"watched_at"`
			} `json:"episodes"`
		} `json:"seasons"`
	} `json:"shows"`
}

func (tr *Trakt) serve(w http.ResponseWriter, r *http.Request) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.busy > 0 {
		tr.busy--
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	if r.Header.Get("trakt-api-key") != tr.ID || r.Header.Get("trakt-api-version") != "2" {
		http.Error(w, "Forbidden - invalid API key or unapproved app", http.StatusForbidden)
		return
	}
	var body map[string]any
	if r.Method == http.MethodPost {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	answer := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	switch r.URL.Path {
	case "/genres/movies":
		answer(http.StatusOK, []map[string]string{{"name": "Action", "slug": "action"}})
		return
	case "/oauth/device/code":
		tr.serial++
		c := &code{device: fmt.Sprintf("device-%d", tr.serial), user: fmt.Sprintf("CODE%04d", tr.serial), state: "pending", expires: tr.now().Add(10 * time.Minute)}
		tr.codes[c.device] = c
		answer(http.StatusOK, map[string]any{"device_code": c.device, "user_code": c.user, "verification_url": "https://trakt.tv/activate", "expires_in": 600, "interval": tr.Interval})
		return
	case "/oauth/device/token":
		if body["client_secret"] != tr.Secret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		c := tr.codes[fmt.Sprint(body["code"])]
		switch {
		case c == nil:
			w.WriteHeader(http.StatusNotFound)
		case tr.now().After(c.expires):
			w.WriteHeader(http.StatusGone)
		case c.state == "denied":
			w.WriteHeader(http.StatusTeapot)
		case c.state == "used":
			w.WriteHeader(http.StatusConflict)
		case c.state == "pending":
			w.WriteHeader(http.StatusBadRequest)
		default:
			c.state = "used"
			answer(http.StatusOK, tr.tokens(c.who))
		}
		return
	case "/oauth/token":
		name, ok := tr.refresh[fmt.Sprint(body["refresh_token"])]
		if !ok || body["client_secret"] != tr.Secret || body["grant_type"] != "refresh_token" {
			answer(http.StatusUnauthorized, map[string]string{"error": "invalid_grant"})
			return
		}
		delete(tr.refresh, fmt.Sprint(body["refresh_token"]))
		answer(http.StatusOK, tr.tokens(name))
		return
	case "/oauth/revoke":
		token := fmt.Sprint(body["token"])
		tr.revoked = append(tr.revoked, token)
		delete(tr.access, token)
		w.WriteHeader(http.StatusOK)
		return
	}
	name, ok := tr.access[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	u := tr.users[name]
	switch r.URL.Path {
	case "/users/settings":
		answer(http.StatusOK, map[string]any{"user": map[string]any{"username": strings.ToLower(u.Name), "name": u.Name}})
	case "/sync/last_activities":
		answer(http.StatusOK, map[string]any{"all": u.Activity.UTC().Format(time.RFC3339), "movies": map[string]any{"watched_at": u.Activity.UTC().Format(time.RFC3339)}})
	case "/sync/watched/movies":
		list := []map[string]any{}
		for id, p := range u.Movies {
			list = append(list, map[string]any{"plays": p.Plays, "last_watched_at": p.Last.UTC().Format(time.RFC3339), "movie": map[string]any{"title": "A film", "ids": map[string]any{"trakt": id + 1000, "tmdb": id}}})
		}
		answer(http.StatusOK, list)
	case "/sync/watched/shows":
		shows := map[int64]map[int][]map[string]any{}
		for e, p := range u.Episodes {
			if shows[e.Show] == nil {
				shows[e.Show] = map[int][]map[string]any{}
			}
			shows[e.Show][e.Season] = append(shows[e.Show][e.Season], map[string]any{"number": e.Number, "plays": p.Plays, "last_watched_at": p.Last.UTC().Format(time.RFC3339)})
		}
		list := []map[string]any{}
		for show, seasons := range shows {
			s := []map[string]any{}
			for number, episodes := range seasons {
				s = append(s, map[string]any{"number": number, "episodes": episodes})
			}
			list = append(list, map[string]any{"plays": 1, "show": map[string]any{"title": "A series", "ids": map[string]any{"tmdb": show}}, "seasons": s})
		}
		answer(http.StatusOK, list)
	case "/sync/history", "/sync/history/remove":
		var h history
		b, _ := json.Marshal(body)
		_ = json.Unmarshal(b, &h)
		remove := r.URL.Path == "/sync/history/remove"
		notFound := []map[string]any{}
		for _, m := range h.Movies {
			switch {
			case tr.NotKnown[m.IDs.TMDB]:
				notFound = append(notFound, map[string]any{"ids": map[string]any{"tmdb": m.IDs.TMDB}})
			case remove:
				delete(u.Movies, m.IDs.TMDB)
				tr.posts = append(tr.posts, fmt.Sprintf("remove movie %d", m.IDs.TMDB))
			default:
				at, _ := time.Parse(time.RFC3339, m.WatchedAt)
				p := u.Movies[m.IDs.TMDB]
				u.Movies[m.IDs.TMDB] = Played{Plays: p.Plays + 1, Last: at}
				tr.posts = append(tr.posts, fmt.Sprintf("add movie %d at %s", m.IDs.TMDB, m.WatchedAt))
			}
		}
		for _, s := range h.Shows {
			for _, season := range s.Seasons {
				for _, e := range season.Episodes {
					key := Episode{Show: s.IDs.TMDB, Season: season.Number, Number: e.Number}
					if remove {
						delete(u.Episodes, key)
						tr.posts = append(tr.posts, fmt.Sprintf("remove episode %d s%de%d", key.Show, key.Season, key.Number))
						continue
					}
					at, _ := time.Parse(time.RFC3339, e.WatchedAt)
					p := u.Episodes[key]
					u.Episodes[key] = Played{Plays: p.Plays + 1, Last: at}
					tr.posts = append(tr.posts, fmt.Sprintf("add episode %d s%de%d", key.Show, key.Season, key.Number))
				}
			}
		}
		u.Activity = tr.now()
		if remove {
			answer(http.StatusOK, map[string]any{"deleted": map[string]int{}, "not_found": map[string]any{"movies": notFound}})
		} else {
			answer(http.StatusCreated, map[string]any{"added": map[string]int{}, "not_found": map[string]any{"movies": notFound}})
		}
	default:
		http.NotFound(w, r)
	}
}
