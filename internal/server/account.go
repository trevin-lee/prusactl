package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

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

// WhoAmI returns the signed-in Connect user.
func WhoAmI(ctx context.Context, c *connect.Client) (json.RawMessage, error) {
	var me json.RawMessage
	if err := c.Get(ctx, "/app/login", nil, &me); err != nil {
		return nil, err
	}
	return me, nil
}
