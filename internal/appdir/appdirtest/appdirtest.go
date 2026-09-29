// Package appdirtest points prusactl's configuration directory at a temporary
// one for the duration of a test.
//
// os.UserConfigDir reads a different variable on each platform: XDG_CONFIG_HOME
// or HOME on Linux, HOME on macOS, AppData on Windows. A test that sets only
// the Unix ones passes there and writes into the real prusactl directory on
// Windows, overwriting the config and credentials of whoever ran it.
package appdirtest

import (
	"path/filepath"
	"testing"
)

// Use gives the test its own configuration directory and returns the home it
// is under. The environment is restored when the test ends.
func Use(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)                                            // macOS, and Linux without XDG_CONFIG_HOME
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))       // Linux
	t.Setenv("AppData", filepath.Join(home, "AppData", "Roaming"))    // Windows
	t.Setenv("LocalAppData", filepath.Join(home, "AppData", "Local")) // Windows, for anything reading the cache dir
	return home
}
