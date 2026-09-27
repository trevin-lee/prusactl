package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/trevin-lee/prusactl/internal/connect"
)

// summaryKeys are the printer fields worth showing in a list; get_printer
// returns everything.
var summaryKeys = []string{
	"uuid", "name", "location", "team_id", "team_name", "printer_type_name", "printer_model",
	"connect_state", "printer_state", "temp", "chamber", "filament", "nozzle_diameter",
	"speed", "flow", "job_info", "dialog_info", "is_online", "last_online", "firmware",
}

type snapshotInput struct {
	printerRef
	CameraID string `json:"camera_id,omitempty" jsonschema:"camera to use when the printer has several; default is the first"`
}

type eventsInput struct {
	printerRef
	Limit  int `json:"limit,omitempty" jsonschema:"number of events, newest first; default 20"`
	Offset int `json:"offset,omitempty"`
}

type telemetryInput struct {
	printerRef
	Minutes     int `json:"minutes,omitempty" jsonschema:"how far back to go; default 30"`
	Granularity int `json:"granularity,omitempty" jsonschema:"seconds between samples; default 15"`
}

func (s *Server) addPrinterTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "list_printers",
		Description: "List the printers on this Prusa Connect account with their state, temperatures, filament, " +
			"current job progress, and any dialog waiting on the printer's screen.",
		Annotations: readOnly("List printers"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		raws, err := s.listPrinters(ctx)
		if err != nil {
			return nil, nil, err
		}
		out := make([]map[string]any, 0, len(raws))
		for _, r := range raws {
			var full map[string]any
			if err := json.Unmarshal(r, &full); err != nil {
				continue
			}
			sum := map[string]any{}
			for _, k := range summaryKeys {
				if v, ok := full[k]; ok && v != nil {
					sum[k] = v
				}
			}
			out = append(out, sum)
		}
		return jsonResult(map[string]any{"printers": out})
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "get_printer",
		Description: "Full live status of one printer as Connect reports it: state, temperatures and targets, axis " +
			"positions, fans, speed/flow, filament, nozzle, current job (progress, time remaining, file), the dialog " +
			"currently on the printer's screen (dialog_info, answer it with respond_to_dialog), storage, and settings.",
		Annotations: readOnly("Get printer status"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in printerRef) (*mcp.CallToolResult, any, error) {
		p, err := s.resolvePrinter(ctx, in.Printer)
		if err != nil {
			return nil, nil, err
		}
		var out json.RawMessage
		if err := s.client.Get(ctx, printerPath(p.UUID), nil, &out); err != nil {
			return nil, nil, err
		}
		return jsonResult(out)
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "get_camera_snapshot",
		Description: "The latest image from a camera attached to the printer in Prusa Connect, so you can see the " +
			"print, the bed, and the nozzle. Use it before starting a print or moving anything (is the plate clear?) and " +
			"to watch a print for failures. Reports how old the image is.",
		Annotations: readOnly("Camera snapshot"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in snapshotInput) (*mcp.CallToolResult, any, error) {
		p, err := s.resolvePrinter(ctx, in.Printer)
		if err != nil {
			return nil, nil, err
		}
		var cams struct {
			Cameras []map[string]any `json:"cameras"`
		}
		if err := s.client.Get(ctx, printerPath(p.UUID, "cameras"), nil, &cams); err != nil {
			return nil, nil, err
		}
		if len(cams.Cameras) == 0 {
			return nil, nil, fmt.Errorf("%s has no camera in Prusa Connect", p.Name)
		}
		cam := cams.Cameras[0]
		if in.CameraID != "" {
			cam = nil
			for _, c := range cams.Cameras {
				if fmt.Sprint(c["id"]) == in.CameraID || fmt.Sprint(c["name"]) == in.CameraID {
					cam = c
				}
			}
			if cam == nil {
				return nil, nil, fmt.Errorf("no camera %q on %s", in.CameraID, p.Name)
			}
		}
		id := connect.PathEscape(jsonID(cam["id"]))
		query := url.Values{"printer_uuid": {p.UUID}}
		resp, err := s.client.Do(ctx, connect.Request{Method: http.MethodGet, Path: "/app/cameras/" + id + "/snapshots/last", Query: query})
		if connect.IsStatus(err, http.StatusNotFound) {
			// WebRTC cameras (Buddy3D) may never have pushed a full snapshot;
			// the web app falls back to the thumbnail endpoint.
			resp, err = s.client.Do(ctx, connect.Request{Method: http.MethodGet, Path: "/thumbnail/camera/" + id, Query: query})
		}
		if err != nil {
			return nil, nil, err
		}
		defer resp.Body.Close()
		img, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		if err != nil {
			return nil, nil, err
		}
		mime := resp.Header.Get("Content-Type")
		if mime == "" || mime == "application/octet-stream" {
			mime = http.DetectContentType(img)
		}
		note := fmt.Sprintf("Camera %q on %s.", fmt.Sprint(cam["name"]), p.Name)
		if lm, err := http.ParseTime(resp.Header.Get("Last-Modified")); err == nil {
			note += fmt.Sprintf(" Taken %s (%s ago).", lm.Local().Format(time.DateTime), time.Since(lm).Round(time.Second))
		}
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: note},
			&mcp.ImageContent{Data: img, MIMEType: mime},
		}}, nil, nil
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "get_telemetry",
		Description: "Recent telemetry time series for the printer (temperatures, fans, speed, axis positions over time).",
		Annotations: readOnly("Printer telemetry"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in telemetryInput) (*mcp.CallToolResult, any, error) {
		p, err := s.resolvePrinter(ctx, in.Printer)
		if err != nil {
			return nil, nil, err
		}
		minutes, gran := in.Minutes, in.Granularity
		if minutes <= 0 {
			minutes = 30
		}
		if gran <= 0 {
			gran = 15
		}
		q := url.Values{
			"from":        {strconv.FormatInt(time.Now().Add(-time.Duration(minutes)*time.Minute).Unix(), 10)},
			"granularity": {strconv.Itoa(gran)},
		}
		var out json.RawMessage
		if err := s.client.Get(ctx, printerPath(p.UUID, "telemetry"), q, &out); err != nil {
			return nil, nil, err
		}
		return jsonResult(out)
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "list_events",
		Description: "The printer's event log, newest first: state changes, job starts and finishes, errors, " +
			"attention requests, commands, and file transfers. Use it to find out what happened while nobody was watching.",
		Annotations: readOnly("Printer events"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in eventsInput) (*mcp.CallToolResult, any, error) {
		p, err := s.resolvePrinter(ctx, in.Printer)
		if err != nil {
			return nil, nil, err
		}
		var out json.RawMessage
		if err := s.client.Get(ctx, printerPath(p.UUID, "events"), pageQuery(in.Limit, in.Offset, 20), &out); err != nil {
			return nil, nil, err
		}
		return jsonResult(out)
	})
}

func pageQuery(limit, offset, def int) url.Values {
	if limit <= 0 {
		limit = def
	}
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	return q
}

// jsonID renders a decoded JSON id (float64 or string) for use in a path.
func jsonID(v any) string {
	if f, ok := v.(float64); ok {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}
