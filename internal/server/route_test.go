package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trevin-lee/prusactl/internal/auth"
	"github.com/trevin-lee/prusactl/internal/link"
)

type noSession struct{}

func (noSession) Load() (*auth.Token, error) { return nil, auth.ErrNotLoggedIn }
func (noSession) Save(*auth.Token) error     { return nil }
func (noSession) Clear() error               { return nil }

func printer(t *testing.T, delay time.Duration) *link.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hostname":"prusa-core-one","serial":"SN1"}`))
	}))
	t.Cleanup(srv.Close)
	return link.New(link.Config{Host: srv.URL, Auth: link.AuthAPIKey}, "k")
}

func TestCancelledCallIsNotRememberedAsUnreachable(t *testing.T) {
	s := &Server{link: printer(t, 500*time.Millisecond), session: &auth.Session{Store: noSession{}}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.route(ctx, printerRef{}); err == nil {
		t.Fatal("a cancelled call should fail, not route anywhere")
	}
	if !s.probed.IsZero() {
		t.Fatal("the cancelled probe was cached; the next calls would skip the printer")
	}
	if tg, err := s.route(context.Background(), printerRef{}); err != nil || !tg.direct {
		t.Fatalf("next call: %+v, %v; want the direct route", tg, err)
	}
}

func TestSetupChangesAreSeenWithoutRestart(t *testing.T) {
	var next *link.Client
	nextErr := link.ErrNotConfigured
	s := &Server{session: &auth.Session{Store: noSession{}}, linkErr: link.ErrNotConfigured,
		openLink: func() (*link.Client, error) { return next, nextErr }}

	if _, err := s.route(context.Background(), printerRef{}); err == nil {
		t.Fatal("nothing is set up yet")
	}
	next, nextErr = printer(t, 0), nil // the user runs `prusactl setup`
	s.probed = time.Time{}             // let the cache expire
	if tg, err := s.route(context.Background(), printerRef{}); err != nil || !tg.direct {
		t.Fatalf("after setup: %+v, %v", tg, err)
	}
	next, nextErr = nil, link.ErrNotConfigured // `prusactl setup --forget`
	s.probed = time.Time{}
	if _, err := s.route(context.Background(), printerRef{}); err == nil || s.direct() != nil {
		t.Fatalf("after --forget the direct route should be gone: %v", err)
	}
}

func TestForcedConnectWithoutSignInSaysSo(t *testing.T) {
	s := &Server{link: printer(t, 0), session: &auth.Session{Store: noSession{}}}
	_, err := s.route(context.Background(), printerRef{Via: "connect"})
	if err == nil || !strings.Contains(err.Error(), "prusactl login") || strings.Contains(err.Error(), "prusactl setup") {
		t.Fatalf("err = %v; want it to point at `prusactl login` only", err)
	}
	if errors.Is(err, link.ErrNotConfigured) {
		t.Fatal("wrong error")
	}
}
