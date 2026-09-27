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
)

type listFilesInput struct {
	printerRef
	Path   string `json:"path,omitempty" jsonschema:"folder on the printer, e.g. /usb or /usb/parts; omit to list the printer's storages"`
	Limit  int    `json:"limit,omitempty" jsonschema:"default 50"`
	Offset int    `json:"offset,omitempty"`
}

type uploadInput struct {
	printerRef
	LocalPath   string `json:"local_path" jsonschema:"absolute path of a sliced file (.bgcode or .gcode) on this computer"`
	Destination string `json:"destination,omitempty" jsonschema:"printer folder to copy it into, e.g. /usb/; default is the printer's first storage"`
	Filename    string `json:"filename,omitempty" jsonschema:"name on the printer; default is the local file name"`
	Then        string `json:"then,omitempty" jsonschema:"after uploading: none (just store it on the printer), queue (append to the print queue), or print (queue first and mark the printer ready so it starts); default none"`
}

type queueInput struct {
	printerRef
	Path      string `json:"path,omitempty" jsonschema:"file already on the printer, e.g. /usb/part.bgcode"`
	Hash      string `json:"hash,omitempty" jsonschema:"file in Connect storage (from upload_file or list_connect_files); use instead of path"`
	TeamID    int64  `json:"team_id,omitempty" jsonschema:"team owning the Connect file; default is the printer's team"`
	Position  *int   `json:"position,omitempty" jsonschema:"0 = front of the queue, -1 = end (default)"`
	SetReady  bool   `json:"set_ready,omitempty" jsonschema:"also mark the printer ready (plate clear) so Connect starts the next job; only with hash"`
	WaitUntil int64  `json:"wait_until,omitempty" jsonschema:"unix time before which the job must not start; only with hash"`
}

type startPrintInput struct {
	printerRef
	Path string `json:"path" jsonschema:"file on the printer, e.g. /usb/part.bgcode (see list_printer_files)"`
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
		Description: "Browse files on the printer's own storage (USB drive etc.). Without path, lists the storages.",
		Annotations: readOnly("List printer files"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listFilesInput) (*mcp.CallToolResult, any, error) {
		p, err := s.resolvePrinter(ctx, in.Printer)
		if err != nil {
			return nil, nil, err
		}
		var out json.RawMessage
		if strings.TrimSpace(in.Path) == "" {
			err = s.client.Get(ctx, printerPath(p.UUID, "storages"), nil, &out)
		} else {
			q := pageQuery(in.Limit, in.Offset, 50)
			q.Set("path", in.Path)
			err = s.client.Get(ctx, printerPath(p.UUID, "files"), q, &out)
		}
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(out)
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "delete_printer_files",
		Description: "Delete files from the printer's storage. Folders are removed with send_command DELETE_FOLDER.",
		Annotations: mutating("Delete printer files", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteFilesInput) (*mcp.CallToolResult, any, error) {
		if len(in.Paths) == 0 {
			return nil, nil, errors.New("paths is empty")
		}
		p, err := s.resolvePrinter(ctx, in.Printer)
		if err != nil {
			return nil, nil, err
		}
		var out json.RawMessage
		err = s.client.JSON(ctx, connect.Request{Method: http.MethodDelete, Path: printerPath(p.UUID, "files"), JSON: map[string]any{"files": in.Paths}}, &out)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(map[string]any{"printer": p.Name, "deleted": in.Paths, "response": out})
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "upload_file",
		Description: "Upload a sliced print file from this computer to Prusa Connect and on to the printer's storage, " +
			"optionally queueing it or starting it. Connect transfers the file to the printer in the background; " +
			"get_transfers shows progress. With then=print the printer is marked ready and starts the job as soon as " +
			"the file arrives: confirm the plate is clear (get_camera_snapshot) first.",
		Annotations: mutating("Upload print file", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in uploadInput) (*mcp.CallToolResult, any, error) {
		then := strings.ToLower(strings.TrimSpace(in.Then))
		if then == "" {
			then = "none"
		}
		if then != "none" && then != "queue" && then != "print" {
			return nil, nil, fmt.Errorf("then must be none, queue, or print (got %q)", in.Then)
		}
		p, err := s.resolvePrinter(ctx, in.Printer)
		if err != nil {
			return nil, nil, err
		}
		res, err := s.upload(ctx, p, in.LocalPath, in.Destination, in.Filename)
		if err != nil {
			return nil, nil, err
		}
		out := map[string]any{"printer": p.Name, "upload": res.Upload, "file": res.File}
		if then != "none" {
			pos := -1
			body := map[string]any{"hash": res.Hash, "team_id": p.TeamID, "position": pos}
			if then == "print" {
				body["position"] = 0
				body["set_ready"] = true
			}
			var queued json.RawMessage
			if err := s.client.JSON(ctx, connect.Request{Method: http.MethodPost, Path: printerPath(p.UUID, "queue"), JSON: body}, &queued); err != nil {
				return nil, nil, fmt.Errorf("uploaded (hash %s) but queueing failed: %w", res.Hash, err)
			}
			out["queued"] = queued
		}
		return jsonResult(out)
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "start_print",
		Description: "Start printing a file that is already on the printer's storage. The printer must be idle " +
			"and the plate clear: check get_printer and get_camera_snapshot first.",
		Annotations: mutating("Start print", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in startPrintInput) (*mcp.CallToolResult, any, error) {
		p, err := s.resolvePrinter(ctx, in.Printer)
		if err != nil {
			return nil, nil, err
		}
		res, err := s.runCommand(ctx, p, "START_PRINT", map[string]any{"path": in.Path}, false, 0)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(res)
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "get_queue",
		Description: "The printer's print queue in Connect: jobs waiting to print, in order, with their ids.",
		Annotations: readOnly("Get print queue"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in printerRef) (*mcp.CallToolResult, any, error) {
		p, err := s.resolvePrinter(ctx, in.Printer)
		if err != nil {
			return nil, nil, err
		}
		var out json.RawMessage
		if err := s.client.Get(ctx, printerPath(p.UUID, "queue"), url.Values{"limit": {"100"}}, &out); err != nil {
			return nil, nil, err
		}
		return jsonResult(out)
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "add_to_queue",
		Description: "Add a file to the printer's print queue, either one on the printer (path) or one in Connect " +
			"storage (hash). Connect starts the next queued job when the printer is idle and marked ready " +
			"(SET_PRINTER_READY via send_command, or set_ready here).",
		Annotations: mutating("Queue print", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in queueInput) (*mcp.CallToolResult, any, error) {
		if (in.Path == "") == (in.Hash == "") {
			return nil, nil, errors.New("pass exactly one of path or hash")
		}
		p, err := s.resolvePrinter(ctx, in.Printer)
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
		if err := s.client.JSON(ctx, connect.Request{Method: http.MethodPost, Path: printerPath(p.UUID, "queue"), JSON: body}, &out); err != nil {
			return nil, nil, err
		}
		return jsonResult(out)
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "remove_from_queue",
		Description: "Remove a job from the printer's print queue.",
		Annotations: mutating("Remove queued job", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in queueJobInput) (*mcp.CallToolResult, any, error) {
		p, err := s.resolvePrinter(ctx, in.Printer)
		if err != nil {
			return nil, nil, err
		}
		path := printerPath(p.UUID, "queue", strconv.FormatInt(in.JobID, 10))
		if err := s.client.JSON(ctx, connect.Request{Method: http.MethodDelete, Path: path}, nil); err != nil {
			return nil, nil, err
		}
		return textResult("Removed job %d from %s's queue.", in.JobID, p.Name)
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "get_transfers",
		Description: "File transfers to the printer: the one in progress (with progress and time remaining) and files waiting in the download queue.",
		Annotations: readOnly("File transfers"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in printerRef) (*mcp.CallToolResult, any, error) {
		p, err := s.resolvePrinter(ctx, in.Printer)
		if err != nil {
			return nil, nil, err
		}
		var transfers, queue json.RawMessage
		if err := s.client.Get(ctx, printerPath(p.UUID, "transfers"), nil, &transfers); err != nil {
			return nil, nil, err
		}
		if err := s.client.Get(ctx, printerPath(p.UUID, "download-queue"), nil, &queue); err != nil {
			return nil, nil, err
		}
		return jsonResult(map[string]any{"transfers": transfers, "download_queue": queue})
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "list_connect_files",
		Description: "Files stored in Prusa Connect's cloud storage for a team (uploads, with their hashes for add_to_queue).",
		Annotations: readOnly("List Connect files"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in connectFilesInput) (*mcp.CallToolResult, any, error) {
		team := in.TeamID
		if team == 0 {
			p, err := s.resolvePrinter(ctx, "")
			if err != nil {
				return nil, nil, fmt.Errorf("pass team_id: %w", err)
			}
			team = p.TeamID
		}
		var out json.RawMessage
		path := "/app/teams/" + strconv.FormatInt(team, 10) + "/files"
		if err := s.client.Get(ctx, path, pageQuery(in.Limit, in.Offset, 50), &out); err != nil {
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
func (s *Server) upload(ctx context.Context, p printerSummary, localPath, destination, filename string) (*uploadResult, error) {
	if !filepath.IsAbs(localPath) {
		return nil, fmt.Errorf("local_path must be absolute (got %q)", localPath)
	}
	st, err := os.Stat(localPath)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", localPath)
	}
	if filename == "" {
		filename = filepath.Base(localPath)
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if ext != ".bgcode" && ext != ".gcode" {
		return nil, fmt.Errorf("%s is not a sliced print file (.bgcode or .gcode); slice it first", filename)
	}
	if destination == "" {
		destination, err = s.defaultStorage(ctx, p.UUID)
		if err != nil {
			return nil, err
		}
	}

	var created json.RawMessage
	err = s.client.JSON(ctx, connect.Request{
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
		return nil, fmt.Errorf("Connect did not return an upload id: %s", created)
	}
	uploadID := strings.Trim(string(meta.ID), `"`)

	resp, err := s.client.Do(ctx, connect.Request{
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
		_ = s.client.JSON(context.WithoutCancel(ctx), abort, nil)
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
	if !json.Valid(fileJSON) {
		fileJSON = nil
	}
	return &uploadResult{Hash: hash, Upload: created, File: fileJSON}, nil
}

// defaultStorage returns the path of the printer's first storage.
func (s *Server) defaultStorage(ctx context.Context, uuid string) (string, error) {
	var resp struct {
		Storages []struct {
			Path     string `json:"path"`
			Name     string `json:"name"`
			ReadOnly bool   `json:"read_only"`
		} `json:"storages"`
	}
	if err := s.client.Get(ctx, printerPath(uuid, "storages"), nil, &resp); err != nil {
		return "", err
	}
	for _, st := range resp.Storages {
		if st.Path != "" && !st.ReadOnly {
			return strings.TrimRight(st.Path, "/") + "/", nil
		}
	}
	return "", errors.New("the printer reports no writable storage (is a USB drive inserted?)")
}
