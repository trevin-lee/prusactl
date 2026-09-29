package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/trevin-lee/prusactl/internal/connect"
	"github.com/trevin-lee/prusactl/internal/link"
)

type listFilesInput struct {
	printerRef
	Path   string `json:"path,omitempty" jsonschema:"folder on the printer, e.g. /usb or /usb/parts; omit to list the printer's storages"`
	Limit  int    `json:"limit,omitempty" jsonschema:"how many entries to return; default 50, at most 500"`
	Offset int    `json:"offset,omitempty" jsonschema:"entries to skip, for the next page (see next_offset)"`
}

type uploadInput struct {
	printerRef
	plateConfirmation
	LocalPath   string `json:"local_path" jsonschema:"absolute path of a sliced file (.bgcode or .gcode) on this computer"`
	Destination string `json:"destination,omitempty" jsonschema:"printer folder to copy it into, e.g. /usb/; default is the printer's first storage"`
	Filename    string `json:"filename,omitempty" jsonschema:"name on the printer; default is the local file name"`
	Then        string `json:"then,omitempty" jsonschema:"after uploading: none (just store it), print (start it right away), or queue (add to the Prusa Connect print queue); default none"`
	Overwrite   bool   `json:"overwrite,omitempty" jsonschema:"replace a file with the same name on the printer (direct route)"`
}

type downloadInput struct {
	printerRef
	Path      string `json:"path" jsonschema:"file on the printer, e.g. /usb/part.bgcode"`
	LocalPath string `json:"local_path" jsonschema:"absolute path on this computer: a folder (the file keeps its name) or a file name"`
	Overwrite bool   `json:"overwrite,omitempty" jsonschema:"replace an existing local file"`
}

type queueInput struct {
	printerRef
	Path      string `json:"path,omitempty" jsonschema:"file already on the printer, e.g. /usb/part.bgcode"`
	Hash      string `json:"hash,omitempty" jsonschema:"file in Connect storage (from upload_file or list_connect_files); use instead of path"`
	TeamID    int64  `json:"team_id,omitempty" jsonschema:"team owning the Connect file; default is the printer's team"`
	Position  *int   `json:"position,omitempty" jsonschema:"0 = front of the queue, -1 = end (default)"`
	SetReady  bool   `json:"set_ready,omitempty" jsonschema:"also mark the printer ready so Connect starts the next job; only with hash. Ready means the plate is clear, the same confirmation plate_clear gives elsewhere: set it only after checking the camera or asking the user"`
	WaitUntil int64  `json:"wait_until,omitempty" jsonschema:"unix time before which the job must not start; only with hash"`
}

type startPrintInput struct {
	printerRef
	plateConfirmation
	Path string `json:"path" jsonschema:"file on the printer as list_printer_files reports it, e.g. /usb/3DBENC~2.BGC"`
}

type deleteFilesInput struct {
	printerRef
	Paths []string `json:"paths" jsonschema:"full paths on the printer, e.g. /usb/old.bgcode"`
}

type queueJobInput struct {
	printerRef
	JobID int64 `json:"job_id" jsonschema:"queued job id from get_queue"`
}

type connectFilesInput struct {
	TeamID int64 `json:"team_id,omitempty" jsonschema:"team whose storage to list; default is the team of the first printer"`
	Limit  int   `json:"limit,omitempty" jsonschema:"default 50"`
	Offset int   `json:"offset,omitempty"`
}

func (s *Server) addFileTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "list_printer_files",
		Description: "Browse files on the printer's own storage (USB drive). Without path, lists the storages.",
		Annotations: readOnly("List printer files"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listFilesInput) (*mcp.CallToolResult, any, error) {
		t, err := s.route(ctx, in.printerRef)
		if err != nil {
			return nil, nil, err
		}
		dir := strings.TrimSpace(in.Path)
		var out json.RawMessage
		switch {
		case t.direct && dir == "":
			_, err = s.direct().Get(ctx, "/api/v1/storage", &out)
		case t.direct:
			var path string
			if path, err = link.FilePath(dir); err == nil {
				_, err = s.direct().Get(ctx, path, &out)
			}
			if err == nil {
				out = compactFolder(dir, out, in.Limit, in.Offset)
			}
		case dir == "":
			err = s.connect.Get(ctx, printerPath(t.connect.UUID, "storages"), nil, &out)
		default:
			q := pageQuery(min(max(in.Limit, 0), 500), in.Offset, 50)
			q.Set("path", dir)
			if err = s.connect.Get(ctx, printerPath(t.connect.UUID, "files"), q, &out); err == nil {
				out = compactConnectFolder(out)
			}
		}
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(withVia(t, map[string]any{"files": out}))
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "delete_printer_files",
		Description: "Delete files from the printer's storage.",
		Annotations: mutating("Delete printer files", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteFilesInput) (*mcp.CallToolResult, any, error) {
		if len(in.Paths) == 0 {
			return nil, nil, errors.New("paths is empty")
		}
		t, err := s.route(ctx, in.printerRef)
		if err != nil {
			return nil, nil, err
		}
		if t.direct {
			for i, p := range in.Paths {
				path, err := link.FilePath(p)
				if err == nil {
					_, err = s.direct().JSON(ctx, link.Request{Method: http.MethodDelete, Path: path}, nil)
				}
				if err != nil {
					return nil, nil, fmt.Errorf("deleted %q; failed on %s: %w", in.Paths[:i], p, err)
				}
			}
			return jsonResult(withVia(t, map[string]any{"deleted": in.Paths}))
		}
		var out json.RawMessage
		err = s.connect.JSON(ctx, connect.Request{Method: http.MethodDelete, Path: printerPath(t.connect.UUID, "files"), JSON: map[string]any{"files": in.Paths}}, &out)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(withVia(t, map[string]any{"deleted": in.Paths, "response": out}))
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "upload_file",
		Description: "Upload a sliced print file (.bgcode/.gcode) from this computer to the printer, optionally " +
			"starting it (then=print) or adding it to the Prusa Connect queue (then=queue). Directly on the local " +
			"network the file goes straight to the printer; through Connect it is stored in Connect and copied to the " +
			"printer in the background (get_transfers shows progress); there then=print puts it first in the queue and " +
			"marks the printer ready, and Connect starts it once the file arrives. then=print is refused unless the " +
			"printer is idle. Before then=print, confirm the plate is clear.",
		Annotations: mutating("Upload print file", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in uploadInput) (*mcp.CallToolResult, any, error) {
		then := strings.ToLower(strings.TrimSpace(in.Then))
		switch then {
		case "":
			then = "none"
		case "none", "print":
		case "queue":
			if err := checkVia(in.printerRef, "connect"); err != nil {
				return nil, nil, fmt.Errorf("then=queue: %w", err)
			}
			in.Via = "connect" // the queue lives in Connect
		default:
			return nil, nil, fmt.Errorf("then must be none, print, or queue (got %q)", in.Then)
		}
		t, err := s.route(ctx, in.printerRef)
		if err != nil {
			return nil, nil, err
		}
		if then == "print" {
			// Check before a possibly long upload, not after.
			if err := s.readyToStart(ctx, t, in.PlateClear); err != nil {
				return nil, nil, err
			}
		}
		if t.direct {
			path, err := s.uploadDirect(ctx, in.LocalPath, in.Destination, in.Filename, in.Overwrite, then == "print")
			if err != nil {
				return nil, nil, err
			}
			out := map[string]any{"uploaded": path, "printing": false}
			if then == "print" {
				for k, v := range startReport(s.startedState(ctx, t)) {
					out[k] = v
				}
			}
			return jsonResult(withVia(t, out))
		}
		p := t.connect
		res, err := s.uploadViaConnect(ctx, p, in.LocalPath, in.Destination, in.Filename)
		if err != nil {
			return nil, nil, err
		}
		out := withVia(t, map[string]any{"upload": res.Upload, "file": res.File})
		if then != "none" {
			body := map[string]any{"hash": res.Hash, "team_id": p.TeamID, "position": -1}
			if then == "print" {
				body["position"] = 0
				body["set_ready"] = true
			}
			var queued json.RawMessage
			if err := s.connect.JSON(ctx, connect.Request{Method: http.MethodPost, Path: printerPath(p.UUID, "queue"), JSON: body}, &queued); err != nil {
				return nil, nil, fmt.Errorf("uploaded (hash %s) but queueing failed: %w", res.Hash, err)
			}
			out["queued"] = queued
			if then == "print" {
				// Through Connect, "print" means first in the queue with the
				// printer marked ready; Connect starts it once the file has been
				// copied over and the printer checks in.
				out["printing"] = false
				out["note"] = "queued first and the printer marked ready; Connect starts it when the file reaches the printer (get_transfers, get_printer)"
			}
		}
		return jsonResult(out)
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "download_printer_file",
		Description: "Copy a file from the printer's storage to this computer, e.g. to inspect the G-code or the " +
			"slicer settings a finished print used. Direct route only: Prusa Connect can't read files off the printer.",
		Annotations: mutating("Download printer file", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in downloadInput) (*mcp.CallToolResult, any, error) {
		if !filepath.IsAbs(in.LocalPath) {
			return nil, nil, fmt.Errorf("local_path must be absolute (got %q)", in.LocalPath)
		}
		if err := checkVia(in.printerRef, "direct"); err != nil {
			return nil, nil, err
		}
		in.Via = "direct"
		t, err := s.route(ctx, in.printerRef)
		if err != nil {
			return nil, nil, err
		}
		path, n, err := s.direct().Download(ctx, in.Path, in.LocalPath, in.Overwrite)
		if errors.Is(err, link.ErrExists) {
			return nil, nil, fmt.Errorf("%s already exists; set overwrite to replace it", path)
		}
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(withVia(t, map[string]any{"saved": path, "bytes": n}))
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "start_print",
		Description: "Start printing a file that is already on the printer's storage. The printer must be idle " +
			"and the plate clear: check get_printer (and the camera, if any) first.",
		Annotations: mutating("Start print", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in startPrintInput) (*mcp.CallToolResult, any, error) {
		t, err := s.route(ctx, in.printerRef)
		if err != nil {
			return nil, nil, err
		}
		if err := s.readyToStart(ctx, t, in.PlateClear); err != nil {
			return nil, nil, err
		}
		if t.direct {
			path, err := link.FilePath(in.Path)
			if err != nil {
				return nil, nil, err
			}
			if _, err := s.direct().JSON(ctx, link.Request{Method: http.MethodPost, Path: path}, nil); err != nil {
				return nil, nil, err
			}
			out := startReport(s.startedState(ctx, t))
			out["started"] = in.Path
			return jsonResult(withVia(t, out))
		}
		res, err := s.runCommand(ctx, t.connect, "START_PRINT", map[string]any{"path": in.Path}, false, 0)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(withVia(t, map[string]any{"started": in.Path, "result": res}))
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "get_queue",
		Description: "The printer's print queue in Prusa Connect: jobs waiting to print, in order, with their ids.",
		Annotations: readOnly("Get print queue"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in pagedRef) (*mcp.CallToolResult, any, error) {
		p, err := s.connectPrinter(ctx, in.printerRef)
		if err != nil {
			return nil, nil, err
		}
		var out json.RawMessage
		if err := s.connect.Get(ctx, printerPath(p.UUID, "queue"), pageQuery(min(max(in.Limit, 0), 500), in.Offset, 100), &out); err != nil {
			return nil, nil, err
		}
		return jsonResult(withVia(target{name: p.Name, connect: p}, map[string]any{"queue": out}))
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "add_to_queue",
		Description: "Add a file to the printer's Prusa Connect print queue, either one on the printer (path) or " +
			"one in Connect storage (hash). Connect starts the next queued job when the printer is idle and marked " +
			"ready (send_command SET_PRINTER_READY, or set_ready here). Marking it ready says the plate is clear, the " +
			"same confirmation as plate_clear: do it only after checking the camera or asking the user.",
		Annotations: mutating("Queue print", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in queueInput) (*mcp.CallToolResult, any, error) {
		if (in.Path == "") == (in.Hash == "") {
			return nil, nil, errors.New("pass exactly one of path or hash")
		}
		p, err := s.connectPrinter(ctx, in.printerRef)
		if err != nil {
			return nil, nil, err
		}
		pos := -1
		if in.Position != nil {
			pos = *in.Position
		}
		body := map[string]any{"position": pos}
		if in.Path != "" {
			body["path"] = in.Path
		} else {
			team := in.TeamID
			if team == 0 {
				team = p.TeamID
			}
			body["hash"], body["team_id"] = in.Hash, team
			if in.SetReady {
				body["set_ready"] = true
			}
			if in.WaitUntil > 0 {
				body["wait_until"] = in.WaitUntil
			}
		}
		var out json.RawMessage
		if err := s.connect.JSON(ctx, connect.Request{Method: http.MethodPost, Path: printerPath(p.UUID, "queue"), JSON: body}, &out); err != nil {
			return nil, nil, err
		}
		return jsonResult(out)
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "remove_from_queue",
		Description: "Remove a job from the printer's Prusa Connect print queue.",
		Annotations: mutating("Remove queued job", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in queueJobInput) (*mcp.CallToolResult, any, error) {
		p, err := s.connectPrinter(ctx, in.printerRef)
		if err != nil {
			return nil, nil, err
		}
		path := printerPath(p.UUID, "queue", strconv.FormatInt(in.JobID, 10))
		if err := s.connect.JSON(ctx, connect.Request{Method: http.MethodDelete, Path: path}, nil); err != nil {
			return nil, nil, err
		}
		return textResult("Removed job %d from %s's queue.", in.JobID, p.Name)
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "get_transfers",
		Description: "File transfers to the printer that are in progress (with progress), plus, through Prusa Connect, files waiting in its download queue.",
		Annotations: readOnly("File transfers"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in printerRef) (*mcp.CallToolResult, any, error) {
		t, err := s.route(ctx, in)
		if err != nil {
			return nil, nil, err
		}
		if t.direct {
			var tr json.RawMessage
			found, err := s.direct().Get(ctx, "/api/v1/transfer", &tr)
			if err != nil {
				return nil, nil, err
			}
			if !found {
				tr = json.RawMessage("null")
			}
			return jsonResult(withVia(t, map[string]any{"transfer": tr}))
		}
		var transfers, queue json.RawMessage
		if err := s.connect.Get(ctx, printerPath(t.connect.UUID, "transfers"), nil, &transfers); err != nil {
			return nil, nil, err
		}
		if err := s.connect.Get(ctx, printerPath(t.connect.UUID, "download-queue"), nil, &queue); err != nil {
			return nil, nil, err
		}
		return jsonResult(withVia(t, map[string]any{"transfers": transfers, "download_queue": queue}))
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "list_connect_files",
		Description: "Files stored in Prusa Connect's cloud storage for a team, with the hashes add_to_queue takes.",
		Annotations: readOnly("List Connect files"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in connectFilesInput) (*mcp.CallToolResult, any, error) {
		team := in.TeamID
		if team == 0 {
			p, err := s.connectPrinter(ctx, printerRef{})
			if err != nil {
				return nil, nil, fmt.Errorf("pass team_id: %w", err)
			}
			team = p.TeamID
		}
		var out json.RawMessage
		path := "/app/teams/" + strconv.FormatInt(team, 10) + "/files"
		if err := s.connect.Get(ctx, path, pageQuery(in.Limit, in.Offset, 50), &out); err != nil {
			return nil, nil, err
		}
		return jsonResult(out)
	})
}

type uploadResult struct {
	Hash   string
	Upload json.RawMessage
	File   json.RawMessage
}

// upload mirrors the web app: register the upload with its destination, then
// PUT the bytes to the team's raw file endpoint. Connect then copies the file
// to the printer on its own.
// compactFolder trims a PrusaLink folder listing to what an agent needs:
// each entry's full path (the printer's short 8.3 name, which is what
// start_print and delete take), display name, type, and times.
// compactFolder turns PrusaLink's folder listing into one page of entries,
// with the total and, when there is more, the offset of the next page.
func compactFolder(dir string, raw json.RawMessage, limit, offset int) json.RawMessage {
	var folder struct {
		Name     string `json:"name"`
		Children []struct {
			Name        string `json:"name"`
			DisplayName string `json:"display_name"`
			Type        string `json:"type"`
			Size        *int64 `json:"size,omitempty"`
			MTimestamp  int64  `json:"m_timestamp"`
			RO          bool   `json:"ro"`
		} `json:"children"`
	}
	if json.Unmarshal(raw, &folder) != nil || folder.Children == nil {
		return raw
	}
	base := "/" + strings.Trim(dir, "/") + "/"
	total := len(folder.Children)
	switch {
	case limit <= 0:
		limit = 50
	case limit > 500:
		limit = 500
	}
	offset = min(max(offset, 0), total)
	page := folder.Children[offset:min(offset+limit, total)]
	entries := make([]map[string]any, 0, len(page))
	for _, c := range page {
		e := map[string]any{"path": base + c.Name, "name": c.DisplayName, "type": c.Type}
		if c.DisplayName == "" {
			e["name"] = c.Name
		}
		if c.MTimestamp > 0 {
			e["modified"] = time.Unix(c.MTimestamp, 0).Format(time.DateTime)
		}
		if c.Size != nil {
			e["size"] = *c.Size
		}
		if c.RO {
			e["read_only"] = true
		}
		entries = append(entries, e)
	}
	res := map[string]any{"folder": base, "entries": entries, "total": total}
	if next := offset + len(page); next < total {
		res["next_offset"] = next
	}
	b, err := json.Marshal(res)
	if err != nil {
		return raw
	}
	return b
}

// compactConnectFolder turns Connect's folder listing into the same page shape
// compactFolder produces, so both routes answer alike. Connect does the paging
// itself and reports it under "pager"; hashes are kept for add_to_queue.
func compactConnectFolder(raw json.RawMessage) json.RawMessage {
	var folder struct {
		Path  string `json:"path"`
		Files *[]struct {
			Name        string `json:"name"`
			DisplayName string `json:"display_name"`
			Path        string `json:"path"`
			Type        string `json:"type"`
			Hash        string `json:"hash"`
			Size        *int64 `json:"size,omitempty"`
			MTimestamp  int64  `json:"m_timestamp"`
			RO          bool   `json:"read_only"`
		} `json:"files"`
		Pager struct {
			Limit  int `json:"limit"`
			Offset int `json:"offset"`
			Total  int `json:"total"`
		} `json:"pager"`
	}
	if json.Unmarshal(raw, &folder) != nil || folder.Files == nil {
		return raw
	}
	entries := make([]map[string]any, 0, len(*folder.Files))
	for _, f := range *folder.Files {
		e := map[string]any{"path": f.Path, "name": f.DisplayName, "type": f.Type}
		if f.DisplayName == "" {
			e["name"] = f.Name
		}
		if f.MTimestamp > 0 {
			e["modified"] = time.Unix(f.MTimestamp, 0).Format(time.DateTime)
		}
		if f.Size != nil {
			e["size"] = *f.Size
		}
		if f.RO {
			e["read_only"] = true
		}
		if f.Hash != "" {
			e["hash"] = f.Hash
		}
		entries = append(entries, e)
	}
	res := map[string]any{"folder": folder.Path, "entries": entries, "total": folder.Pager.Total}
	if next := folder.Pager.Offset + len(entries); next < folder.Pager.Total {
		res["next_offset"] = next
	}
	b, err := json.Marshal(res)
	if err != nil {
		return raw
	}
	return b
}

// checkPrintFile validates a local sliced file and settles its printer name.
func checkPrintFile(localPath, filename string) (os.FileInfo, string, error) {
	if !filepath.IsAbs(localPath) {
		return nil, "", fmt.Errorf("local_path must be absolute (got %q)", localPath)
	}
	st, err := os.Stat(localPath)
	if err != nil {
		return nil, "", err
	}
	if !st.Mode().IsRegular() {
		return nil, "", fmt.Errorf("%s is not a regular file", localPath)
	}
	if filename == "" {
		filename = filepath.Base(localPath)
	}
	if strings.ContainsAny(filename, "/\\") {
		return nil, "", fmt.Errorf("filename must not contain slashes (got %q)", filename)
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if ext != ".bgcode" && ext != ".gcode" {
		return nil, "", fmt.Errorf("%s is not a sliced print file (.bgcode or .gcode); slice it first", filename)
	}
	return st, filename, nil
}

// uploadDirect PUTs the file straight to the printer's storage.
func (s *Server) uploadDirect(ctx context.Context, localPath, destination, filename string, overwrite, printAfter bool) (string, error) {
	st, filename, err := checkPrintFile(localPath, filename)
	if err != nil {
		return "", err
	}
	if destination == "" {
		destination = "/usb/"
	}
	target := strings.TrimRight(destination, "/") + "/" + filename
	path, err := link.FilePath(target)
	if err != nil {
		return "", err
	}
	flag := func(b bool) string {
		if b {
			return "?1"
		}
		return "?0"
	}
	_, err = s.direct().JSON(ctx, link.Request{
		Method:        http.MethodPut,
		Path:          path,
		Body:          func() (io.ReadCloser, error) { return os.Open(localPath) },
		ContentLength: st.Size(),
		ContentType:   "application/octet-stream",
		Header:        http.Header{"Overwrite": {flag(overwrite)}, "Print-After-Upload": {flag(printAfter)}},
		Timeout:       30 * time.Minute,
	}, nil)
	if link.IsStatus(err, http.StatusConflict) && !overwrite {
		return "", fmt.Errorf("%s already exists on the printer (pass overwrite=true to replace it): %w", target, err)
	}
	return target, err
}

// uploadViaConnect mirrors the web app: register the upload with its
// destination, then PUT the bytes to the team's raw file endpoint. Connect
// then copies the file to the printer on its own.
func (s *Server) uploadViaConnect(ctx context.Context, p printerSummary, localPath, destination, filename string) (*uploadResult, error) {
	st, filename, err := checkPrintFile(localPath, filename)
	if err != nil {
		return nil, err
	}
	if destination == "" {
		destination, err = s.defaultStorage(ctx, p.UUID)
		if err != nil {
			return nil, err
		}
	}

	var created json.RawMessage
	err = s.connect.JSON(ctx, connect.Request{
		Method: http.MethodPost,
		Path:   "/app/users/teams/" + strconv.FormatInt(p.TeamID, 10) + "/uploads",
		JSON: map[string]any{
			"destination":  destination,
			"filename":     filename,
			"size":         st.Size(),
			"printer_uuid": p.UUID,
		},
	}, &created)
	if err != nil {
		return nil, err
	}
	var meta struct {
		ID   json.RawMessage `json:"id"`
		Hash string          `json:"hash"`
	}
	if err := json.Unmarshal(created, &meta); err != nil || len(meta.ID) == 0 {
		return nil, connect.Missing("POST", "/app/users/teams/{team}/uploads", "id")
	}
	uploadID := strings.Trim(string(meta.ID), `"`)

	resp, err := s.connect.Do(ctx, connect.Request{
		Method: http.MethodPut,
		Path:   "/app/teams/" + strconv.FormatInt(p.TeamID, 10) + "/files/raw",
		Query:  url.Values{"upload_id": {uploadID}},
		Body: func() (io.ReadCloser, error) {
			return os.Open(localPath)
		},
		ContentLength: st.Size(),
		ContentType:   "text/x.gcode", // what PrusaSlicer sends for both .gcode and .bgcode
		Header:        http.Header{"Upload-Size": {strconv.FormatInt(st.Size(), 10)}},
		Timeout:       30 * time.Minute,
	})
	if err != nil {
		// Don't leave a half-finished upload holding the team's quota.
		abort := connect.Request{Method: http.MethodPost, Path: "/app/uploads/" + connect.PathEscape(uploadID) + "/abort"}
		_ = s.connect.JSON(context.WithoutCancel(ctx), abort, nil)
		return nil, err
	}
	defer resp.Body.Close()
	fileJSON, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var file struct {
		Hash string `json:"hash"`
	}
	_ = json.Unmarshal(fileJSON, &file)
	hash := file.Hash
	if hash == "" {
		hash = meta.Hash
	}
	if hash == "" {
		// The file is in Connect, but without its hash it can't be queued or
		// found again.
		return nil, connect.Missing("PUT", "/app/teams/{team}/files/raw", "hash")
	}
	if !json.Valid(fileJSON) {
		fileJSON = nil
	}
	return &uploadResult{Hash: hash, Upload: created, File: fileJSON}, nil
}

// defaultStorage returns the path of the printer's first storage.
func (s *Server) defaultStorage(ctx context.Context, uuid string) (string, error) {
	var resp struct {
		Storages []struct {
			Path       string `json:"path"`
			Mountpoint string `json:"mountpoint"`
			ReadOnly   bool   `json:"read_only"`
		} `json:"storages"`
	}
	if err := s.connect.Get(ctx, printerPath(uuid, "storages"), nil, &resp); err != nil {
		return "", err
	}
	for _, st := range resp.Storages {
		path := st.Path
		if path == "" {
			path = st.Mountpoint
		}
		if path != "" && !st.ReadOnly {
			return strings.TrimRight(path, "/") + "/", nil
		}
	}
	return "", errors.New("the printer reports no writable storage (is a USB drive inserted?)")
}
