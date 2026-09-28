package compat

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestMessageSaysWhatChangedAndWhatToDo(t *testing.T) {
	Version = "0.1.2"
	msg := New(Connect, "GET /app/printers no longer includes %q", "printers").Error()
	for _, want := range []string{"Prusa Connect", "prusactl 0.1.2", `"printers"`, "changed its API", "brew upgrade prusactl", Issues} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q: %s", want, msg)
		}
	}
	if msg := New(Printer, "GET /api/v1/status answered 404").Error(); !strings.Contains(msg, "firmware") {
		t.Errorf("printer message should point at the firmware: %s", msg)
	}
}

func TestIsSeesThroughWrapping(t *testing.T) {
	err := fmt.Errorf("listing printers: %w", New(Connect, "x"))
	if !Is(err) || Is(errors.New("printer not found")) {
		t.Fatal("Is misclassified")
	}
}
