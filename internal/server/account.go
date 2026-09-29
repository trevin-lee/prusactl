package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/trevin-lee/prusactl/internal/connect"
)

// RegisterUser mirrors the Connect web app's first call after sign-in, which
// creates the Connect user record for a Prusa Account on first use.
func RegisterUser(ctx context.Context, c *connect.Client) error {
	err := c.JSON(ctx, connect.Request{Method: http.MethodPost, Path: "/app/register"}, nil)
	if err != nil && !connect.IsStatus(err, http.StatusConflict) { // 409: already registered
		return fmt.Errorf("registering with Prusa Connect: %w", err)
	}
	return nil
}

// Account identifies the signed-in Prusa Account: who is signed in and which
// team's printers they'll reach. Connect's record also carries the email
// address, account id, terms-acceptance dates and organization ids, which
// nothing here needs, so they are left out rather than handed to a model.
type Account struct {
	PublicName    string `json:"public_name,omitempty"`
	Name          string `json:"name,omitempty"`
	DefaultTeamID int64  `json:"default_team_id,omitempty"`
	TeamName      string `json:"team_name,omitempty"`
}

// WhoAmI returns who is signed in to Prusa Connect.
func WhoAmI(ctx context.Context, c *connect.Client) (*Account, error) {
	var me struct {
		User struct {
			PublicName    string `json:"public_name"`
			FirstName     string `json:"first_name"`
			LastName      string `json:"last_name"`
			DefaultTeamID int64  `json:"default_team_id"`
			Teams         []struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"teams"`
		} `json:"user"`
	}
	if err := c.Get(ctx, "/app/login", nil, &me); err != nil {
		return nil, err
	}
	u := me.User
	acct := &Account{
		PublicName:    u.PublicName,
		Name:          strings.TrimSpace(u.FirstName + " " + u.LastName),
		DefaultTeamID: u.DefaultTeamID,
	}
	for _, t := range u.Teams {
		if t.ID == u.DefaultTeamID {
			acct.TeamName = t.Name
		}
	}
	return acct, nil
}
