package youtube

import (
	"bytes"
	"compress/flate"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net/url"
	"strings"
)

// sealer seals a link of the site's into a token only Foyer can open: a
// device holds the token, never the link — which names the house's own
// address, and which it could not open from elsewhere anyway.
type sealer struct {
	aead cipher.AEAD
}

func newSealer(key []byte) (*sealer, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &sealer{aead: aead}, nil
}

// seal is a link as a token: deflated, sealed, in URL-safe base64. The
// site's links are long and much alike, and a playlist holds a thousand.
func (s *sealer) seal(link string) (string, error) {
	var packed bytes.Buffer
	w, err := flate.NewWriter(&packed, flate.BestSpeed)
	if err != nil {
		return "", err
	}
	if _, err := io.WriteString(w, link); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize(), s.aead.NonceSize()+packed.Len()+s.aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(s.aead.Seal(nonce, nonce, packed.Bytes(), nil)), nil
}

var errToken = errors.New("that is no link Foyer sealed")

// open is a token's link — the site's own, nothing else.
func (s *sealer) open(token string) (*url.URL, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) < s.aead.NonceSize() {
		return nil, errToken
	}
	packed, err := s.aead.Open(nil, raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():], nil)
	if err != nil {
		return nil, errToken
	}
	plain, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(packed)), 16<<10))
	if err != nil {
		return nil, errToken
	}
	u, err := url.Parse(string(plain))
	if err != nil || u.Scheme != "https" || !siteHost(u.Hostname()) {
		return nil, errToken
	}
	return u, nil
}

// siteHost is a host of the site's streams and captions: what a relayed
// link may lead to, and nothing else.
func siteHost(host string) bool {
	host = strings.ToLower(host)
	for _, domain := range []string{"googlevideo.com", "youtube.com"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}
