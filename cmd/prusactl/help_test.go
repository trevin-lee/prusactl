package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Every command in the table must be handled by run(): asking for its help is
// enough to prove it's dispatched, and doesn't touch the network or keychain.
func TestEveryCommandHasHelp(t *testing.T) {
	for _, c := range commands {
		if err := run(context.Background(), []string{c.Name, "--help"}); err != nil {
			t.Errorf("%s --help: %v", c.Name, err)
		}
		if err := run(context.Background(), []string{"help", c.Name}); err != nil {
			t.Errorf("help %s: %v", c.Name, err)
		}
		if c.Summary == "" || c.Help == "" {
			t.Errorf("%s has no summary or help text", c.Name)
		}
	}
}

func TestCompletionsCoverEveryCommandAndFlag(t *testing.T) {
	for shell, write := range map[string]func(*bytes.Buffer){
		"bash": func(b *bytes.Buffer) { writeBashCompletion(b) },
		"zsh":  func(b *bytes.Buffer) { writeZshCompletion(b) },
		"fish": func(b *bytes.Buffer) { writeFishCompletion(b) },
	} {
		var b bytes.Buffer
		write(&b)
		script := b.String()
		for _, c := range listed() {
			if !strings.Contains(script, c.Name) {
				t.Errorf("%s completion lacks the %s command", shell, c.Name)
			}
			for _, f := range c.Flags {
				want := "--" + f.Name
				if shell == "fish" {
					want = "-l " + f.Name
				}
				if !strings.Contains(script, want) {
					t.Errorf("%s completion lacks %s %s", shell, c.Name, want)
				}
			}
		}
		// Syntax-check with the shell itself when it's installed.
		bin := shell
		if _, err := exec.LookPath(bin); err != nil {
			continue
		}
		file := filepath.Join(t.TempDir(), "prusactl."+shell)
		if err := os.WriteFile(file, b.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(bin, "-n", file).CombinedOutput(); err != nil {
			t.Errorf("%s -n: %v\n%s", shell, err, out)
		}
	}
}

func TestFlagsParseAnywhere(t *testing.T) {
	opts, pos, err := lookup("setup").parse([]string{"10.0.0.5", "--api-key", "--user", "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pos) != 1 || pos[0] != "10.0.0.5" || !opts.on("api-key") || opts.str("user") != "bob" || opts.on("forget") {
		t.Fatalf("pos=%v api-key=%v user=%q", pos, opts.on("api-key"), opts.str("user"))
	}
	if opts, _, _ := lookup("setup").parse(nil); opts.str("user") != "maker" {
		t.Fatalf("default user = %q", opts.str("user"))
	}
	if _, _, err := lookup("setup").parse([]string{"--bogus"}); err == nil || !strings.Contains(err.Error(), "unknown flag --bogus") {
		t.Fatalf("err = %v", err)
	}
	// api goes through the same parser, so a typo isn't sent as the HTTP method.
	if _, _, err := lookup("api").parse([]string{"--foo", "/api/v1/status"}); err == nil || !strings.Contains(err.Error(), "unknown flag --foo") {
		t.Fatalf("api err = %v", err)
	}
	if opts, pos, err := lookup("api").parse([]string{"/api/v1/status", "--raw"}); err != nil || !opts.on("raw") || len(pos) != 1 {
		t.Fatalf("api --raw after the path: %v %v", pos, err)
	}
}

func TestSuggestion(t *testing.T) {
	for typo, want := range map[string]string{"stauts": "status", "dowload": "download", "comp": "completion", "zzzz": ""} {
		if got := suggest(typo); got != want {
			t.Errorf("suggest(%q) = %q, want %q", typo, got, want)
		}
	}
}
