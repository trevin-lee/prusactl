package server

import (
	"context"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/trevin-lee/prusactl/internal/auth"
)

type loginInput struct {
	Force          bool `json:"force,omitempty" jsonschema:"sign in again even if a working session is saved (e.g. to switch accounts)"`
	TimeoutMinutes int  `json:"timeout_minutes,omitempty" jsonschema:"how long to wait for the user to finish signing in; default 10"`
}

func (s *Server) addAuthTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "login",
		Description: "Sign in to Prusa Connect. Opens a browser window on Prusa's own sign-in page on the user's computer and waits " +
			"for them to finish; the password never passes through this tool. The session is saved in the OS keychain and " +
			"renews itself, so this is only needed once, or when another tool reports that you are not signed in. " +
			"Tell the user a sign-in window is opening before calling this.",
		Annotations: mutating("Sign in to Prusa Connect", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in loginInput) (*mcp.CallToolResult, any, error) {
		if !in.Force && s.session.SignedIn() {
			if me, err := WhoAmI(ctx, s.client); err == nil {
				return jsonResult(map[string]any{"status": "already signed in", "user": me})
			} else if !errors.Is(err, auth.ErrNotLoggedIn) {
				return nil, nil, err
			}
		}
		timeout := 10 * time.Minute
		if in.TimeoutMinutes > 0 {
			timeout = time.Duration(in.TimeoutMinutes) * time.Minute
		}
		if _, err := s.session.Login(ctx, timeout); err != nil {
			return nil, nil, err
		}
		regErr := RegisterUser(ctx, s.client)
		me, err := WhoAmI(ctx, s.client)
		if err != nil {
			if regErr != nil {
				err = errors.Join(err, regErr)
			}
			return nil, nil, err
		}
		return jsonResult(map[string]any{"status": "signed in", "user": me})
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "logout",
		Description: "Forget the saved Prusa Connect session (removes the tokens from the keychain). The printer is unaffected.",
		Annotations: mutating("Sign out", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		if err := s.session.Logout(); err != nil {
			return nil, nil, err
		}
		return textResult("Signed out. Call login to sign in again.")
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "auth_status",
		Description: "Report whether a Prusa Connect session is saved and which account it belongs to.",
		Annotations: readOnly("Sign-in status"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		if !s.session.SignedIn() {
			return jsonResult(map[string]any{"signed_in": false, "hint": "call login"})
		}
		me, err := WhoAmI(ctx, s.client)
		if errors.Is(err, auth.ErrNotLoggedIn) {
			return jsonResult(map[string]any{"signed_in": false, "reason": err.Error(), "hint": "call login"})
		}
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(map[string]any{"signed_in": true, "user": me})
	})
}
