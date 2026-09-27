package auth

import (
	"encoding/json"
	"errors"

	"github.com/zalando/go-keyring"
)

// Tokens live in the OS credential store (macOS Keychain, Secret Service on
// Linux, Credential Manager on Windows). Access and refresh tokens are
// separate items because a single item holding two JWTs can exceed the size
// the macOS `security` tool accepts.
const (
	keyringService = "prusactl"
	accessAccount  = "connect-access-token"
	refreshAccount = "connect-refresh-token"
)

// Store persists tokens.
type Store interface {
	Load() (*Token, error) // ErrNotLoggedIn if nothing is stored
	Save(*Token) error
	Clear() error
}

// KeyringStore is the default Store.
type KeyringStore struct{}

func (KeyringStore) Load() (*Token, error) {
	refresh, err := keyring.Get(keyringService, refreshAccount)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, ErrNotLoggedIn
	}
	if err != nil {
		return nil, err
	}
	t := &Token{RefreshToken: refresh}
	if raw, err := keyring.Get(keyringService, accessAccount); err == nil {
		var cached Token
		if json.Unmarshal([]byte(raw), &cached) == nil {
			t.AccessToken, t.Expiry = cached.AccessToken, cached.Expiry
		}
	}
	return t, nil
}

func (KeyringStore) Save(t *Token) error {
	if t.RefreshToken == "" {
		return errors.New("refusing to store a session without a refresh token")
	}
	if err := keyring.Set(keyringService, refreshAccount, t.RefreshToken); err != nil {
		return err
	}
	cached, _ := json.Marshal(Token{AccessToken: t.AccessToken, Expiry: t.Expiry})
	if err := keyring.Set(keyringService, accessAccount, string(cached)); err != nil {
		// The access token is only a cache shared between processes; losing it
		// costs one extra refresh.
		_ = keyring.Delete(keyringService, accessAccount)
	}
	return nil
}

func (KeyringStore) Clear() error {
	var errs []error
	for _, acct := range []string{accessAccount, refreshAccount} {
		if err := keyring.Delete(keyringService, acct); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
