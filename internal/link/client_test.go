package link

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakePrinter checks Digest auth the way PrusaLink does (MD5, qop=auth).
func fakePrinter(t *testing.T, onUpload func(body string)) (*httptest.Server, *int) {
	const realm, nonce, user, pass = "Printer API", "n0nce", "maker", "secret"
	uploads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ch, ok := parseChallenge(r.Header.Get("Authorization"))
		valid := false
		if ok {
			params := map[string]string{}
			for _, p := range splitParams(strings.TrimPrefix(r.Header.Get("Authorization"), "Digest ")) {
				k, v, _ := strings.Cut(strings.TrimSpace(p), "=")
				params[k] = strings.Trim(v, `"`)
			}
			ha1 := md5hex(user + ":" + realm + ":" + pass)
			ha2 := md5hex(r.Method + ":" + params["uri"])
			want := md5hex(strings.Join([]string{ha1, ch.nonce, params["nc"], params["cnonce"], "auth", ha2}, ":"))
			valid = params["username"] == user && params["response"] == want && params["uri"] == r.URL.RequestURI()
		}
		if !valid {
			w.Header().Set("WWW-Authenticate", `Digest realm="`+realm+`", nonce="`+nonce+`", qop="auth", stale=FALSE`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			uploads++
			onUpload(string(b))
			w.WriteHeader(http.StatusCreated)
		case r.URL.Path == "/api/v1/job":
			w.WriteHeader(http.StatusNoContent)
		default:
			_, _ = w.Write([]byte(`{"name":"Core One","serial":"SN1"}`))
		}
	}))
	return srv, &uploads
}

func TestDigestAuthAndNoContent(t *testing.T) {
	srv, _ := fakePrinter(t, nil)
	defer srv.Close()
	c := New(Config{Host: srv.URL, User: "maker", Auth: AuthDigest}, "secret")
	var info struct{ Name, Serial string }
	if found, err := c.Get(context.Background(), "/api/v1/info", &info); err != nil || !found || info.Serial != "SN1" {
		t.Fatalf("info=%+v found=%v err=%v", info, found, err)
	}
	if found, err := c.Get(context.Background(), "/api/v1/job", nil); err != nil || found {
		t.Fatalf("job: found=%v err=%v", found, err)
	}
	bad := New(Config{Host: srv.URL, User: "maker", Auth: AuthDigest}, "wrong")
	if _, err := bad.Get(context.Background(), "/api/v1/info", nil); !IsStatus(err, http.StatusUnauthorized) {
		t.Fatalf("wrong password: %v", err)
	}
}

func TestUploadIsSentOnce(t *testing.T) {
	var got []string
	srv, uploads := fakePrinter(t, func(b string) { got = append(got, b) })
	defer srv.Close()
	c := New(Config{Host: srv.URL, User: "maker", Auth: AuthDigest}, "secret")
	_, err := c.JSON(context.Background(), Request{
		Method:        http.MethodPut,
		Path:          "/api/v1/files/usb/a.gcode",
		Body:          func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("G28\n")), nil },
		ContentLength: 4,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if *uploads != 1 || got[0] != "G28\n" {
		t.Fatalf("uploads=%d bodies=%q", *uploads, got)
	}
}

func TestFilePath(t *testing.T) {
	for in, want := range map[string]string{
		"/usb/a b.bgcode":  "/api/v1/files/usb/a%20b.bgcode",
		"usb/dir/x.gcode":  "/api/v1/files/usb/dir/x.gcode",
		"/usb/":            "/api/v1/files/usb/",
		"/usb/../etc/pass": "",
	} {
		got, err := FilePath(in)
		if want == "" {
			if err == nil {
				t.Errorf("%q: accepted", in)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestNormalizeHost(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.8.162":          "http://192.168.8.162",
		"prusa.local:8080":       "http://prusa.local:8080",
		"https://printer.lan/x/": "https://printer.lan",
	} {
		if got, err := NormalizeHost(in); err != nil || got != want {
			t.Errorf("%q: got %q, %v", in, got, err)
		}
	}
}

func TestEnvIgnoresUnfilledPlaceholders(t *testing.T) {
	t.Setenv("PRUSACTL_HOST", "${user_config.printer_address}")
	t.Setenv("PRUSACTL_CONFIG", t.TempDir()+"/none.json")
	if _, err := LoadConfig(); err != ErrNotConfigured {
		t.Fatalf("placeholder was used as an address: %v", err)
	}
	t.Setenv("PRUSACTL_HOST", " 10.0.0.5 ")
	cfg, err := LoadConfig()
	if err != nil || cfg.Host != "http://10.0.0.5" || cfg.User != "maker" {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}
