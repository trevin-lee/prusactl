package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/trevin-lee/prusactl/internal/connect"
)

type apiInput struct {
	Method string            `json:"method,omitempty" jsonschema:"GET, POST, PUT, PATCH or DELETE; default GET"`
	Path   string            `json:"path" jsonschema:"API path starting with /app/, e.g. /app/printers/{uuid}/events"`
	Query  map[string]string `json:"query,omitempty" jsonschema:"query-string parameters"`
	Body   any               `json:"body,omitempty" jsonschema:"JSON request body"`
}

func (s *Server) addAPITool() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "api_request",
		Description: "Call any Prusa Connect web API endpoint as the signed-in user, for anything the other tools don't " +
			"cover (printer settings via PATCH /app/printers/{uuid}, groups, teams, notifications, statistics under " +
			"/app/stats/printers/{uuid}/..., firmware, Connect file storage under /app/teams/{team_id}/files). The API is the " +
			"one connect.prusa3d.com itself uses. Prefer the dedicated tools when one fits.",
		Annotations: mutating("Raw Connect API call", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in apiInput) (*mcp.CallToolResult, any, error) {
		method := strings.ToUpper(strings.TrimSpace(in.Method))
		if method == "" {
			method = http.MethodGet
		}
		switch method {
		case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		default:
			return nil, nil, fmt.Errorf("unsupported method %q", in.Method)
		}
		u, err := url.Parse(strings.TrimSpace(in.Path))
		if err != nil {
			return nil, nil, err
		}
		// Only relative /app/ paths: the bearer token must never be sent elsewhere.
		if u.Scheme != "" || u.Host != "" || !strings.HasPrefix(u.Path, "/app/") || strings.Contains(u.Path, "..") {
			return nil, nil, fmt.Errorf("path must be a Connect API path starting with /app/ (got %q)", in.Path)
		}
		q := u.Query()
		for k, v := range in.Query {
			q.Set(k, v)
		}
		req := connect.Request{Method: method, Path: u.Path, Query: q}
		if in.Body != nil {
			req.JSON = in.Body
		}
		resp, err := s.client.Do(ctx, req)
		if err != nil {
			return nil, nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			return nil, nil, err
		}
		ct := resp.Header.Get("Content-Type")
		switch {
		case strings.HasPrefix(ct, "image/"):
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{Data: body, MIMEType: ct}}}, nil, nil
		case json.Valid(body):
			return jsonResult(map[string]any{"status": resp.StatusCode, "body": json.RawMessage(body)})
		case utf8.Valid(body):
			return jsonResult(map[string]any{"status": resp.StatusCode, "content_type": ct, "body": string(body)})
		default:
			return jsonResult(map[string]any{"status": resp.StatusCode, "content_type": ct, "bytes": len(body)})
		}
	})
}
