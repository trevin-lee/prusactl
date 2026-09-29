package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/trevin-lee/prusactl/internal/appdir/appdirtest"
)

// standIn answers the PrusaLink info endpoint like a printer would.
func standIn(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hostname":"stand-in"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runSetup runs `prusactl setup` with the secret piped on stdin.
func runSetup(t *testing.T, secretValue string, args ...string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.WriteString(secretValue + "\n")
	w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()
	if err := setup(context.Background(), append(args, "--password-stdin")); err != nil {
		t.Fatalf("setup %v: %v", args, err)
	}
}

// Re-running setup must not leave the replaced printer's secret behind, and
// --forget must leave nothing but the lock file.
func TestSetupReplacesAndForgetsSecrets(t *testing.T) {
	home := appdirtest.Use(t)
	t.Setenv("PRUSACTL_KEYRING", "file")
	t.Setenv("PRUSACTL_CONFIG", filepath.Join(home, "config.json"))
	a, b := standIn(t), standIn(t)

	runSetup(t, "pass-a", a.URL)
	runSetup(t, "pass-b", b.URL)
	runSetup(t, "key-b", b.URL, "--api-key")

	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(dir, "prusactl", "secrets.json")
	raw, err := os.ReadFile(store)
	if err != nil {
		t.Fatal(err)
	}
	for _, old := range []string{"pass-a", "pass-b"} {
		if contains(string(raw), old) {
			t.Errorf("a replaced secret (%s) is still stored", old)
		}
	}
	if !contains(string(raw), "key-b") {
		t.Error("the current API key isn't stored")
	}

	if err := setup(context.Background(), []string{"--forget"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Errorf("secrets.json should be gone after --forget: %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
