package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/trevin-lee/prusactl/internal/link"
)

// fakeStatus serves /api/v1/status, stepping through states on each call.
func fakeStatus(t *testing.T, states ...string) *Server {
	t.Helper()
	var mu sync.Mutex
	i := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		st := states[min(i, len(states)-1)]
		i++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"printer":{"state":%q}}`, st)
	}))
	t.Cleanup(srv.Close)
	return &Server{link: link.New(link.Config{Host: srv.URL, Auth: link.AuthAPIKey}, "k")}
}

func TestReadyToStartRefusesABusyPrinter(t *testing.T) {
	tg := target{direct: true, name: "Core One"}
	for _, st := range []string{"PRINTING", "PAUSED", "BUSY", "ATTENTION"} {
		err := fakeStatus(t, st).readyToStart(context.Background(), tg, true)
		if err == nil || !strings.Contains(err.Error(), st) {
			t.Errorf("%s: err = %v, want a refusal naming the state", st, err)
		}
	}
	for _, st := range []string{"IDLE", "READY"} {
		if err := fakeStatus(t, st).readyToStart(context.Background(), tg, false); err != nil {
			t.Errorf("%s: %v", st, err)
		}
	}
	for _, st := range []string{"FINISHED", "STOPPED"} {
		err := fakeStatus(t, st).readyToStart(context.Background(), tg, false)
		if err == nil || !strings.Contains(err.Error(), "plate_clear") {
			t.Errorf("%s without plate_clear: err = %v, want a request to check the plate", st, err)
		}
		if err := fakeStatus(t, st).readyToStart(context.Background(), tg, true); err != nil {
			t.Errorf("%s with plate_clear: %v", st, err)
		}
	}
}

func TestStartReportSaysWhatActuallyHappened(t *testing.T) {
	tg := target{direct: true}
	got := startReport(fakeStatus(t, "IDLE", "PRINTING").startedState(context.Background(), tg))
	if got["printing"] != true || got["note"] != nil {
		t.Errorf("started print: %v", got)
	}
	got = startReport(fakeStatus(t, "IDLE", "ATTENTION").startedState(context.Background(), tg))
	if got["printing"] != false || !strings.Contains(fmt.Sprint(got["note"]), "question") {
		t.Errorf("print stopped on a question: %v", got)
	}
}
