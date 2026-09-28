package connect

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trevin-lee/prusactl/internal/compat"
)

type fixedToken struct{}

func (fixedToken) AccessToken(context.Context) (string, error) { return "t", nil }
func (fixedToken) Invalidate(string)                           {}

// Each case is a real response shape: Connect answers a missing route with
// NOT_FOUND_ENDPOINT and a missing record with a specific code.
func TestChangedAPIIsReportedAndNormalErrorsAreNot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/gone":
			w.WriteHeader(404)
			fmt.Fprint(w, `{"message": "Endpoint not found", "code": "NOT_FOUND_ENDPOINT"}`)
		case "/app/printers/nope":
			w.WriteHeader(404)
			fmt.Fprint(w, `{"message": "Printer ID not found", "code": "NOT_FOUND_PRINTER"}`)
		case "/app/bare404":
			w.WriteHeader(404)
			fmt.Fprint(w, `<html>not found</html>`)
		case "/app/method":
			w.WriteHeader(405)
		case "/app/html":
			fmt.Fprint(w, `<html>maintenance</html>`)
		case "/app/list":
			fmt.Fprint(w, `{"printers": {"renamed": true}}`)
		case "/app/busy":
			w.WriteHeader(409)
			fmt.Fprint(w, `{"code": "CONFLICT_COMMAND"}`)
		}
	}))
	defer srv.Close()
	c := New(fixedToken{}, "test")
	c.BaseURL = srv.URL

	var list struct {
		Printers []string `json:"printers"`
	}
	for path, wantCompat := range map[string]bool{
		"/app/gone": true, "/app/bare404": true, "/app/method": true, "/app/html": true, "/app/list": true,
		"/app/printers/nope": false, "/app/busy": false,
	} {
		err := c.Get(context.Background(), path, nil, &list)
		if err == nil {
			t.Errorf("%s: no error", path)
			continue
		}
		if compat.Is(err) != wantCompat {
			t.Errorf("%s: compat=%v, want %v (%v)", path, compat.Is(err), wantCompat, err)
		}
	}
	// A wrapped 404 still reads as a 404 for callers that fall back on it.
	if err := c.Get(context.Background(), "/app/bare404", nil, nil); !IsStatus(err, 404) {
		t.Errorf("IsStatus lost through wrapping: %v", err)
	}
}
