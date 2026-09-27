package server

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestEveryToolResultIsMasked(t *testing.T) {
	// A tool that passes Connect's printer record straight through.
	res, _, err := jsonResult(map[string]any{
		"printer": map[string]any{"name": "Core One", "prusalink_api_key": "def456"},
		"events":  []any{map[string]any{"camera": map[string]any{"token": "cam-token"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if strings.Contains(text, "def456") || strings.Contains(text, "cam-token") || !strings.Contains(text, "Core One") {
		t.Fatalf("result not masked correctly: %s", text)
	}
}
