// Command prusactl drives a Prusa printer through Prusa Connect, as an MCP
// server for AI agents (`prusactl mcp`) or from the shell.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"time"

	"github.com/trevin-lee/prusactl/internal/auth"
	"github.com/trevin-lee/prusactl/internal/connect"
	"github.com/trevin-lee/prusactl/internal/server"
)

var version = "" // set with -ldflags "-X main.version=..."

func buildVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

const usage = `prusactl: control a Prusa printer through Prusa Connect.

Usage:
  prusactl login [--timeout 10m]   sign in through a Chrome window
  prusactl logout                  forget the saved session
  prusactl status                  show who is signed in
  prusactl mcp                     run the MCP server on stdio
  prusactl api [METHOD] PATH [JSON-BODY]
                                   call the Connect API directly, e.g.
                                   prusactl api /app/printers
  prusactl version
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "prusactl:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	session := auth.NewSession()
	ua := "prusactl/" + buildVersion()
	client := connect.New(session, ua)

	switch args[0] {
	case "login":
		fs := flag.NewFlagSet("login", flag.ContinueOnError)
		timeout := fs.Duration("timeout", 10*time.Minute, "how long to wait for sign-in")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Opening a Chrome window at Prusa's sign-in page…")
		if _, err := session.Login(ctx, *timeout); err != nil {
			return err
		}
		if err := server.RegisterUser(ctx, client); err != nil {
			fmt.Fprintln(os.Stderr, "warning:", err)
		}
		return printStatus(ctx, client)
	case "logout":
		if err := session.Logout(); err != nil {
			return err
		}
		fmt.Println("Signed out; the saved session was removed from the keychain.")
		return nil
	case "status":
		return printStatus(ctx, client)
	case "mcp":
		return server.New(session, client, buildVersion()).Run(ctx)
	case "api":
		return apiCommand(ctx, client, args[1:])
	case "version":
		fmt.Println(buildVersion())
		return nil
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], usage)
	}
}

func printStatus(ctx context.Context, client *connect.Client) error {
	me, err := server.WhoAmI(ctx, client)
	if err != nil {
		return err
	}
	out, _ := json.MarshalIndent(me, "", "  ")
	fmt.Println(string(out))
	return nil
}

func apiCommand(ctx context.Context, client *connect.Client, args []string) error {
	method := http.MethodGet
	if len(args) > 0 && !strings.HasPrefix(args[0], "/") {
		method = strings.ToUpper(args[0])
		args = args[1:]
	}
	if len(args) == 0 {
		return errors.New("api: missing PATH, e.g. /app/printers")
	}
	u, err := url.Parse(args[0])
	if err != nil {
		return err
	}
	req := connect.Request{Method: method, Path: u.Path, Query: u.Query()}
	if len(args) > 1 {
		if !json.Valid([]byte(args[1])) {
			return errors.New("api: body is not valid JSON")
		}
		req.JSON = json.RawMessage(args[1])
	}
	resp, err := client.Do(ctx, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, body, "", "  ") == nil {
		body = pretty.Bytes()
	}
	fmt.Fprintf(os.Stderr, "%d %s (%s)\n", resp.StatusCode, http.StatusText(resp.StatusCode), resp.Header.Get("Content-Type"))
	os.Stdout.Write(body)
	fmt.Println()
	return nil
}
