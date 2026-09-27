package connect

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeTokens struct {
	tokens      []string
	invalidated []string
}

func (f *fakeTokens) AccessToken(context.Context) (string, error) { return f.tokens[0], nil }
func (f *fakeTokens) Invalidate(a string) {
	f.invalidated = append(f.invalidated, a)
	f.tokens = f.tokens[1:]
}

func TestRetriesOnceAfter401WithFreshBody(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Upload-Size") != "5" || r.ContentLength != 5 {
			t.Errorf("upload headers not sent: %v %d", r.Header, r.ContentLength)
		}
		_, _ = w.Write([]byte(`{"hash":"h"}`))
	}))
	defer srv.Close()

	tokens := &fakeTokens{tokens: []string{"stale", "good"}}
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), Tokens: tokens}
	var out struct{ Hash string }
	err := c.JSON(context.Background(), Request{
		Method:        http.MethodPut,
		Path:          "/app/teams/1/files/raw",
		Body:          func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("bytes")), nil },
		ContentLength: 5,
		Header:        http.Header{"Upload-Size": {"5"}},
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if out.Hash != "h" || len(bodies) != 2 || bodies[1] != "bytes" {
		t.Fatalf("hash=%q bodies=%q", out.Hash, bodies)
	}
	if len(tokens.invalidated) != 1 || tokens.invalidated[0] != "stale" {
		t.Fatalf("invalidated %v", tokens.invalidated)
	}
}

func TestErrorsCarryStatusAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"PRINTER_BUSY"}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), Tokens: &fakeTokens{tokens: []string{"t"}}}
	err := c.Get(context.Background(), "/app/printers", nil, nil)
	if !IsStatus(err, http.StatusConflict) || !strings.Contains(err.Error(), "PRINTER_BUSY") {
		t.Fatalf("got %v", err)
	}
}
