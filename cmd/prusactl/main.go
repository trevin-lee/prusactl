// Command prusactl controls a Prusa printer from the shell or, as an MCP
// server (`prusactl mcp`), from an AI agent. It reaches the printer directly
// on the local network through PrusaLink, and optionally through Prusa
// Connect from anywhere.
package main

import (
	"bufio"
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

	"golang.org/x/term"

	"github.com/trevin-lee/prusactl/internal/auth"
	"github.com/trevin-lee/prusactl/internal/connect"
	"github.com/trevin-lee/prusactl/internal/link"
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

const usage = `prusactl: control a Prusa printer from the terminal or an AI agent.

Usage:
  prusactl setup [ADDRESS]           connect directly to the printer on your network
  prusactl login                     optional: sign in to Prusa Connect (remote access,
                                     camera, dialogs, queue, history)
  prusactl logout                    forget the Prusa Connect session
  prusactl status                    printer state and how it is reachable
  prusactl mcp                       run the MCP server on stdio
  prusactl download PATH [DEST]      copy a file from the printer's storage to this
                                     computer, e.g. /usb/part.bgcode
  prusactl api [METHOD] PATH [JSON]  call an API directly: /api/... goes to the
                                     printer, /app/... to Prusa Connect
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
	cc := connect.New(session, "prusactl/"+buildVersion())
	lc, lcErr := link.Open()

	switch args[0] {
	case "setup":
		return setup(ctx, args[1:])
	case "login":
		return login(ctx, session, cc, lc, lcErr)
	case "logout":
		if err := session.Logout(); err != nil {
			return err
		}
		fmt.Println("Signed out of Prusa Connect; the session was removed from the keychain.")
		return nil
	case "status":
		return status(ctx, server.New(session, cc, lc, lcErr, buildVersion()), lc)
	case "mcp":
		return server.New(session, cc, lc, lcErr, buildVersion()).Run(ctx)
	case "download":
		return download(ctx, lc, lcErr, args[1:])
	case "api":
		return apiCommand(ctx, cc, lc, lcErr, args[1:])
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

var stdin = bufio.NewReader(os.Stdin)

func ask(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func askSecret(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("run this in an interactive terminal (or pass --password-stdin)")
	}
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	return string(b), err
}

func setup(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	user := fs.String("user", "maker", "PrusaLink username")
	apiKey := fs.Bool("api-key", false, "authenticate with a PrusaLink API key instead of the password")
	fromStdin := fs.Bool("password-stdin", false, "read the password or API key from stdin")
	forget := fs.Bool("forget", false, "remove the saved printer and its password")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *forget {
		cfg, err := link.LoadConfig()
		if errors.Is(err, link.ErrNotConfigured) {
			fmt.Println("No printer is set up.")
			return nil
		}
		if err != nil {
			return err
		}
		if err := link.RemoveConfig(cfg); err != nil {
			return err
		}
		fmt.Printf("Forgot %s and its saved password.\n", cfg.Host)
		return nil
	}

	address := fs.Arg(0)
	if address == "" {
		var err error
		if address, err = ask("Printer address (IP or hostname; on the printer: Settings > Network): "); err != nil {
			return err
		}
	}
	host, err := link.NormalizeHost(address)
	if err != nil {
		return err
	}
	cfg := link.Config{Host: host, User: *user, Auth: link.AuthDigest}
	what := "PrusaLink password (on the printer: Settings > Network > PrusaLink)"
	if *apiKey {
		cfg.Auth, what = link.AuthAPIKey, "PrusaLink API key"
	}

	var secret string
	if *fromStdin {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
		if err != nil {
			return err
		}
		secret = strings.TrimSpace(string(b))
	} else if secret, err = askSecret(what + ": "); err != nil {
		return err
	}
	if secret == "" {
		return errors.New("empty password")
	}

	vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var info struct {
		Name, Hostname, Serial string
	}
	if _, err := link.New(cfg, secret).Get(vctx, "/api/v1/info", &info); err != nil {
		return fmt.Errorf("checking the printer: %w", err)
	}
	if err := cfg.SaveSecret(secret); err != nil {
		return fmt.Errorf("saving the password to the keychain: %w", err)
	}
	if err := link.SaveConfig(cfg); err != nil {
		return err
	}
	name := info.Name
	if name == "" {
		name = info.Hostname
	}
	fmt.Printf("Connected to %s at %s. The password is saved in your keychain.\n", name, host)
	return nil
}

// terminalPrompter asks for Prusa Account details on the terminal.
type terminalPrompter struct{}

func (terminalPrompter) Email() (string, error) { return ask("Prusa Account email: ") }
func (terminalPrompter) Password() (string, error) {
	return askSecret("Password (sent only to account.prusa3d.com, never saved): ")
}
func (terminalPrompter) OneTimeCode() (string, error) {
	return ask("Two-factor code from your authenticator app: ")
}

func login(ctx context.Context, session *auth.Session, cc *connect.Client, lc *link.Client, lcErr error) error {
	fmt.Fprintln(os.Stderr, "Signing in to Prusa Connect with your Prusa Account.")
	if _, err := session.Login(ctx, terminalPrompter{}); err != nil {
		return err
	}
	if err := server.RegisterUser(ctx, cc); err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
	fmt.Println("Signed in. The session is saved in your keychain and renews itself.")
	return status(ctx, server.New(session, cc, lc, lcErr, buildVersion()), lc)
}

func status(ctx context.Context, srv *server.Server, lc *link.Client) error {
	st := srv.Status(ctx)
	direct, _ := st["direct"].(map[string]any)
	cloud, _ := st["connect"].(map[string]any)

	switch {
	case direct["reachable"] == true:
		var s struct {
			Printer map[string]any `json:"printer"`
			Job     map[string]any `json:"job"`
		}
		line := "reachable"
		if _, err := lc.Get(ctx, "/api/v1/status", &s); err == nil {
			line = fmt.Sprint(s.Printer["state"])
			if p, ok := s.Job["progress"]; ok {
				line += fmt.Sprintf(" %v%%", p)
			}
			line += fmt.Sprintf(", nozzle %v/%v°C, bed %v/%v°C",
				s.Printer["temp_nozzle"], s.Printer["target_nozzle"], s.Printer["temp_bed"], s.Printer["target_bed"])
		}
		fmt.Printf("Printer (direct):  %s: %s\n", direct["host"], line)
	case direct["configured"] == true:
		fmt.Printf("Printer (direct):  %s: not reachable (%v)\n", direct["host"], direct["error"])
	default:
		fmt.Printf("Printer (direct):  not set up: %v\n", orText(direct["setup"], direct["error"]))
	}

	if cloud["signed_in"] == true {
		who := "signed in"
		var me struct {
			User struct {
				PublicName string `json:"public_name"`
				FirstName  string `json:"first_name"`
				LastName   string `json:"last_name"`
			} `json:"user"`
		}
		if raw, ok := cloud["user"].(json.RawMessage); ok && json.Unmarshal(raw, &me) == nil && me.User.PublicName != "" {
			who = "signed in as " + me.User.PublicName
			if full := strings.TrimSpace(me.User.FirstName + " " + me.User.LastName); full != "" {
				who += " (" + full + ")"
			}
		}
		if e, ok := cloud["error"]; ok {
			who += fmt.Sprintf(" (error: %v)", e)
		}
		fmt.Printf("Prusa Connect:     %s\n", who)
	} else {
		fmt.Printf("Prusa Connect:     not signed in (%v)\n", orText(cloud["error"], cloud["setup"]))
	}
	return nil
}

func orText(a, b any) any {
	if a != nil {
		return a
	}
	return b
}

func download(ctx context.Context, lc *link.Client, lcErr error, args []string) error {
	fs := flag.NewFlagSet("download", flag.ContinueOnError)
	overwrite := fs.Bool("overwrite", false, "replace DEST if it already exists")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 || fs.NArg() > 2 {
		return errors.New("download: usage: prusactl download [--overwrite] PATH [DEST], e.g. /usb/part.bgcode")
	}
	if lc == nil {
		return lcErr
	}
	path, n, err := lc.Download(ctx, fs.Arg(0), fs.Arg(1), *overwrite)
	if err != nil {
		return err
	}
	fmt.Printf("Saved %s (%d bytes).\n", path, n)
	return nil
}

func apiCommand(ctx context.Context, cc *connect.Client, lc *link.Client, lcErr error, args []string) error {
	method := http.MethodGet
	if len(args) > 0 && !strings.HasPrefix(args[0], "/") {
		method = strings.ToUpper(args[0])
		args = args[1:]
	}
	if len(args) == 0 {
		return errors.New("api: missing PATH, e.g. /api/v1/status (printer) or /app/printers (Connect)")
	}
	u, err := url.Parse(args[0])
	if err != nil {
		return err
	}
	var body []byte
	if len(args) > 1 {
		if !json.Valid([]byte(args[1])) {
			return errors.New("api: body is not valid JSON")
		}
		body = []byte(args[1])
	}

	var resp *http.Response
	switch {
	case strings.HasPrefix(u.Path, "/api/"):
		if lc == nil {
			return lcErr
		}
		req := link.Request{Method: method, Path: u.Path, Query: u.Query()}
		if body != nil {
			req.Body = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
			req.ContentLength, req.ContentType = int64(len(body)), "application/json"
		}
		resp, err = lc.Do(ctx, req)
	case strings.HasPrefix(u.Path, "/app/"):
		req := connect.Request{Method: method, Path: u.Path, Query: u.Query()}
		if body != nil {
			req.JSON = json.RawMessage(body)
		}
		resp, err = cc.Do(ctx, req)
	default:
		return errors.New("api: PATH must start with /api/ (printer) or /app/ (Prusa Connect)")
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, out, "", "  ") == nil {
		out = pretty.Bytes()
	}
	fmt.Fprintf(os.Stderr, "%d %s\n", resp.StatusCode, http.StatusText(resp.StatusCode))
	os.Stdout.Write(out)
	if len(out) > 0 {
		fmt.Println()
	}
	return nil
}
