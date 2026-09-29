package hint

import (
	"strings"
	"testing"
)

// Advice must fit where prusactl runs: a terminal, or the MCP bundle's
// settings, where there is no terminal and no installed copy.
func TestAdviceFitsWhereItRuns(t *testing.T) {
	t.Setenv("PRUSACTL_MANAGED", "")
	for _, got := range []string{Printer(), WrongSecret(), Connect()} {
		if !strings.Contains(got, "prusactl ") {
			t.Errorf("installed: %q should name a command", got)
		}
	}

	t.Setenv("PRUSACTL_MANAGED", "mcpb")
	for _, got := range []string{Printer(), WrongSecret()} {
		if strings.Contains(got, "prusactl setup") || !strings.Contains(got, "settings") {
			t.Errorf("bundle: %q should point at the extension's settings", got)
		}
	}
	// The bundle reads the same saved session, so it must not claim Connect is
	// unavailable; it just can't sign in itself.
	c := Connect()
	if !strings.Contains(c, "prusactl login") || !strings.Contains(c, "saved session") {
		t.Errorf("bundle: %q should say an installed copy's sign-in is used", c)
	}
	if strings.Contains(c, "directly only") || strings.Contains(c, "unavailable") {
		t.Errorf("bundle: %q wrongly says Connect can't work here", c)
	}
}
