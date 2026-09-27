package link

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"

	"github.com/trevin-lee/prusactl/internal/appdir"
)

// ErrNotConfigured means no printer has been set up for direct access.
var ErrNotConfigured = errors.New("no printer set up for direct access: run `prusactl setup` in a terminal")

// Auth modes PrusaLink supports.
const (
	AuthDigest = "digest"  // username + password from the printer's screen
	AuthAPIKey = "api-key" // X-Api-Key header
)

// Config is the saved printer address. The secret lives in the OS keychain.
type Config struct {
	Host string `json:"host"`           // e.g. http://192.168.8.162
	User string `json:"user,omitempty"` // Digest username, default "maker"
	Auth string `json:"auth,omitempty"` // AuthDigest (default) or AuthAPIKey
}

type fileFormat struct {
	Printer *Config `json:"printer,omitempty"`
}

// NormalizeHost turns "192.168.8.162" or "prusa.local:8080" into an origin.
func NormalizeHost(h string) (string, error) {
	h = strings.TrimSpace(h)
	if h == "" {
		return "", errors.New("empty printer address")
	}
	if !strings.Contains(h, "://") {
		h = "http://" + h
	}
	u, err := url.Parse(h)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("not a printer address: %q", h)
	}
	return u.Scheme + "://" + u.Host, nil
}

func configPath() (string, error) {
	if p := os.Getenv("PRUSACTL_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := appdir.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// LoadConfig reads the saved printer, applying PRUSACTL_HOST / PRUSACTL_USER /
// PRUSACTL_AUTH overrides. It returns ErrNotConfigured if there is none.
func LoadConfig() (Config, error) {
	var cfg Config
	path, err := configPath()
	if err != nil {
		return cfg, err
	}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return cfg, err
	default:
		var f fileFormat
		if err := json.Unmarshal(b, &f); err != nil {
			return cfg, fmt.Errorf("reading %s: %w", path, err)
		}
		if f.Printer != nil {
			cfg = *f.Printer
		}
	}
	if v := os.Getenv("PRUSACTL_HOST"); v != "" {
		cfg.Host = v
	}
	if v := os.Getenv("PRUSACTL_USER"); v != "" {
		cfg.User = v
	}
	if v := os.Getenv("PRUSACTL_AUTH"); v != "" {
		cfg.Auth = v
	}
	if cfg.Host == "" {
		return cfg, ErrNotConfigured
	}
	if cfg.Host, err = NormalizeHost(cfg.Host); err != nil {
		return cfg, err
	}
	if cfg.User == "" {
		cfg.User = "maker"
	}
	if cfg.Auth == "" {
		cfg.Auth = AuthDigest
	}
	return cfg, nil
}

// SaveConfig writes the printer address (never the secret).
func SaveConfig(cfg Config) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(fileFormat{Printer: &cfg}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// RemoveConfig forgets the printer and its secret.
func RemoveConfig(cfg Config) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	errs := []error{}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	if err := keyring.Delete("prusactl", cfg.secretAccount()); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (cfg Config) secretAccount() string {
	if cfg.Auth == AuthAPIKey {
		return "printer-api-key:" + cfg.Host
	}
	return "printer-password:" + cfg.Host + ":" + cfg.User
}

// Secret returns the printer password or API key: PRUSACTL_PASSWORD /
// PRUSACTL_API_KEY if set, else the keychain.
func (cfg Config) Secret() (string, error) {
	env := "PRUSACTL_PASSWORD"
	if cfg.Auth == AuthAPIKey {
		env = "PRUSACTL_API_KEY"
	}
	if v := os.Getenv(env); v != "" {
		return v, nil
	}
	s, err := keyring.Get("prusactl", cfg.secretAccount())
	if errors.Is(err, keyring.ErrNotFound) {
		return "", fmt.Errorf("no saved password for %s: run `prusactl setup` again", cfg.Host)
	}
	return s, err
}

// SaveSecret stores the printer password or API key in the keychain.
func (cfg Config) SaveSecret(secret string) error {
	return keyring.Set("prusactl", cfg.secretAccount(), secret)
}
