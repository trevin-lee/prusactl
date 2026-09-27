//go:build unix

package auth

import (
	"os"
	"path/filepath"
	"syscall"

	"github.com/trevin-lee/prusactl/internal/appdir"
)

// lockRefresh serializes token refreshes across processes. Prusa Account
// rotates refresh tokens, so two MCP server processes refreshing with the same
// token at once would leave one of them holding a revoked token.
func lockRefresh() (unlock func(), err error) {
	dir, err := appdir.Dir()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "refresh.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
