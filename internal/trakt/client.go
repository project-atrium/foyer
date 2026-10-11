// Package trakt keeps each user's watch status in step with their own Trakt
// account: connected by a code typed on Trakt's site, then read and written
// every ten minutes, both ways or Foyer's way alone, by plays counted.
package trakt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"foyer/internal/version"
)

// Site is Trakt's API.
const Site = "https://api.trakt.tv"

// oob is the redirect Trakt's device flow names for a refresh.
const oob = "urn:ietf:wg:oauth:2.0:oob"

var (
	// ErrRefused is a token Trakt no longer takes, or an app it does not
	// know: the user's to mend by connecting again — never a 401 to a
	// device, which would take it for its own session's end.
	ErrRefused = errors.New("Trakt refused")
	// ErrBusy is Trakt asking Foyer to wait, for Retry-After.
	ErrBusy = errors.New("Trakt asked to wait")
)

// Client is Trakt for the household's app.
type Client struct {
	HTTP   *http.Client
	Site   string
	ID     string
	Secret string
}

// New is a client for the household's app at developer.trakt.tv.
func New(id, secret string) *Client {
	return &Client{HTTP: &http.Client{Timeout: 30 * time.Second}, Site: Site, ID: id, Secret: secret}
}

// Busy is Trakt's word to wait, and for how long.
type Busy struct{ After time.Duration }

func (b *Busy) Error() string { return "Trakt asked to wait " + b.After.String() }
func (b *Busy) Unwrap() error { return ErrBusy }

// call sends one request, its answer read into into where it is wanted. A
// token in the request, or the app's secret, never reaches an error.
func (c *Client) call(ctx context.Context, method, path, token string, body, into any) (int, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Site+path, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("trakt-api-version", "2")
	req.Header.Set("trakt-api-key", c.ID)
	req.Header.Set("User-Agent", "Foyer/"+version.Version)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("Trakt could not be reached: %w", errors.Unwrap(err))
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusTooManyRequests:
		after, _ := strconv.Atoi(res.Header.Get("Retry-After"))
		return res.StatusCode, &Busy{After: time.Duration(max(after, 1)) * time.Second}
	case res.StatusCode == http.StatusUnauthorized, res.StatusCode == http.StatusForbidden:
		return res.StatusCode, ErrRefused
	case res.StatusCode >= 500:
		return res.StatusCode, fmt.Errorf("Trakt answered %d", res.StatusCode)
	}
	if into != nil && res.StatusCode >= 200 && res.StatusCode < 300 {
		if err := json.NewDecoder(io.LimitReader(res.Body, 64<<20)).Decode(into); err != nil {
			return res.StatusCode, fmt.Errorf("Trakt's answer could not be read: %w", err)
		}
	}
	return res.StatusCode, nil
}

// Check is whether Trakt knows the household's app: a list any app may read.
func (c *Client) Check(ctx context.Context) error {
	status, err := c.call(ctx, http.MethodGet, "/genres/movies", "", nil, nil)
	if err == nil && status != http.StatusOK {
		err = fmt.Errorf("Trakt answered %d", status)
	}
	return err
}

// DeviceCode is what a user types on Trakt's site to connect.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// Code begins connecting a user.
func (c *Client) Code(ctx context.Context) (DeviceCode, error) {
	var code DeviceCode
	status, err := c.call(ctx, http.MethodPost, "/oauth/device/code", "", map[string]string{"client_id": c.ID}, &code)
	if err == nil && (status != http.StatusOK || code.DeviceCode == "") {
		err = fmt.Errorf("Trakt answered %d", status)
	}
	return code, err
}

// Tokens are a user's, as Trakt hands them.
type Tokens struct {
	Access    string `json:"access_token"`
	Refresh   string `json:"refresh_token"`
	ExpiresIn int64  `json:"expires_in"`
	CreatedAt int64  `json:"created_at"`
}

// Expires is when the access token stops working.
func (t Tokens) Expires(now time.Time) time.Time {
	from := now
	if t.CreatedAt > 0 {
		from = time.Unix(t.CreatedAt, 0)
	}
	return from.Add(time.Duration(t.ExpiresIn) * time.Second)
}

// How a code stands, asked.
const (
	Pending = "pending"
	Slower  = "slower"
	Denied  = "denied"
	Expired = "expired"
	Gone    = "gone"
)

// Poll asks whether a code was entered: the tokens once it was, else how it
// stands.
func (c *Client) Poll(ctx context.Context, deviceCode string) (Tokens, string, error) {
	var tokens Tokens
	status, err := c.call(ctx, http.MethodPost, "/oauth/device/token", "",
		map[string]string{"code": deviceCode, "client_id": c.ID, "client_secret": c.Secret}, &tokens)
	var busy *Busy
	switch {
	case errors.As(err, &busy):
		return Tokens{}, Slower, nil
	case err != nil && !errors.Is(err, ErrRefused):
		return Tokens{}, "", err
	}
	switch status {
	case http.StatusOK:
		return tokens, "", nil
	case http.StatusBadRequest:
		return Tokens{}, Pending, nil
	case http.StatusTeapot:
		return Tokens{}, Denied, nil
	case http.StatusGone:
		return Tokens{}, Expired, nil
	case http.StatusNotFound, http.StatusConflict:
		return Tokens{}, Gone, nil
	}
	// An app Trakt does not know, or a secret it does not take.
	return Tokens{}, "", ErrRefused
}

// Refresh is a new pair for a user's refresh token; ErrRefused where Trakt
// takes it no longer.
func (c *Client) Refresh(ctx context.Context, refresh string) (Tokens, error) {
	var tokens Tokens
	status, err := c.call(ctx, http.MethodPost, "/oauth/token", "", map[string]string{
		"refresh_token": refresh, "client_id": c.ID, "client_secret": c.Secret, "redirect_uri": oob, "grant_type": "refresh_token",
	}, &tokens)
	switch {
	case err != nil:
		return Tokens{}, err
	case status == http.StatusOK && tokens.Access != "":
		return tokens, nil
	}
	return Tokens{}, ErrRefused
}

// Revoke ends a token at Trakt, best effort.
func (c *Client) Revoke(ctx context.Context, access string) error {
	_, err := c.call(ctx, http.MethodPost, "/oauth/revoke", "", map[string]string{"token": access, "client_id": c.ID, "client_secret": c.Secret}, nil)
	return err
}

// Account is whose a token is, as Trakt names them.
func (c *Client) Account(ctx context.Context, access string) (string, error) {
	var settings struct {
		User struct {
			Username string `json:"username"`
			Name     string `json:"name"`
		} `json:"user"`
	}
	if _, err := c.call(ctx, http.MethodGet, "/users/settings", access, nil, &settings); err != nil {
		return "", err
	}
	if settings.User.Name != "" {
		return settings.User.Name, nil
	}
	return settings.User.Username, nil
}

// Activities is when a user's history last changed on Trakt.
func (c *Client) Activities(ctx context.Context, access string) (time.Time, error) {
	var last struct {
		All time.Time `json:"all"`
	}
	_, err := c.call(ctx, http.MethodGet, "/sync/last_activities", access, nil, &last)
	return last.All, err
}

// IDs are what Trakt knows a title by; Foyer goes by TMDB's.
type IDs struct {
	Trakt int64 `json:"trakt,omitempty"`
	TMDB  int64 `json:"tmdb,omitempty"`
}

// Watched is one thing Trakt has a user watching: how often, and last when.
type Watched struct {
	Plays int
	Last  time.Time
}

// WatchedMovies is every film a user watched, by TMDB's id.
func (c *Client) WatchedMovies(ctx context.Context, access string) (map[int64]Watched, error) {
	var list []struct {
		Plays int       `json:"plays"`
		Last  time.Time `json:"last_watched_at"`
		Movie struct {
			IDs IDs `json:"ids"`
		} `json:"movie"`
	}
	if _, err := c.call(ctx, http.MethodGet, "/sync/watched/movies", access, nil, &list); err != nil {
		return nil, err
	}
	out := make(map[int64]Watched, len(list))
	for _, m := range list {
		if m.Movie.IDs.TMDB > 0 {
			out[m.Movie.IDs.TMDB] = Watched{Plays: m.Plays, Last: m.Last}
		}
	}
	return out, nil
}

// Episode is one of a series', by TMDB's id for the series.
type Episode struct {
	Show   int64
	Season int
	Number int
}

// WatchedEpisodes is every episode a user watched.
func (c *Client) WatchedEpisodes(ctx context.Context, access string) (map[Episode]Watched, error) {
	var list []struct {
		Show struct {
			IDs IDs `json:"ids"`
		} `json:"show"`
		Seasons []struct {
			Number   int `json:"number"`
			Episodes []struct {
				Number int       `json:"number"`
				Plays  int       `json:"plays"`
				Last   time.Time `json:"last_watched_at"`
			} `json:"episodes"`
		} `json:"seasons"`
	}
	if _, err := c.call(ctx, http.MethodGet, "/sync/watched/shows", access, nil, &list); err != nil {
		return nil, err
	}
	out := map[Episode]Watched{}
	for _, s := range list {
		if s.Show.IDs.TMDB <= 0 {
			continue
		}
		for _, season := range s.Seasons {
			for _, e := range season.Episodes {
				out[Episode{Show: s.Show.IDs.TMDB, Season: season.Number, Number: e.Number}] = Watched{Plays: e.Plays, Last: e.Last}
			}
		}
	}
	return out, nil
}

// History is plays to add to a user's history on Trakt, or things to take
// out of it whole.
type History struct {
	Movies []HistoryMovie `json:"movies,omitempty"`
	Shows  []HistoryShow  `json:"shows,omitempty"`
}

type HistoryMovie struct {
	WatchedAt string `json:"watched_at,omitempty"`
	IDs       IDs    `json:"ids"`
}

type HistoryShow struct {
	IDs     IDs             `json:"ids"`
	Seasons []HistorySeason `json:"seasons"`
}

type HistorySeason struct {
	Number   int              `json:"number"`
	Episodes []HistoryEpisode `json:"episodes"`
}

type HistoryEpisode struct {
	Number    int    `json:"number"`
	WatchedAt string `json:"watched_at,omitempty"`
}

// Empty says whether there is nothing in it.
func (h History) Empty() bool { return len(h.Movies) == 0 && len(h.Shows) == 0 }

// NotFound is what Trakt did not know of a request's, by TMDB's ids.
type NotFound struct {
	Movies   map[int64]bool
	Episodes map[Episode]bool
}

type notFound struct {
	Movies []struct {
		IDs IDs `json:"ids"`
	} `json:"movies"`
	Shows []struct {
		IDs     IDs `json:"ids"`
		Seasons []struct {
			Number   int `json:"number"`
			Episodes []struct {
				Number int `json:"number"`
			} `json:"episodes"`
		} `json:"seasons"`
	} `json:"shows"`
	Episodes []struct {
		IDs IDs `json:"ids"`
	} `json:"episodes"`
}

// has says whether Trakt did not know th.
func (n NotFound) has(th thing) bool {
	if th.movie > 0 {
		return n.Movies[th.movie]
	}
	return n.Episodes[th.episode]
}

func (n notFound) read() NotFound {
	out := NotFound{Movies: map[int64]bool{}, Episodes: map[Episode]bool{}}
	for _, m := range n.Movies {
		out.Movies[m.IDs.TMDB] = true
	}
	for _, s := range n.Shows {
		for _, season := range s.Seasons {
			for _, e := range season.Episodes {
				out.Episodes[Episode{Show: s.IDs.TMDB, Season: season.Number, Number: e.Number}] = true
			}
		}
	}
	return out
}

// Add adds plays to a user's history: each at when Foyer heard of it.
func (c *Client) Add(ctx context.Context, access string, h History) (NotFound, error) {
	var answer struct {
		NotFound notFound `json:"not_found"`
	}
	if _, err := c.call(ctx, http.MethodPost, "/sync/history", access, h, &answer); err != nil {
		return NotFound{}, err
	}
	return answer.NotFound.read(), nil
}

// Remove takes things out of a user's history, every play of each.
func (c *Client) Remove(ctx context.Context, access string, h History) error {
	_, err := c.call(ctx, http.MethodPost, "/sync/history/remove", access, h, nil)
	return err
}
