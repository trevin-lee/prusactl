// Package secret keeps prusactl's credentials: the printer password or API key
// and the Prusa Connect session.
//
// They go in the OS credential store (macOS Keychain, Secret Service on Linux,
// Credential Manager on Windows). A headless Linux machine such as a Raspberry
// Pi usually has no Secret Service, so when the store is unavailable, or
// PRUSACTL_KEYRING=file is set, they go in secrets.json in prusactl's config
// directory instead, readable only by the user.
package secret

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/zalando/go-keyring"

	"github.com/trevin-lee/prusactl/internal/appdir"
)

const service = "prusactl"

// ErrNotFound means nothing is stored under the account.
var ErrNotFound = errors.New("secret not found")

var (
	// filePath is replaced in tests.
	filePath = func() (string, error) {
		dir, err := appdir.Dir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "secrets.json"), nil
	}
	mu       sync.Mutex
	warned   bool
	usedFile bool // a credential was saved to the file this run
)

func forceFile() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("PRUSACTL_KEYRING")), "file")
}

// goos is replaced in tests.
var goos = runtime.GOOS

// noBackend matches the errors go-keyring gives on Linux and BSD when there is
// no Secret Service at all: no D-Bus session, no dbus-launch, or nothing
// providing org.freedesktop.secrets.
var noBackend = regexp.MustCompile(`(?i)dbus|org\.freedesktop\.secrets|ServiceUnknown|session bus|executable file not found`)

// unavailable reports whether a keychain error means this machine has no
// credential store. It is deliberately narrow: a keychain that exists but
// refused (the user clicked Deny, or it is locked) must surface as an error,
// not quietly send the secret to a file. macOS and Windows always have one.
func unavailable(err error) bool {
	if err == nil || errors.Is(err, keyring.ErrNotFound) || errors.Is(err, keyring.ErrSetDataTooBig) {
		return false
	}
	if goos == "darwin" || goos == "windows" {
		return false
	}
	return noBackend.MatchString(err.Error())
}

// Get returns the secret stored under account, or ErrNotFound.
func Get(account string) (string, error) {
	if !forceFile() {
		v, err := keyring.Get(service, account)
		if err == nil {
			return v, nil
		}
		if !errors.Is(err, keyring.ErrNotFound) && !unavailable(err) {
			return "", err
		}
		// Not in the keychain, or no keychain: it may be in the file.
	}
	return fileGet(account)
}

// Set stores secret under account.
func Set(account, secret string) error {
	if !forceFile() {
		err := keyring.Set(service, account, secret)
		if err == nil {
			// Don't leave an older copy behind in the file.
			if ferr := fileDelete(account); ferr != nil && !errors.Is(ferr, ErrNotFound) {
				return ferr
			}
			return nil
		}
		if !unavailable(err) {
			return err
		}
		warnFallback(err)
	}
	return fileSet(account, secret)
}

// Delete removes account from wherever it is stored. It is not an error if
// nothing was stored.
func Delete(account string) error {
	var errs []error
	if !forceFile() {
		if err := keyring.Delete(service, account); err != nil && !errors.Is(err, keyring.ErrNotFound) && !unavailable(err) {
			errs = append(errs, err)
		}
	}
	if err := fileDelete(account); err != nil && !errors.Is(err, ErrNotFound) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func warnFallback(err error) {
	mu.Lock()
	defer mu.Unlock()
	if warned {
		return
	}
	warned = true
	path, _ := filePath()
	fmt.Fprintf(os.Stderr, "prusactl: no system keychain available (%v); keeping credentials in %s, readable only by you\n", err, path)
}

func readFile() (map[string]string, string, error) {
	path, err := filePath()
	if err != nil {
		return nil, "", err
	}
	m := map[string]string{}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return m, path, nil
	}
	if err != nil {
		return nil, "", err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, "", fmt.Errorf("%s is damaged (%v); delete it and run `prusactl setup` again", path, err)
	}
	return m, path, nil
}

func writeFile(path string, m map[string]string) error {
	if len(m) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".secrets-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func fileGet(account string) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	m, _, err := readFile()
	if err != nil {
		return "", err
	}
	v, ok := m[account]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func fileSet(account, secret string) error {
	mu.Lock()
	defer mu.Unlock()
	m, path, err := readFile()
	if err != nil {
		return err
	}
	m[account] = secret
	usedFile = true
	return writeFile(path, m)
}

func fileDelete(account string) error {
	mu.Lock()
	defer mu.Unlock()
	m, path, err := readFile()
	if err != nil {
		return err
	}
	if _, ok := m[account]; !ok {
		return ErrNotFound
	}
	delete(m, account)
	return writeFile(path, m)
}

// Where describes where the last credential was saved, for messages such as
// "The password is saved in …".
func Where() string {
	mu.Lock()
	file := usedFile
	mu.Unlock()
	if file || forceFile() {
		if path, err := filePath(); err == nil {
			return path
		}
	}
	return "your keychain"
}
