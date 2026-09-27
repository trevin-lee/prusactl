// Package auth signs in to Prusa Connect with the same OAuth 2.0
// authorization-code + PKCE flow connect.prusa3d.com uses against Prusa
// Account, with the Connect web app's public client. The tokens it yields are
// the ones the web app sends as "Authorization: Bearer" to the /app API.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// These mirror window.ACCOUNT_URL / ACCOUNT_CLIENT_ID in
// https://connect.prusa3d.com/environment.js and the redirect the web app
// registers. Prusa can rotate them; override with the environment variables
// read in Config.
const (
	DefaultAccountURL  = "https://account.prusa3d.com"
	DefaultClientID    = "MRHTlZhZqkNrrQ6FUPtjyusAz8nc59ErHXP8XkS4"
	DefaultRedirectURI = "https://connect.prusa3d.com/login/auth-callback"
)

// ErrNotLoggedIn means there is no usable refresh token; run the login flow.
var ErrNotLoggedIn = errors.New("not signed in to Prusa Connect: run `prusactl login` in a terminal")

// Config identifies the OAuth client.
type Config struct {
	AccountURL  string
	ClientID    string
	RedirectURI string
	HTTP        *http.Client
}

// Token is what Prusa Account's /o/token/ endpoint returns, with the
// access-token expiry made absolute.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Expiry       time.Time `json:"expiry"`
}

// Valid reports whether the access token is usable for at least `margin`.
func (t *Token) Valid(margin time.Duration) bool {
	return t != nil && t.AccessToken != "" && time.Until(t.Expiry) > margin
}

type pkce struct {
	verifier, challenge, state string
}

func newPKCE() (pkce, error) {
	buf := make([]byte, 64)
	if _, err := rand.Read(buf); err != nil {
		return pkce{}, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	st := make([]byte, 24)
	if _, err := rand.Read(st); err != nil {
		return pkce{}, err
	}
	return pkce{
		verifier:  verifier,
		challenge: base64.RawURLEncoding.EncodeToString(sum[:]),
		state:     base64.RawURLEncoding.EncodeToString(st),
	}, nil
}

func (c Config) authorizeURL(p pkce) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {c.ClientID},
		"code_challenge_method": {"S256"},
		"code_challenge":        {p.challenge},
		"redirect_uri":          {c.RedirectURI},
		"state":                 {p.state},
	}
	return strings.TrimRight(c.AccountURL, "/") + "/o/authorize/?" + q.Encode()
}

// codeFromCallback validates the redirect Prusa Account sent the browser to
// and extracts the authorization code.
func (c Config) codeFromCallback(raw string, p pkce) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("unparseable callback URL: %w", err)
	}
	q := u.Query()
	if e := q.Get("error"); e != "" {
		return "", fmt.Errorf("sign-in was refused: %s %s", e, q.Get("error_description"))
	}
	if q.Get("state") != p.state {
		return "", errors.New("sign-in callback had the wrong state parameter; try again")
	}
	code := q.Get("code")
	if code == "" {
		return "", errors.New("sign-in callback carried no authorization code")
	}
	return code, nil
}

// OAuthError is an error response from the token endpoint.
type OAuthError struct {
	Status      int
	Code        string `json:"error"`
	Description string `json:"error_description"`
}

func (e *OAuthError) Error() string {
	msg := fmt.Sprintf("Prusa Account token endpoint returned %d", e.Status)
	if e.Code != "" {
		msg += ": " + e.Code
	}
	if e.Description != "" {
		msg += " (" + e.Description + ")"
	}
	return msg
}

// Revoked reports whether the grant itself is dead, as opposed to a transient
// failure, so the caller knows to discard the stored refresh token.
func (e *OAuthError) Revoked() bool {
	return e.Code == "invalid_grant" || e.Code == "invalid_client" || e.Code == "unauthorized_client"
}

func (c Config) exchange(ctx context.Context, form url.Values) (*Token, error) {
	form.Set("client_id", c.ClientID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.AccountURL, "/")+"/o/token/", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reaching Prusa Account: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		oe := &OAuthError{Status: resp.StatusCode}
		_ = json.Unmarshal(body, oe)
		return nil, oe
	}
	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("unreadable token response: %w", err)
	}
	if raw.AccessToken == "" {
		return nil, errors.New("token response had no access_token")
	}
	t := &Token{AccessToken: raw.AccessToken, RefreshToken: raw.RefreshToken}
	// Prefer the JWT's own exp claim, as the Connect web app does.
	if exp, ok := jwtExpiry(raw.AccessToken); ok {
		t.Expiry = exp
	} else if raw.ExpiresIn > 0 {
		t.Expiry = time.Now().Add(time.Duration(raw.ExpiresIn) * time.Second)
	} else {
		t.Expiry = time.Now().Add(5 * time.Minute)
	}
	return t, nil
}

func (c Config) exchangeCode(ctx context.Context, code string, p pkce) (*Token, error) {
	return c.exchange(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {c.RedirectURI},
		"code_verifier": {p.verifier},
	})
}

// Refresh trades a refresh token for a new token pair. Prusa Account rotates
// refresh tokens, so the returned RefreshToken replaces the old one.
func (c Config) Refresh(ctx context.Context, refreshToken string) (*Token, error) {
	t, err := c.exchange(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	})
	if err != nil {
		return nil, err
	}
	if t.RefreshToken == "" {
		t.RefreshToken = refreshToken
	}
	return t, nil
}

// jwtExpiry reads the exp claim without verifying the signature; it is only
// used to schedule refreshes, never to make a trust decision.
func jwtExpiry(tok string) (time.Time, bool) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp float64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(int64(claims.Exp), 0), true
}

// Subject returns the JWT's sub claim (the Prusa Account user id), if any.
func Subject(tok string) string {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims struct {
		Sub any `json:"sub"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Sub == nil {
		return ""
	}
	return fmt.Sprint(claims.Sub)
}
