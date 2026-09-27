// Package server exposes Prusa Connect as MCP tools.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/trevin-lee/prusactl/internal/auth"
	"github.com/trevin-lee/prusactl/internal/connect"
)

const instructions = `Controls the user's Prusa 3D printer(s) through Prusa Connect, with the same reach as the Connect web app.

Start with list_printers. Every printer argument accepts the printer's name or UUID, and may be omitted when the account has one printer.

Before anything physical (starting a print, moving axes, heating, marking the printer ready), check the printer's state with get_printer and, when a camera exists, look at get_camera_snapshot: the plate must be clear and nothing may be in the way. If the printer shows a dialog (dialog_info in get_printer), answer it with respond_to_dialog.

list_supported_commands shows every command this printer's firmware accepts, with arguments and the states it is allowed in; send_command runs any of them. api_request reaches any other part of the Connect API.

If a tool reports that you are not signed in, call login: it opens a browser window where the user signs in to their Prusa Account.`

// Server bundles the MCP server with its Connect client.
type Server struct {
	session *auth.Session
	client  *connect.Client
	mcp     *mcp.Server
}

// New builds the MCP server and registers every tool.
func New(session *auth.Session, client *connect.Client, version string) *Server {
	s := &Server{session: session, client: client}
	s.mcp = mcp.NewServer(
		&mcp.Implementation{Name: "prusactl", Title: "Prusa Connect", Version: version},
		&mcp.ServerOptions{Instructions: instructions},
	)
	s.addAuthTools()
	s.addPrinterTools()
	s.addControlTools()
	s.addFileTools()
	s.addJobTools()
	s.addAPITool()
	return s
}

// Run serves MCP over stdio until the client disconnects.
func (s *Server) Run(ctx context.Context) error {
	return s.mcp.Run(ctx, &mcp.StdioTransport{})
}

// Annotation presets.
func readOnly(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true}
}

func mutating(title string, destructive bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, DestructiveHint: &destructive}
}

// jsonResult returns v as compact JSON text.
func jsonResult(v any) (*mcp.CallToolResult, any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil, nil
}

func textResult(format string, args ...any) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}}}, nil, nil
}

// printerRef is embedded in every per-printer tool input.
type printerRef struct {
	Printer string `json:"printer,omitempty" jsonschema:"printer name or UUID; optional when the account has exactly one printer"`
}

// printerSummary is the subset of a Connect printer record needed to address it.
type printerSummary struct {
	UUID         string `json:"uuid"`
	Name         string `json:"name"`
	TeamID       int64  `json:"team_id"`
	ConnectState string `json:"connect_state"`
	PrinterState string `json:"printer_state"`
}

func (s *Server) listPrinters(ctx context.Context) ([]json.RawMessage, error) {
	var page struct {
		Printers []json.RawMessage `json:"printers"`
	}
	if err := s.client.Get(ctx, "/app/printers", nil, &page); err != nil {
		return nil, err
	}
	return page.Printers, nil
}

// resolvePrinter maps a name or UUID (or nothing, with a single printer) to
// the printer's record.
func (s *Server) resolvePrinter(ctx context.Context, ref string) (printerSummary, error) {
	raws, err := s.listPrinters(ctx)
	if err != nil {
		return printerSummary{}, err
	}
	printers := make([]printerSummary, 0, len(raws))
	for _, r := range raws {
		var p printerSummary
		if err := json.Unmarshal(r, &p); err == nil && p.UUID != "" {
			printers = append(printers, p)
		}
	}
	if len(printers) == 0 {
		return printerSummary{}, errors.New("this Prusa Connect account has no printers; add one at connect.prusa3d.com first")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		if len(printers) == 1 {
			return printers[0], nil
		}
		return printerSummary{}, fmt.Errorf("the account has %d printers; pass printer as one of: %s", len(printers), names(printers))
	}
	for _, p := range printers {
		if strings.EqualFold(p.UUID, ref) || strings.EqualFold(p.Name, ref) {
			return p, nil
		}
	}
	var partial []printerSummary
	for _, p := range printers {
		if strings.Contains(strings.ToLower(p.Name), strings.ToLower(ref)) {
			partial = append(partial, p)
		}
	}
	if len(partial) == 1 {
		return partial[0], nil
	}
	return printerSummary{}, fmt.Errorf("no printer matches %q; printers: %s", ref, names(printers))
}

func names(ps []printerSummary) string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = fmt.Sprintf("%q (%s)", p.Name, p.UUID)
	}
	return strings.Join(out, ", ")
}

func printerPath(uuid string, rest ...string) string {
	p := "/app/printers/" + connect.PathEscape(uuid)
	for _, r := range rest {
		p += "/" + r
	}
	return p
}
