//go:build windows

package auth

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"

	"github.com/trevin-lee/prusactl/internal/appdir"
)

// lockRefresh serializes token refreshes across processes (see lock_unix.go).
func lockRefresh() (unlock func(), err error) {
	dir, err := appdir.Dir()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "refresh.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	h := windows.Handle(f.Fd())
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, ol); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = windows.UnlockFileEx(h, 0, 1, 0, ol)
		f.Close()
	}, nil
}
