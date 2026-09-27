package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const redirectURI = "https://connect.example/login/auth-callback"

// fakeAccount imitates Prusa Account's Django login pages and OAuth endpoints.
func fakeAccount(t *testing.T, requireOTP bool) *httptest.Server {
	var challenge, state string
	mux := http.NewServeMux()
	loggedIn := func(r *http.Request) bool {
		c, err := r.Cookie("sessionid")
		return err == nil && c.Value == "ok"
	}
	page := func(w http.ResponseWriter, title, body string) {
		http.SetCookie(w, &http.Cookie{Name: "csrftoken", Value: "csrf1", Path: "/"})
		fmt.Fprintf(w, `<html><head><title>%s</title></head><body>%s</body></html>`, title, body)
	}
	checkCSRF := func(w http.ResponseWriter, r *http.Request) bool {
		_ = r.ParseForm()
		c, err := r.Cookie("csrftoken")
		if err != nil || c.Value != r.Form.Get("csrfmiddlewaretoken") || !strings.HasPrefix(r.Referer(), "http") {
			http.Error(w, "CSRF verification failed", http.StatusForbidden)
			return false
		}
		return true
	}
	mux.HandleFunc("/o/authorize/", func(w http.ResponseWriter, r *http.Request) {
		if !loggedIn(r) {
			http.Redirect(w, r, "/login/?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
			return
		}
		q := r.URL.Query()
		challenge, state = q.Get("code_challenge"), q.Get("state")
		http.Redirect(w, r, q.Get("redirect_uri")+"?code=the-code&state="+url.QueryEscape(state), http.StatusFound)
	})
	loginForm := func(errors string) string {
		return `<form id="recaptcha_form" method="post" action="">` + errors +
			`<input type="hidden" name="csrfmiddlewaretoken" value="csrf1">
			<input type="hidden" name="next" value="` + "NEXT" + `">
			<input type="text" name="email"><input type="password" name="password">
			<button type="submit">Log in</button></form>`
	}
	mux.HandleFunc("/login/", func(w http.ResponseWriter, r *http.Request) {
		next := r.URL.Query().Get("next")
		if r.Method == http.MethodGet {
			page(w, "Prusa Account", strings.Replace(loginForm(""), "NEXT", next, 1))
			return
		}
		if !checkCSRF(w, r) {
			return
		}
		if r.Form.Get("email") != "me@example.com" || r.Form.Get("password") != "hunter2" {
			page(w, "Prusa Account", strings.Replace(loginForm(`<ul class="errorlist"><li>Please enter a correct email and password.</li></ul>`), "NEXT", r.Form.Get("next"), 1))
			return
		}
		if requireOTP {
			http.Redirect(w, r, "/login/totp/?next="+url.QueryEscape(r.Form.Get("next")), http.StatusFound)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "sessionid", Value: "ok", Path: "/"})
		http.Redirect(w, r, r.Form.Get("next"), http.StatusFound)
	})
	mux.HandleFunc("/login/totp/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			page(w, "Two-factor", `<form method="post"><input type="hidden" name="csrfmiddlewaretoken" value="csrf1">
				<input type="text" name="otp_token" inputmode="numeric"><button type="submit">Verify</button></form>`)
			return
		}
		if !checkCSRF(w, r) || r.Form.Get("otp_token") != "123456" {
			http.Error(w, "bad code", http.StatusForbidden)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "sessionid", Value: "ok", Path: "/"})
		http.Redirect(w, r, r.URL.Query().Get("next"), http.StatusFound)
	})
	mux.HandleFunc("/o/token/", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if r.Form.Get("code") != "the-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge ||
			r.Form.Get("redirect_uri") != redirectURI || r.Form.Get("client_id") != "cid" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": fakeJWT(time.Now().Add(time.Hour)), "refresh_token": "r1", "expires_in": 3600,
		})
	})
	return httptest.NewServer(mux)
}

type scripted struct {
	email, password, otp string
	asked                []string
}

func (s *scripted) Email() (string, error) { s.asked = append(s.asked, "email"); return s.email, nil }
func (s *scripted) Password() (string, error) {
	s.asked = append(s.asked, "password")
	return s.password, nil
}
func (s *scripted) OneTimeCode() (string, error) { s.asked = append(s.asked, "otp"); return s.otp, nil }

func TestPasswordLogin(t *testing.T) {
	for _, otp := range []bool{false, true} {
		t.Run(fmt.Sprintf("otp=%v", otp), func(t *testing.T) {
			srv := fakeAccount(t, otp)
			defer srv.Close()
			c := Config{AccountURL: srv.URL, ClientID: "cid", RedirectURI: redirectURI, HTTP: srv.Client()}
			p := &scripted{email: "me@example.com", password: "hunter2", otp: "123 456"}
			tok, err := c.PasswordLogin(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			if tok.RefreshToken != "r1" || !tok.Valid(time.Minute) {
				t.Fatalf("token %+v", tok)
			}
			want := "email,password"
			if otp {
				want += ",otp"
			}
			if strings.Join(p.asked, ",") != want {
				t.Fatalf("asked %v, want %s", p.asked, want)
			}
		})
	}
}

func TestPasswordLoginReportsWrongPassword(t *testing.T) {
	srv := fakeAccount(t, false)
	defer srv.Close()
	c := Config{AccountURL: srv.URL, ClientID: "cid", RedirectURI: redirectURI, HTTP: srv.Client()}
	_, err := c.PasswordLogin(context.Background(), &scripted{email: "me@example.com", password: "wrong"})
	if err == nil || !strings.Contains(err.Error(), "correct email and password") {
		t.Fatalf("got %v", err)
	}
}

func TestPasswordNeverPostedOffPrusaAccount(t *testing.T) {
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("sign-in details reached another host: %s %s", r.Method, r.URL)
	}))
	defer evil.Close()
	account, _ := url.Parse("https://account.prusa3d.com")
	page, _ := url.Parse("https://account.prusa3d.com/login/")
	for _, action := range []string{evil.URL + "/steal", "http://account.prusa3d.com/login/", "//evil.example/login"} {
		_, err := postForm(context.Background(), evil.Client(), account, page, action, url.Values{"password": {"hunter2"}})
		if err == nil || !strings.Contains(err.Error(), "refusing") {
			t.Errorf("action %q: err = %v, want a refusal", action, err)
		}
	}
}
