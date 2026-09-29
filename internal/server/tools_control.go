package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/trevin-lee/prusactl/internal/connect"
	"github.com/trevin-lee/prusactl/internal/link"
)

// supportedCommand is one entry of /app/printers/{uuid}/supported-commands.
// Entries with an ID are G-code snippets from the team's library rather than
// built-in commands.
type supportedCommand struct {
	Command             string          `json:"command"`
	ID                  json.RawMessage `json:"id,omitempty"`
	Description         string          `json:"description,omitempty"`
	ExecutableFromState []string        `json:"executable_from_state,omitempty"`
	Args                json.RawMessage `json:"args,omitempty"`
}

func (s *Server) supportedCommands(ctx context.Context, uuid string) ([]supportedCommand, error) {
	var resp struct {
		Commands *[]supportedCommand `json:"commands"`
	}
	if err := s.connect.Get(ctx, printerPath(uuid, "supported-commands"), nil, &resp); err != nil {
		return nil, err
	}
	if resp.Commands == nil {
		return nil, connect.Missing("GET", printerPath(uuid, "supported-commands"), "commands")
	}
	return *resp.Commands, nil
}

// printerState reads the printer's current state as Connect reports it.
func (s *Server) printerDetail(ctx context.Context, uuid string) (map[string]any, error) {
	var p map[string]any
	if err := s.connect.Get(ctx, printerPath(uuid), nil, &p); err != nil {
		return nil, err
	}
	return p, nil
}

func stateOf(p map[string]any) string {
	if st, ok := p["connect_state"].(string); ok && st != "" {
		return st
	}
	st, _ := p["printer_state"].(string)
	return st
}

// connectState is stateOf for decisions that depend on the state: a record
// without one means the format changed, and guessing would skip the checks.
func connectState(p map[string]any, uuid string) (string, error) {
	if st := stateOf(p); st != "" {
		return st, nil
	}
	return "", connect.Missing("GET", printerPath(uuid), "connect_state")
}

type commandResult struct {
	Printer  string          `json:"printer"`
	Command  string          `json:"command"`
	Kwargs   map[string]any  `json:"kwargs"`
	Response json.RawMessage `json:"response"`
}

// runCommand validates a command against what the printer supports and its
// current state, then sends it. Synchronous commands wait for the printer to
// acknowledge; asynchronous ones return the queued command record.
func (s *Server) runCommand(ctx context.Context, p printerSummary, command string, kwargs map[string]any, async bool, timeout int) (*commandResult, error) {
	command = strings.ToUpper(strings.TrimSpace(command))
	if kwargs == nil {
		kwargs = map[string]any{}
	}
	cmds, err := s.supportedCommands(ctx, p.UUID)
	if err != nil {
		return nil, err
	}
	var match *supportedCommand
	for i := range cmds {
		if strings.EqualFold(cmds[i].Command, command) {
			match = &cmds[i]
			break
		}
	}
	if match == nil {
		avail := make([]string, 0, len(cmds))
		for _, c := range cmds {
			avail = append(avail, c.Command)
		}
		sort.Strings(avail)
		return nil, fmt.Errorf("%s does not support %s; supported: %s", p.Name, command, strings.Join(avail, ", "))
	}
	if len(match.ExecutableFromState) > 0 {
		detail, err := s.printerDetail(ctx, p.UUID)
		if err != nil {
			return nil, err
		}
		st, err := connectState(detail, p.UUID)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(match.ExecutableFromState, st) {
			return nil, fmt.Errorf("%s can't run %s while %s; allowed in: %s",
				p.Name, command, st, strings.Join(match.ExecutableFromState, ", "))
		}
	}

	body := map[string]any{"kwargs": kwargs}
	path := printerPath(p.UUID, "commands")
	q := url.Values{}
	if len(match.ID) > 0 && string(match.ID) != "null" {
		// Library G-code snippets are addressed by id and only run queued.
		body["gcode_id"] = match.ID
		async = true
	} else {
		body["command"] = match.Command
	}
	if !async {
		path += "/sync"
		if timeout > 0 {
			q.Set("timeout", strconv.Itoa(timeout))
		}
	}
	var resp json.RawMessage
	if err := s.connect.JSON(ctx, connect.Request{Method: http.MethodPost, Path: path, Query: q, JSON: body}, &resp); err != nil {
		return nil, err
	}
	return &commandResult{Printer: p.Name, Command: match.Command, Kwargs: kwargs, Response: resp}, nil
}

type listCommandsInput struct {
	printerRef
	ExecutableNow bool `json:"executable_now,omitempty" jsonschema:"only list commands the printer accepts in its current state"`
}

type sendCommandInput struct {
	printerRef
	plateConfirmation
	Command        string         `json:"command" jsonschema:"command name exactly as list_supported_commands shows it, e.g. HOME, MOVE, SET_NOZZLE_TEMPERATURE"`
	Kwargs         map[string]any `json:"kwargs,omitempty" jsonschema:"command arguments by name, typed as list_supported_commands describes them"`
	Async          bool           `json:"async,omitempty" jsonschema:"queue the command and return immediately instead of waiting for the printer to acknowledge it"`
	TimeoutSeconds int            `json:"timeout_seconds,omitempty" jsonschema:"for synchronous commands, how long Connect should wait for the printer"`
}

type controlPrintInput struct {
	printerRef
	Action string `json:"action" jsonschema:"pause, resume, or stop"`
}

type gcodeInput struct {
	printerRef
	plateConfirmation
	Gcode string `json:"gcode" jsonschema:"G-code lines to run, e.g. \"G28\\nM104 S215\""`
}

// macroPath is where run_gcode puts its one-off job.
const macroPath = "/usb/prusactl-macro.gcode"

type dialogInput struct {
	printerRef
	Button string `json:"button" jsonschema:"label of the button to press, exactly as get_printer shows it in dialog_info.buttons"`
}

type getCommandInput struct {
	printerRef
	CommandID int64 `json:"command_id" jsonschema:"id returned by an async send_command"`
}

func (s *Server) addControlTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "list_supported_commands",
		Description: "List every command this printer's firmware accepts through Connect: its arguments (name, type, unit, " +
			"default, required) and the printer states it may run in (executable_from_state). This is the full control " +
			"surface of the printer: movement, homing, temperatures, fans, speed/flow, filament load/unload, mesh bed " +
			"leveling, file and folder management, printer-ready flags, resets, and G-code snippets from the team library.",
		Annotations: readOnly("List printer commands"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listCommandsInput) (*mcp.CallToolResult, any, error) {
		p, err := s.connectPrinter(ctx, in.printerRef)
		if err != nil {
			return nil, nil, err
		}
		cmds, err := s.supportedCommands(ctx, p.UUID)
		if err != nil {
			return nil, nil, err
		}
		state := p.ConnectState
		if in.ExecutableNow {
			kept := cmds[:0]
			for _, c := range cmds {
				if len(c.ExecutableFromState) == 0 || slices.Contains(c.ExecutableFromState, state) {
					kept = append(kept, c)
				}
			}
			cmds = kept
		}
		sort.Slice(cmds, func(i, j int) bool { return cmds[i].Command < cmds[j].Command })
		out := map[string]any{"printer": p.Name, "state": state, "commands": cmds}
		if state == "OFFLINE" {
			out["note"] = "the printer is offline in Prusa Connect, so no command can reach it through Connect right now"
		}
		return jsonResult(out)
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "send_command",
		Description: "Run any command from list_supported_commands on the printer, exactly as the Connect web app's controls do. " +
			"The command and state are checked against the printer's supported-command list first. By default waits for the " +
			"printer to acknowledge. Physical commands (MOVE, HOME, heating, filament, MESH_BED_LEVELING) act on real hardware: " +
			"check get_printer and the camera first. After a finished or stopped print, START_PRINT, HOME, MOVE, MOVE_Z and " +
			"MESH_BED_LEVELING also need plate_clear=true, as start_print does. SET_PRINTER_READY tells Connect the plate is " +
			"clear, so it is itself that confirmation: send it only after looking.",
		Annotations: mutating("Send printer command", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in sendCommandInput) (*mcp.CallToolResult, any, error) {
		p, err := s.connectPrinter(ctx, in.printerRef)
		if err != nil {
			return nil, nil, err
		}
		if needsPlateCheck(in.Command) {
			if err := s.plateCheck(ctx, target{name: p.Name, connect: p}, in.PlateClear); err != nil {
				return nil, nil, err
			}
		}
		res, err := s.runCommand(ctx, p, in.Command, in.Kwargs, in.Async, in.TimeoutSeconds)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(res)
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "get_command",
		Description: "Check the state of a command sent with send_command async=true.",
		Annotations: readOnly("Get command status"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getCommandInput) (*mcp.CallToolResult, any, error) {
		p, err := s.connectPrinter(ctx, in.printerRef)
		if err != nil {
			return nil, nil, err
		}
		var out json.RawMessage
		if err := s.connect.Get(ctx, printerPath(p.UUID, "commands", strconv.FormatInt(in.CommandID, 10)), nil, &out); err != nil {
			return nil, nil, err
		}
		return jsonResult(out)
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "control_print",
		Description: "Pause, resume, or stop the current print. A question on the printer's screen (state ATTENTION) " +
			"can't be answered this way; use respond_to_dialog through Prusa Connect. Stopping is final: the job can't " +
			"be resumed and the part stays on the plate.",
		Annotations: mutating("Pause/resume/stop print", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in controlPrintInput) (*mcp.CallToolResult, any, error) {
		action := strings.ToLower(strings.TrimSpace(in.Action))
		switch action {
		case "pause", "resume", "stop":
		default:
			return nil, nil, actionError(in.Action)
		}
		t, err := s.route(ctx, in.printerRef)
		if err != nil {
			return nil, nil, err
		}
		if t.direct {
			var job struct {
				ID    int64  `json:"id"`
				State string `json:"state"`
			}
			found, err := s.direct().Get(ctx, "/api/v1/job", &job)
			if err != nil {
				return nil, nil, err
			}
			if !found {
				return nil, nil, fmt.Errorf("%s has no job to %s", t.name, action)
			}
			id := strconv.FormatInt(job.ID, 10)
			// Buddy firmware's PrusaLink implements pause, resume (from PAUSED
			// only) and stop; the spec's "continue" is answered with 400.
			req := link.Request{Method: http.MethodPut, Path: "/api/v1/job/" + id + "/" + action}
			switch action {
			case "pause", "resume":
				if job.State == "ATTENTION" || (action != "pause" && job.State != "PAUSED") {
					return nil, nil, fmt.Errorf("%s's job is %s; the printer only resumes from PAUSED over the local network. A question on its screen must be answered there, or through Prusa Connect (respond_to_dialog)", t.name, job.State)
				}
			case "stop":
				req = link.Request{Method: http.MethodDelete, Path: "/api/v1/job/" + id}
			default:
				return nil, nil, actionError(in.Action)
			}
			if _, err := s.direct().JSON(ctx, req, nil); err != nil {
				return nil, nil, err
			}
			return jsonResult(withVia(t, map[string]any{"action": action, "job_id": job.ID, "state_before": job.State}))
		}
		cmd := map[string]string{"pause": "PAUSE_PRINT", "resume": "RESUME_PRINT", "stop": "STOP_PRINT"}[action]
		if cmd == "" {
			return nil, nil, actionError(in.Action)
		}
		res, err := s.runCommand(ctx, t.connect, cmd, nil, false, 0)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(withVia(t, map[string]any{"action": action, "result": res}))
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "run_gcode",
		Description: "Run G-code on the printer over the direct connection: prusactl uploads it as a tiny print " +
			"job (/usb/prusactl-macro.gcode) and starts it, so it only works while the printer is idle (not " +
			"printing or paused). This is how to heat (M104/M140/M109/M190), home (G28), move (G90/G91 + G1), " +
			"load/unload filament (M701/M702), level (G29), or send any other command when Prusa Connect isn't " +
			"available. It shows up on the printer and in history as a short print. Moves act on real hardware: " +
			"make sure nothing is in the way.",
		Annotations: mutating("Run G-code", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gcodeInput) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(in.Gcode) == "" {
			return nil, nil, fmt.Errorf("gcode is empty")
		}
		if err := checkVia(in.printerRef, "direct"); err != nil {
			return nil, nil, err
		}
		in.Via = "direct"
		t, err := s.route(ctx, in.printerRef)
		if err != nil {
			return nil, nil, fmt.Errorf("run_gcode needs the direct connection: %w", err)
		}
		if err := s.readyToStart(ctx, t, in.PlateClear); err != nil {
			return nil, nil, err
		}
		// Without this header line Buddy firmware asks, on the printer's screen, whether a
		// file not sliced with input shaping should run, and waits there. Slicer output
		// always carries it.
		body := []byte("; prusactl macro\nM862.6 P\"Input shaper\" ; FW feature check\n" + strings.TrimSpace(in.Gcode) + "\n")
		path, err := link.FilePath(macroPath)
		if err != nil {
			return nil, nil, err
		}
		_, err = s.direct().JSON(ctx, link.Request{
			Method:        http.MethodPut,
			Path:          path,
			Body:          func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil },
			ContentLength: int64(len(body)),
			ContentType:   "application/octet-stream",
			Header:        http.Header{"Overwrite": {"?1"}, "Print-After-Upload": {"?1"}},
		}, nil)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(withVia(t, map[string]any{"started": macroPath, "lines": strings.Count(strings.TrimSpace(in.Gcode), "\n") + 1,
			"next": "check get_printer until the state leaves PRINTING"}))
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "respond_to_dialog",
		Description: "Press a button on the dialog currently shown on the printer's screen (filament runout, " +
			"errors, confirmations, \"remove the print\" prompts, etc.), the way the Connect web app does remotely. " +
			"get_printer shows the open dialog in dialog_info with its text and buttons.",
		Annotations: mutating("Answer printer dialog", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in dialogInput) (*mcp.CallToolResult, any, error) {
		p, err := s.connectPrinter(ctx, in.printerRef)
		if err != nil {
			return nil, nil, err
		}
		detail, err := s.printerDetail(ctx, p.UUID)
		if err != nil {
			return nil, nil, err
		}
		dialog, _ := detail["dialog_info"].(map[string]any)
		if dialog == nil {
			return nil, nil, fmt.Errorf("%s has no dialog open", p.Name)
		}
		var buttons []string
		if bs, ok := dialog["buttons"].([]any); ok {
			for _, b := range bs {
				buttons = append(buttons, fmt.Sprint(b))
			}
		}
		button := ""
		for _, b := range buttons {
			if strings.EqualFold(b, strings.TrimSpace(in.Button)) {
				button = b
			}
		}
		if button == "" {
			return nil, nil, fmt.Errorf("the open dialog has no %q button; buttons: %q", in.Button, buttons)
		}
		res, err := s.runCommand(ctx, p, "DIALOG_ACTION", map[string]any{"dialog_id": dialog["id"], "button": button}, false, 0)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(map[string]any{"dialog": dialog, "pressed": button, "result": res})
	})
}

// actionError explains control_print's action, naming "continue" only to point
// at the action that replaces it.
func actionError(got string) error {
	if strings.EqualFold(strings.TrimSpace(got), "continue") {
		return fmt.Errorf("action must be pause, resume, or stop; to carry on a paused print use resume")
	}
	return fmt.Errorf("action must be pause, resume, or stop (got %q)", got)
}
