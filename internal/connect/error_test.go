package connect

import (
	"strings"
	"testing"
)

func TestAPIErrorMasksBody(t *testing.T) {
	e := &APIError{Method: "GET", Path: "/app/printers/x", Status: 500, Body: `{"api_key":"abc123","detail":"boom"}`}
	if msg := e.Error(); strings.Contains(msg, "abc123") || !strings.Contains(msg, "boom") {
		t.Fatalf("error = %s", msg)
	}
}
