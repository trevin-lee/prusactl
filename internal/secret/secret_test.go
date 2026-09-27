package secret

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zalando/go-keyring"
)

func useTempFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secrets.json")
	old := filePath
	filePath = func() (string, error) { return path, nil }
	t.Cleanup(func() { filePath = old })
	return path
}

func TestFileBackend(t *testing.T) {
	path := useTempFile(t)
	t.Setenv("PRUSACTL_KEYRING", "file")

	if _, err := Get("a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty store: %v", err)
	}
	if err := Set("a", "one"); err != nil {
		t.Fatal(err)
	}
	if err := Set("b", "two"); err != nil {
		t.Fatal(err)
	}
	if v, err := Get("a"); err != nil || v != "one" {
		t.Fatalf("Get(a) = %q, %v", v, err)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(path)
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("file mode %v, err %v; want 0600", st.Mode().Perm(), err)
		}
	}
	if err := Delete("a"); err != nil {
		t.Fatal(err)
	}
	if err := Delete("a"); err != nil {
		t.Fatalf("deleting a missing secret: %v", err)
	}
	if err := Delete("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty store should remove the file: %v", err)
	}
}

func TestFallsBackWhenKeychainUnavailable(t *testing.T) {
	useTempFile(t)
	keyring.MockInitWithError(errors.New("exec: \"dbus-launch\": executable file not found in $PATH"))
	t.Cleanup(keyring.MockInit)
	warned = true // keep test output quiet

	if err := Set("printer", "hunter2"); err != nil {
		t.Fatalf("Set should fall back to the file: %v", err)
	}
	if v, err := Get("printer"); err != nil || v != "hunter2" {
		t.Fatalf("Get = %q, %v", v, err)
	}
	if err := Delete("printer"); err != nil {
		t.Fatal(err)
	}
	if _, err := Get("printer"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after Delete: %v", err)
	}
}

func TestKeychainPreferredAndFileCopyCleared(t *testing.T) {
	useTempFile(t)
	keyring.MockInit()
	t.Setenv("PRUSACTL_KEYRING", "file")
	if err := Set("printer", "old"); err != nil { // an earlier copy in the file
		t.Fatal(err)
	}
	t.Setenv("PRUSACTL_KEYRING", "")
	if v, err := Get("printer"); err != nil || v != "old" {
		t.Fatalf("file copy should still be found: %q, %v", v, err)
	}
	if err := Set("printer", "new"); err != nil {
		t.Fatal(err)
	}
	if v, err := keyring.Get(service, "printer"); err != nil || v != "new" {
		t.Fatalf("keychain = %q, %v", v, err)
	}
	if _, err := fileGet("printer"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the file copy should be gone: %v", err)
	}
}
