package auth

import (
	"encoding/json"
	"errors"

	"github.com/trevin-lee/prusactl/internal/secret"
)

// Tokens live in the OS credential store, or a private file where there is none
// (see package secret). Access and refresh tokens are separate items because a
// single item holding two JWTs can exceed the size the macOS `security` tool
// accepts.
const (
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
	refresh, err := secret.Get(refreshAccount)
	if errors.Is(err, secret.ErrNotFound) {
		return nil, ErrNotLoggedIn
	}
	if err != nil {
		return nil, err
	}
	t := &Token{RefreshToken: refresh}
	if raw, err := secret.Get(accessAccount); err == nil {
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
	if err := secret.Set(refreshAccount, t.RefreshToken); err != nil {
		return err
	}
	cached, _ := json.Marshal(Token{AccessToken: t.AccessToken, Expiry: t.Expiry})
	if err := secret.Set(accessAccount, string(cached)); err != nil {
		// The access token is only a cache shared between processes; losing it
		// costs one extra refresh.
		_ = secret.Delete(accessAccount)
	}
	return nil
}

func (KeyringStore) Clear() error {
	var errs []error
	for _, acct := range []string{accessAccount, refreshAccount} {
		if err := secret.Delete(acct); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
