package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAuthorizeURLCarriesPKCE(t *testing.T) {
	p, err := newPKCE()
	if err != nil {
		t.Fatal(err)
	}
	c := Config{AccountURL: "https://account.example", ClientID: "cid", RedirectURI: "https://app.example/cb"}
	u, err := url.Parse(c.authorizeURL(p))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Path != "/o/authorize/" || q.Get("client_id") != "cid" || q.Get("redirect_uri") != "https://app.example/cb" {
		t.Fatalf("unexpected authorize URL %s", u)
	}
	sum := sha256.Sum256([]byte(p.verifier))
	if q.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(sum[:]) || q.Get("code_challenge_method") != "S256" {
		t.Fatal("code_challenge is not S256 of the verifier")
	}
	if len(p.verifier) < 43 || len(p.verifier) > 128 {
		t.Fatalf("verifier length %d outside RFC 7636 bounds", len(p.verifier))
	}
}

func TestCodeFromCallback(t *testing.T) {
	c := Config{}
	p := pkce{state: "s1"}
	if code, err := c.codeFromCallback("https://app.example/cb?code=abc&state=s1", p); err != nil || code != "abc" {
		t.Fatalf("got %q, %v", code, err)
	}
	for _, bad := range []string{
		"https://app.example/cb?code=abc&state=other",
		"https://app.example/cb?code=abc",
		"https://app.example/cb?state=s1",
		"https://app.example/cb?error=access_denied&state=s1",
	} {
		if _, err := c.codeFromCallback(bad, p); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func fakeJWT(exp time.Time) string {
	enc := base64.RawURLEncoding.EncodeToString
	payload, _ := json.Marshal(map[string]any{"exp": exp.Unix(), "sub": 42})
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(payload) + ".sig"
}

func TestJWTClaims(t *testing.T) {
	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	tok := fakeJWT(exp)
	got, ok := jwtExpiry(tok)
	if !ok || !got.Equal(exp) {
		t.Fatalf("expiry %v, %v", got, ok)
	}
	if Subject(tok) != "42" {
		t.Fatalf("subject %q", Subject(tok))
	}
	if _, ok := jwtExpiry("opaque-token"); ok {
		t.Fatal("parsed an opaque token")
	}
}

type memStore struct {
	mu  sync.Mutex
	tok *Token
}

func (m *memStore) Load() (*Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tok == nil {
		return nil, ErrNotLoggedIn
	}
	cp := *m.tok
	return &cp, nil
}
func (m *memStore) Save(t *Token) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *t
	m.tok = &cp
	return nil
}
func (m *memStore) Clear() error { m.mu.Lock(); defer m.mu.Unlock(); m.tok = nil; return nil }

func TestSessionRefreshesAndRotates(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // keep the refresh lock file out of the real config dir
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("client_id") != "cid" {
			http.Error(w, `{"error":"invalid_request"}`, 400)
			return
		}
		if r.Form.Get("refresh_token") != "r1" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": fakeJWT(time.Now().Add(time.Hour)), "refresh_token": "r2", "expires_in": 3600,
		})
	}))
	defer srv.Close()

	store := &memStore{tok: &Token{RefreshToken: "r1"}}
	s := &Session{Config: Config{AccountURL: srv.URL, ClientID: "cid", HTTP: srv.Client()}, Store: store}
	a1, err := s.AccessToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stored, _ := store.Load(); stored.RefreshToken != "r2" || stored.AccessToken != a1 {
		t.Fatalf("rotated token not persisted: %+v", stored)
	}
	if a2, _ := s.AccessToken(context.Background()); a2 != a1 || calls != 1 {
		t.Fatalf("valid token was refreshed again (calls=%d)", calls)
	}

	// A revoked refresh token clears the session and asks for a new login.
	s.Invalidate(a1)
	store.tok.RefreshToken = "dead"
	if _, err := s.AccessToken(context.Background()); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("want ErrNotLoggedIn, got %v", err)
	}
	if _, err := store.Load(); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatal("revoked session was not cleared")
	}
	if !strings.Contains(s.Config.AccountURL, "127.0.0.1") {
		t.Fatal("test hit a real server")
	}
}

func TestRejectedRefreshKeepsANewerSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := &memStore{tok: &Token{RefreshToken: "old"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.Form.Get("refresh_token") {
		case "old":
			// Meanwhile the user ran `prusactl login` in another terminal.
			_ = store.Save(&Token{RefreshToken: "new"})
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		case "new":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": fakeJWT(time.Now().Add(time.Hour)), "refresh_token": "newer", "expires_in": 3600,
			})
		default:
			t.Errorf("unexpected refresh token %q", r.Form.Get("refresh_token"))
		}
	}))
	defer srv.Close()

	s := &Session{Config: Config{AccountURL: srv.URL, ClientID: "cid", HTTP: srv.Client()}, Store: store}
	if _, err := s.AccessToken(context.Background()); err != nil {
		t.Fatalf("the new session should be used: %v", err)
	}
	if stored, err := store.Load(); err != nil || stored.RefreshToken != "newer" {
		t.Fatalf("stored session = %+v, %v; the new login was lost", stored, err)
	}
}
