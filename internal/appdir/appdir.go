// Package appdir locates prusactl's configuration directory.
package appdir

import (
	"os"
	"path/filepath"
)

// Dir returns (creating it if needed) the per-user prusactl directory, e.g.
// ~/Library/Application Support/prusactl on macOS.
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "prusactl")
	return dir, os.MkdirAll(dir, 0o700)
}
