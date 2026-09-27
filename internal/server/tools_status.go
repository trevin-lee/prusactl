package server

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/trevin-lee/prusactl/internal/auth"
	"github.com/trevin-lee/prusactl/internal/link"
)

// Status reports how the printer can be reached, for the CLI and MCP alike.
func (s *Server) Status(ctx context.Context) map[string]any {
	direct := map[string]any{"configured": s.link != nil}
	switch {
	case s.link != nil:
		direct["host"] = s.link.Config.Host
		if info, err := s.probeLink(ctx); err != nil {
			direct["reachable"], direct["error"] = false, err.Error()
		} else {
			direct["reachable"], direct["printer"] = true, info
		}
	case errors.Is(s.linkErr, link.ErrNotConfigured):
		direct["setup"] = "run `prusactl setup` in a terminal"
	case s.linkErr != nil:
		direct["error"] = s.linkErr.Error()
	}

	cloud := map[string]any{"signed_in": false}
	if s.session.SignedIn() {
		me, err := WhoAmI(ctx, s.connect)
		switch {
		case errors.Is(err, auth.ErrNotLoggedIn):
			cloud["error"] = err.Error()
		case err != nil:
			cloud["signed_in"], cloud["error"] = true, err.Error()
		default:
			cloud["signed_in"], cloud["user"] = true, me
		}
	}
	if cloud["signed_in"] != true {
		cloud["setup"] = "optional: run `prusactl login` in a terminal for remote access, camera, dialogs, queue and history"
	}
	return map[string]any{"direct": direct, "connect": cloud}
}

func (s *Server) addStatusTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "connection_status",
		Description: "How the printer can be reached right now: directly on the local network (PrusaLink) " +
			"and/or through Prusa Connect, and which account is signed in. Says what to set up if neither works.",
		Annotations: readOnly("Connection status"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return jsonResult(s.Status(ctx))
	})
}
