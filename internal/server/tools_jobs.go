package server

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listJobsInput struct {
	pagedRef          // default 10
	States   []string `json:"states,omitempty" jsonschema:"filter by job state, e.g. PRINTING, FIN_OK, FIN_ERROR, FIN_STOPPED"`
}

type jobInput struct {
	printerRef
	JobID int64 `json:"job_id" jsonschema:"job id from list_jobs or get_printer's job_info"`
}

func (s *Server) addJobTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "list_jobs",
		Description: "The printer's job history, newest first: what printed, when, for how long, and how it ended.",
		Annotations: readOnly("Print history"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listJobsInput) (*mcp.CallToolResult, any, error) {
		p, err := s.connectPrinter(ctx, in.printerRef)
		if err != nil {
			return nil, nil, err
		}
		q := pageQuery(in.Limit, in.Offset, 10)
		for _, st := range in.States {
			q.Add("state", strings.ToUpper(st))
		}
		var out json.RawMessage
		if err := s.connect.Get(ctx, printerPath(p.UUID, "jobs"), q, &out); err != nil {
			return nil, nil, err
		}
		return jsonResult(withNextOffset(out))
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "get_job",
		Description: "One print job in detail, including the objects on the plate that can be cancelled individually " +
			"(cancelable.objects; cancel one with send_command CANCEL_OBJECT and kwargs {\"object_id\": id}).",
		Annotations: readOnly("Get job"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in jobInput) (*mcp.CallToolResult, any, error) {
		p, err := s.connectPrinter(ctx, in.printerRef)
		if err != nil {
			return nil, nil, err
		}
		var out json.RawMessage
		if err := s.connect.Get(ctx, printerPath(p.UUID, "jobs", strconv.FormatInt(in.JobID, 10)), nil, &out); err != nil {
			return nil, nil, err
		}
		return jsonResult(out)
	})
}
