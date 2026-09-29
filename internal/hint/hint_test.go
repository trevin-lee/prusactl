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
	if c := Connect(); !strings.Contains(c, "installed copy") {
		t.Errorf("bundle: %q should say Connect needs an installed copy", c)
	}
}
