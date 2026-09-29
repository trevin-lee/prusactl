package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/trevin-lee/prusactl/internal/link"
)

// Every command here is a thin wrapper over the MCP tool of the same purpose,
// so the CLI and an agent can do exactly the same things.

func action(pos []string, allowed ...string) (string, []string, error) {
	if len(pos) == 0 {
		return allowed[0], nil, nil
	}
	got := strings.ToLower(pos[0])
	for _, a := range allowed {
		if got == a {
			return a, pos[1:], nil
		}
	}
	return "", nil, fmt.Errorf("unknown action %q; use one of: %s", pos[0], strings.Join(allowed, ", "))
}

// --- printers and status ---------------------------------------------------------

func printersCmd(ctx context.Context, o options, _ []string) error {
	return runTool(ctx, "list_printers", map[string]any{}, o.on("json"), func(raw json.RawMessage) error {
		m := decodeMap(raw)
		// One printer can appear on both routes, under the name each route
		// knows it by, so each row says which route it came from.
		type row struct {
			route string
			p     map[string]any
		}
		var list []row
		for _, p := range rows(m, "connect") {
			list = append(list, row{"connect", p})
		}
		// The direct route reaches one printer, so it answers with that one
		// rather than a list of them.
		if one, ok := m["direct"].(map[string]any); ok && len(one) > 0 {
			list = append(list, row{"direct", one})
		}
		if len(list) == 0 {
			fmt.Println("No printers. Run `prusactl setup` for the one on your network, or `prusactl login` for a Prusa Connect account.")
			return nil
		}
		fmt.Printf("%-24s %-8s %s\n", "NAME", "ROUTE", "STATE / ADDRESS")
		for _, r := range list {
			name := field(r.p, "name")
			if name == "" {
				name = field(r.p, "hostname")
			}
			where := field(r.p, "connect_state")
			if where == "" {
				where = field(r.p, "host")
			}
			fmt.Printf("%-24s %-8s %s\n", name, r.route, where)
		}
		return nil
	})
}

func telemetryCmd(ctx context.Context, o options, _ []string) error {
	args := printerArgs(o)
	if m := o.str("minutes"); m != "" {
		if v, err := strconv.Atoi(m); err == nil {
			args["minutes"] = v
		}
	}
	// Connect answers with a sample per field per time bucket, most of them
	// empty. The table is what a person wants from that: where each reading
	// ended up, and how far it ranged.
	return runTool(ctx, "get_telemetry", args, o.on("json"), func(raw json.RawMessage) error {
		m := decodeMap(raw)
		series, _ := m["data_series"].(map[string]any)
		if len(series) == 0 {
			fmt.Println("No telemetry was recorded.")
			return nil
		}
		names := make([]string, 0, len(series))
		for k := range series {
			names = append(names, k)
		}
		sort.Strings(names)
		fmt.Printf("%-20s %10s %10s %10s\n", "READING", "LATEST", "LOW", "HIGH")
		for _, k := range names {
			vals := numbers(series[k])
			if len(vals) == 0 {
				fmt.Printf("%-20s %10s\n", k, "-")
				continue
			}
			low, high := vals[0], vals[0]
			for _, v := range vals {
				low, high = min(low, v), max(high, v)
			}
			fmt.Printf("%-20s %10.1f %10.1f %10.1f\n", k, vals[len(vals)-1], low, high)
		}
		if p, ok := m["pager"].(map[string]any); ok {
			fmt.Printf("\n%s to %s, a sample every %ss.\n",
				stamp(field(p, "from")), stamp(field(p, "to")), field(p, "granularity"))
		}
		return nil
	})
}

func eventsCmd(ctx context.Context, o options, _ []string) error {
	return runTool(ctx, "list_events", pageArgs(o, printerArgs(o)), o.on("json"), func(raw json.RawMessage) error {
		for _, e := range rows(decodeMap(raw), "events") {
			fmt.Printf("%-20s %-18s %s\n", stamp(field(e, "created")), field(e, "event"), field(e, "command"))
		}
		return nil
	})
}

func transfersCmd(ctx context.Context, o options, _ []string) error {
	return runTool(ctx, "get_transfers", printerArgs(o), o.on("json"), func(raw json.RawMessage) error {
		m := decodeMap(raw)
		t, _ := m["transfer"].(map[string]any)
		if len(t) == 0 {
			fmt.Println("Nothing is being transferred.")
			return nil
		}
		name := field(t, "display_name")
		if name == "" {
			name = field(t, "name")
		}
		fmt.Printf("%s\n  %s", name, field(t, "type"))
		if p := field(t, "progress"); p != "" {
			fmt.Printf(", %s%%", p)
		}
		if s := field(t, "transferred"); s != "" {
			fmt.Printf(", %s of %s bytes", s, field(t, "size"))
		}
		fmt.Println()
		return nil
	})
}

// --- printing --------------------------------------------------------------------

// plateClear settles the "is the plate empty?" question the tools require after
// a finished or stopped print. An agent passes a flag; a person is asked.
func plateClear(o options) (map[string]any, error) {
	args := printerArgs(o)
	if o.on("plate-clear") {
		args["plate_clear"] = true
		return args, nil
	}
	return args, nil
}

func withPlateConfirm(ctx context.Context, o options, name string, args map[string]any, render func(json.RawMessage) error) error {
	err := runTool(ctx, name, args, o.on("json"), render)
	if err == nil || !strings.Contains(err.Error(), "plate_clear") {
		return err
	}
	// The printer is FINISHED or STOPPED: the last part may still be there.
	ok, cerr := confirm("The last print may still be on the plate. Is the plate clear?")
	if cerr != nil {
		return cerr
	}
	if !ok {
		return errors.New("stopped: clear the plate, then run this again")
	}
	args["plate_clear"] = true
	return runTool(ctx, name, args, o.on("json"), render)
}

// started reports what a job-starting tool did, whichever route ran it.
func started(raw json.RawMessage) error {
	m := decodeMap(raw)
	what := field(m, "started")
	if what == "" {
		what = field(m, "uploaded")
	}
	if what == "" {
		return printJSON(raw)
	}
	fmt.Printf("Printing %s.\n", what)
	if st := field(m, "state"); st != "" {
		fmt.Printf("The printer is %s.\n", st)
	}
	return nil
}

func printCmd(ctx context.Context, o options, pos []string) error {
	if len(pos) != 1 {
		return errors.New("print: usage: prusactl print FILE (a sliced .bgcode or .gcode on this computer)")
	}
	path, err := filepath.Abs(pos[0])
	if err != nil {
		return err
	}
	args, err := plateClear(o)
	if err != nil {
		return err
	}
	args["local_path"] = path
	args["then"] = "print"
	if d := o.str("destination"); d != "" {
		args["destination"] = d
	}
	if o.on("overwrite") {
		args["overwrite"] = true
	}
	return withPlateConfirm(ctx, o, "upload_file", args, started)
}

func startCmd(ctx context.Context, o options, pos []string) error {
	if len(pos) != 1 {
		return errors.New("start: usage: prusactl start PATH (a file already on the printer, e.g. /usb/part.bgcode)")
	}
	args, err := plateClear(o)
	if err != nil {
		return err
	}
	args["path"] = pos[0]
	return withPlateConfirm(ctx, o, "start_print", args, started)
}

func controlCmd(act string) func(context.Context, options, []string) error {
	return func(ctx context.Context, o options, _ []string) error {
		args := printerArgs(o)
		args["action"] = act
		return runTool(ctx, "control_print", args, o.on("json"), func(raw json.RawMessage) error {
			m := decodeMap(raw)
			fmt.Printf("%s: %s (job %s was %s)\n", field(m, "printer"), act, field(m, "job_id"), field(m, "state_before"))
			return nil
		})
	}
}

func gcodeCmd(ctx context.Context, o options, pos []string) error {
	if len(pos) == 0 {
		return errors.New(`gcode: usage: prusactl gcode "G28" ["M104 S215" ...]`)
	}
	args, err := plateClear(o)
	if err != nil {
		return err
	}
	args["gcode"] = strings.Join(pos, "\n")
	return withPlateConfirm(ctx, o, "run_gcode", args, func(raw json.RawMessage) error {
		m := decodeMap(raw)
		fmt.Printf("Sent %s line(s) of G-code to the printer as %s.\n", field(m, "lines"), field(m, "started"))
		fmt.Println("It runs as a one-off job; the printer is busy until it ends.")
		return nil
	})
}

func dialogCmd(ctx context.Context, o options, pos []string) error {
	if len(pos) != 1 {
		return errors.New("dialog: usage: prusactl dialog BUTTON (the label get_printer shows in dialog_info)")
	}
	args := printerArgs(o)
	args["button"] = pos[0]
	return runTool(ctx, "respond_to_dialog", args, o.on("json"), nil)
}

// --- files on the printer ----------------------------------------------------------

func filesCmd(ctx context.Context, o options, pos []string) error {
	act, rest, err := action(pos, "ls", "get", "put", "rm")
	if err != nil {
		return err
	}
	switch act {
	case "ls":
		args := pageArgs(o, printerArgs(o))
		if len(rest) > 0 {
			args["path"] = rest[0]
		}
		return runTool(ctx, "list_printer_files", args, o.on("json"), func(raw json.RawMessage) error {
			m := decodeMap(raw)
			files, _ := m["files"].(map[string]any)
			if files == nil {
				return printJSON(raw)
			}
			for _, e := range rows(files, "entries") {
				size := field(e, "size")
				if size == "" {
					size = "-"
				}
				fmt.Printf("%-12s %10s  %s\n", field(e, "type"), size, field(e, "path"))
			}
			if n := field(files, "next_offset"); n != "" {
				fmt.Printf("\n%d of %s shown; next page: --offset %s\n", len(rows(files, "entries")), field(files, "total"), n)
			}
			return nil
		})
	case "get":
		lc, lcErr := link.Open()
		return download(ctx, lc, lcErr, rest)
	case "put":
		if len(rest) != 1 {
			return errors.New("files put: usage: prusactl files put FILE")
		}
		path, err := filepath.Abs(rest[0])
		if err != nil {
			return err
		}
		args := printerArgs(o)
		args["local_path"], args["then"] = path, "none"
		if d := o.str("destination"); d != "" {
			args["destination"] = d
		}
		if o.on("overwrite") {
			args["overwrite"] = true
		}
		return runTool(ctx, "upload_file", args, o.on("json"), func(raw json.RawMessage) error {
			m := decodeMap(raw)
			fmt.Printf("Uploaded %s to %s.\n", filepath.Base(path), field(m, "uploaded"))
			if h := field(m, "hash"); h != "" {
				fmt.Printf("Prusa Connect storage hash: %s\n", h)
			}
			return nil
		})
	default: // rm
		if len(rest) == 0 {
			return errors.New("files rm: usage: prusactl files rm PATH [PATH...]")
		}
		args := printerArgs(o)
		args["paths"] = rest
		return runTool(ctx, "delete_printer_files", args, o.on("json"), func(raw json.RawMessage) error {
			fmt.Printf("Deleted %s.\n", strings.Join(rest, ", "))
			return nil
		})
	}
}

// --- files in Prusa Connect ---------------------------------------------------------

func cloudCmd(ctx context.Context, o options, pos []string) error {
	act, rest, err := action(pos, "ls", "rm")
	if err != nil {
		return err
	}
	if act == "rm" {
		if len(rest) == 0 {
			return errors.New("cloud rm: usage: prusactl cloud rm HASH [HASH...]")
		}
		return runTool(ctx, "delete_connect_files", map[string]any{"hashes": rest}, o.on("json"), func(raw json.RawMessage) error {
			// Connect answers 204 whether or not the hash was there, so this
			// says what was asked, not what existed.
			fmt.Printf("Asked Prusa Connect to delete %d file(s) from its storage.\n", len(rest))
			return nil
		})
	}
	return runTool(ctx, "list_connect_files", pageArgs(o, map[string]any{}), o.on("json"), func(raw json.RawMessage) error {
		for _, f := range rows(decodeMap(raw), "files") {
			fmt.Printf("%-44s %10s  %s\n", field(f, "hash"), field(f, "size"), field(f, "display_name"))
		}
		return nil
	})
}

// --- the print queue -----------------------------------------------------------------

func queueCmd(ctx context.Context, o options, pos []string) error {
	act, rest, err := action(pos, "ls", "add", "rm")
	if err != nil {
		return err
	}
	switch act {
	case "ls":
		return runTool(ctx, "get_queue", pageArgs(o, printerArgs(o)), o.on("json"), func(raw json.RawMessage) error {
			m := decodeMap(raw)
			q, _ := m["queue"].(map[string]any)
			if q == nil {
				return printJSON(raw)
			}
			jobs := rows(q, "planned_jobs")
			if len(jobs) == 0 {
				jobs = rows(q, "queue")
			}
			if len(jobs) == 0 {
				fmt.Println("The queue is empty.")
			}
			for _, j := range jobs {
				name := field(j, "file", "display_name")
				if name == "" {
					name = field(j, "path")
				}
				fmt.Printf("%-10s %s\n", field(j, "id"), name)
			}
			return nil
		})
	case "add":
		if len(rest) != 1 {
			return errors.New("queue add: usage: prusactl queue add PATH-ON-PRINTER | --hash HASH")
		}
		args := printerArgs(o)
		if o.on("hash") {
			args["hash"] = rest[0]
		} else {
			args["path"] = rest[0]
		}
		if p := o.str("position"); p != "" {
			if v, err := strconv.Atoi(p); err == nil {
				args["position"] = v
			}
		}
		return runTool(ctx, "add_to_queue", args, o.on("json"), func(raw json.RawMessage) error {
			m := decodeMap(raw)
			q, _ := m["queued"].(map[string]any)
			name := field(q, "file", "display_name")
			if name == "" {
				name = field(q, "file", "name")
			}
			fmt.Printf("Queued %s as job %s (%s).\n", name, field(q, "id"), field(q, "state"))
			return nil
		})
	default: // rm
		if len(rest) != 1 {
			return errors.New("queue rm: usage: prusactl queue rm JOB-ID")
		}
		id, err := strconv.ParseInt(rest[0], 10, 64)
		if err != nil {
			return fmt.Errorf("queue rm: %q is not a job id", rest[0])
		}
		args := printerArgs(o)
		args["job_id"] = id
		return runTool(ctx, "remove_from_queue", args, o.on("json"), func(raw json.RawMessage) error {
			fmt.Printf("Removed job %d from the queue.\n", id)
			return nil
		})
	}
}

// --- job history ----------------------------------------------------------------------

func jobsCmd(ctx context.Context, o options, pos []string) error {
	if len(pos) == 1 {
		id, err := strconv.ParseInt(pos[0], 10, 64)
		if err != nil {
			return fmt.Errorf("jobs: %q is not a job id", pos[0])
		}
		args := printerArgs(o)
		args["job_id"] = id
		return runTool(ctx, "get_job", args, o.on("json"), func(raw json.RawMessage) error {
			j := decodeMap(raw)
			name := field(j, "file", "display_name")
			if name == "" {
				name = field(j, "path")
			}
			fmt.Printf("%s\n", name)
			fmt.Printf("  job %s, %s\n", field(j, "id"), field(j, "state"))
			if st := stamp(field(j, "start")); st != "" {
				fmt.Printf("  started %s\n", st)
			}
			if en := stamp(field(j, "end")); en != "" {
				fmt.Printf("  ended   %s\n", en)
			}
			if h := field(j, "print_height"); h != "" {
				fmt.Printf("  printed %s mm tall\n", h)
			}
			return nil
		})
	}
	args := pageArgs(o, printerArgs(o))
	if st := o.str("state"); st != "" {
		args["states"] = strings.Split(st, ",")
	}
	return runTool(ctx, "list_jobs", args, o.on("json"), func(raw json.RawMessage) error {
		for _, j := range rows(decodeMap(raw), "jobs") {
			name := field(j, "file", "display_name")
			if name == "" {
				name = field(j, "file", "name")
			}
			fmt.Printf("%-8s %-12s %s\n", field(j, "id"), field(j, "state"), name)
		}
		return nil
	})
}

// --- firmware commands -------------------------------------------------------------------

func cmdCmd(ctx context.Context, o options, pos []string) error {
	act, rest, err := action(pos, "ls", "send", "status")
	if err != nil {
		return err
	}
	switch act {
	case "ls":
		args := printerArgs(o)
		if o.on("now") {
			args["executable_now"] = true
		}
		return runTool(ctx, "list_supported_commands", args, o.on("json"), func(raw json.RawMessage) error {
			seen := map[string]bool{}
			for _, c := range rows(decodeMap(raw), "commands") {
				name := field(c, "command")
				if seen[name] { // Connect lists some commands twice
					continue
				}
				seen[name] = true
				fmt.Printf("%-28s %s\n", name, field(c, "description"))
				for _, a := range rows(c, "args") {
					req := ""
					if field(a, "required") == "true" {
						req = " (required)"
					}
					fmt.Printf("%-28s   %s: %s%s\n", "", field(a, "name"), field(a, "type"), req)
				}
			}
			return nil
		})
	case "status":
		if len(rest) != 1 {
			return errors.New("cmd status: usage: prusactl cmd status COMMAND-ID")
		}
		id, err := strconv.ParseInt(rest[0], 10, 64)
		if err != nil {
			return fmt.Errorf("cmd status: %q is not a command id", rest[0])
		}
		args := printerArgs(o)
		args["command_id"] = id
		return runTool(ctx, "get_command", args, o.on("json"), func(raw json.RawMessage) error {
			m := decodeMap(raw)
			fmt.Printf("%s %s is %s\n", field(m, "command_id"), field(m, "command"), field(m, "state"))
			for _, e := range rows(m, "events") {
				fmt.Printf("  %-20s %s %s\n", stamp(field(e, "created")), field(e, "event"), field(e, "reason"))
			}
			return nil
		})
	default: // send
		if len(rest) == 0 {
			return errors.New(`cmd send: usage: prusactl cmd send NAME [key=value ...], e.g. cmd send SET_NOZZLE_TEMPERATURE nozzle_temperature=215; "prusactl cmd ls" names the arguments each command takes`)
		}
		args, err := plateClear(o)
		if err != nil {
			return err
		}
		args["command"] = rest[0]
		if kw := parseKwargs(rest[1:]); len(kw) > 0 {
			args["kwargs"] = kw
		}
		return withPlateConfirm(ctx, o, "send_command", args, func(raw json.RawMessage) error {
			m := decodeMap(raw)
			c, _ := m["response"].(map[string]any)
			fmt.Printf("%s is %s", field(m, "command"), field(c, "command", "state"))
			if id := field(c, "command", "id"); id != "" {
				fmt.Printf(" (command %s; `prusactl cmd status %s` follows it)", id, id)
			}
			fmt.Println()
			return nil
		})
	}
}

// parseKwargs turns key=value arguments into typed command arguments.
func parseKwargs(pairs []string) map[string]any {
	out := map[string]any{}
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			continue
		}
		switch {
		case v == "true":
			out[k] = true
		case v == "false":
			out[k] = false
		default:
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				out[k] = f
			} else {
				out[k] = v
			}
		}
	}
	return out
}

// --- camera -------------------------------------------------------------------------------

func cameraCmd(ctx context.Context, o options, pos []string) error {
	tc, err := openTools(ctx)
	if err != nil {
		return err
	}
	defer tc.close()
	args := printerArgs(o)
	if id := o.str("camera"); id != "" {
		args["camera_id"] = id
	}
	img, mime, note, err := tc.image(ctx, "get_camera_snapshot", args)
	if err != nil {
		return err
	}
	dest := ""
	if len(pos) == 1 {
		dest = pos[0]
	}
	if dest == "" && !stdoutIsFile() {
		ext := ".jpg"
		if strings.Contains(mime, "png") {
			ext = ".png"
		}
		dest = "snapshot" + ext
	}
	if dest == "" {
		_, err := os.Stdout.Write(img)
		return err
	}
	if err := os.WriteFile(dest, img, 0o644); err != nil {
		return err
	}
	fmt.Printf("Saved %s (%d bytes). %s\n", dest, len(img), note)
	return nil
}
