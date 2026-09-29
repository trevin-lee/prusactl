package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// The command table is the one source for the usage text, `prusactl help
// COMMAND`, the shell completion scripts, and the flags setup and download
// parse, so none of them can drift from the others.

type flagSpec struct {
	Name    string // without dashes
	Value   string // placeholder when the flag takes a value, e.g. "NAME"; empty for a switch
	Default string
	Usage   string
}

// posArg describes a positional argument for shell completion. Kind is
// "hosts", "files", "commands", "" (nothing to suggest), or a space-separated
// list of literal choices.
type posArg struct {
	Label string
	Kind  string
}

type command struct {
	Name    string
	Args    string // synopsis after the name
	Summary string
	Help    string
	Flags   []flagSpec
	Pos     []posArg
}

var commands = []*command{
	{
		Name:    "setup",
		Args:    "[flags] [ADDRESS]",
		Summary: "Connect directly to the printer on your network",
		Help: `Saves the printer's address, checks that it answers, and saves its PrusaLink
password (or API key) in your keychain, or in a private file on machines
without one. ADDRESS is its IP address or hostname, e.g. 192.168.1.50; you're
asked for it if you leave it out. The password is on the printer under
Settings > Network > PrusaLink.`,
		Flags: []flagSpec{
			{Name: "user", Value: "NAME", Default: "maker", Usage: "PrusaLink username"},
			{Name: "api-key", Usage: "use a PrusaLink API key instead of the password"},
			{Name: "password-stdin", Usage: "read the password or API key from stdin"},
			{Name: "forget", Usage: "remove the saved printer and its password"},
		},
		Pos: []posArg{{Label: "printer address", Kind: "hosts"}},
	},
	{
		Name:    "login",
		Summary: "Sign in to Prusa Connect (optional)",
		Help: `Asks for your Prusa Account email, password, and two-factor code in the
terminal. The password goes only to account.prusa3d.com and is never saved;
the session is saved and renews itself. Prusa Connect adds remote access, the
camera, on-screen dialogs, the print queue, and history.`,
	},
	{
		Name:    "logout",
		Summary: "Forget the Prusa Connect session",
		Help:    `Removes the saved Prusa Connect session from this computer.`,
	},
	{
		Name:    "status",
		Summary: "Show the printer's state and how it's reachable",
		Help:    `Shows whether the printer answers directly and whether Prusa Connect is signed in.`,
	},
	{
		Name:    "mcp",
		Summary: "Run the MCP server on stdio, for AI agents",
		Help: `Speaks the Model Context Protocol over stdin and stdout, so an AI agent can
run the printer. Add it to Claude Code with:

  claude mcp add --scope user prusa -- "$(command -v prusactl)" mcp

That saves the full path, so it works even when the app isn't started from a
terminal that has your PATH.`,
	},
	{
		Name:    "download",
		Args:    "[--overwrite] PATH [DEST]",
		Summary: "Copy a file from the printer to this computer",
		Help: `PATH is a file on the printer's storage, e.g. /usb/part.bgcode. DEST is a
file or folder on this computer; the default is the current folder.`,
		Flags: []flagSpec{{Name: "overwrite", Usage: "replace DEST if it already exists"}},
		Pos:   []posArg{{Label: "printer path"}, {Label: "destination", Kind: "files"}},
	},
	{
		Name:    "api",
		Args:    "[--raw] [METHOD] PATH [JSON]",
		Summary: "Call the printer's or Prusa Connect's API directly",
		Help: `Paths starting with /api/ go to the printer (PrusaLink), /app/ to Prusa
Connect. METHOD defaults to GET; JSON is the request body. API keys, tokens,
and passwords in the response are shown as [redacted] unless you pass --raw.`,
		Flags: []flagSpec{{Name: "raw", Usage: "show API keys and tokens in the response"}},
		Pos:   []posArg{{Label: "method or path", Kind: "GET POST PUT PATCH DELETE"}},
	},
	{
		Name:    "completion",
		Args:    "SHELL",
		Summary: "Print a shell completion script (bash, zsh, fish)",
		Help: `Prints a script that makes Tab complete prusactl's commands and flags.
Homebrew installs these for you. Otherwise:

  zsh:   prusactl completion zsh > "${fpath[1]}/_prusactl"   (then open a new shell)
  bash:  echo 'source <(prusactl completion bash)' >> ~/.bashrc
  fish:  prusactl completion fish > ~/.config/fish/completions/prusactl.fish`,
		Pos: []posArg{{Label: "shell", Kind: "bash zsh fish"}},
	},
	{
		Name:    "version",
		Summary: "Print the version",
		Help:    `Prints prusactl's version. prusactl --version does the same.`,
	},
	{
		Name:    "help",
		Args:    "[COMMAND]",
		Summary: "Show help for a command",
		Help:    `Shows the command list, or the details of one command.`,
		Pos:     []posArg{{Label: "command", Kind: "commands"}},
	},
}

func lookup(name string) *command {
	for _, c := range commands {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func commandNames() []string {
	names := make([]string, len(commands))
	for i, c := range commands {
		names[i] = c.Name
	}
	return names
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, "prusactl: hand your Prusa printer to an AI agent (`prusactl mcp`).\nThese commands set it up and check on it.\n\n")
	fmt.Fprint(w, "Usage:\n  prusactl <command> [arguments]\n\nCommands:\n")
	for _, c := range commands {
		fmt.Fprintf(w, "  %-11s %s\n", c.Name, c.Summary)
	}
	fmt.Fprint(w, "\nRun `prusactl help <command>` for details, or see https://github.com/trevin-lee/prusactl\n")
}

func (c *command) printHelp(w io.Writer) {
	fmt.Fprintf(w, "Usage: prusactl %s", c.Name)
	if c.Args != "" {
		fmt.Fprintf(w, " %s", c.Args)
	}
	fmt.Fprintf(w, "\n\n%s.\n\n%s\n", c.Summary, c.Help)
	if len(c.Flags) > 0 {
		fmt.Fprint(w, "\nFlags:\n")
		for _, f := range c.Flags {
			name := "--" + f.Name
			if f.Value != "" {
				name += " " + f.Value
			}
			usage := f.Usage
			if f.Default != "" {
				usage += fmt.Sprintf(" (default %q)", f.Default)
			}
			fmt.Fprintf(w, "  %-20s %s\n", name, usage)
		}
	}
}

// wantsHelp reports whether args ask for help, as in `prusactl setup --help`.
func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "-help" {
			return true
		}
	}
	return false
}

func helpCommand(args []string) error {
	switch len(args) {
	case 0:
		printUsage(os.Stdout)
		return nil
	case 1:
		c := lookup(args[0])
		if c == nil {
			return unknownCommand(args[0])
		}
		c.printHelp(os.Stdout)
		return nil
	}
	return errors.New("help: usage: prusactl help [COMMAND]")
}

func unknownCommand(name string) error {
	if s := suggest(name); s != "" {
		return fmt.Errorf("unknown command %q; did you mean %q? (`prusactl help` lists them all)", name, s)
	}
	return fmt.Errorf("unknown command %q; run `prusactl help` for the list", name)
}

// suggest returns the command closest to a mistyped name, if one is close.
func suggest(name string) string {
	best, bestDist := "", 3
	for _, c := range commands {
		if strings.HasPrefix(c.Name, name) && len(name) >= 2 {
			return c.Name
		}
		if d := editDistance(name, c.Name); d < bestDist {
			best, bestDist = c.Name, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// options are a command's parsed flags.
type options struct{ fs *flag.FlagSet }

func (o options) str(name string) string { return o.fs.Lookup(name).Value.String() }
func (o options) on(name string) bool    { return o.fs.Lookup(name).Value.String() == "true" }

// parse parses args against c's flags, accepting flags before, between, or
// after the positional arguments, and returns the positional ones.
func (c *command) parse(args []string) (options, []string, error) {
	fs := flag.NewFlagSet(c.Name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	for _, f := range c.Flags {
		if f.Value != "" {
			fs.String(f.Name, f.Default, f.Usage)
		} else {
			fs.Bool(f.Name, false, f.Usage)
		}
	}
	pos, err := parseFlags(fs, args)
	if err != nil {
		msg := err.Error()
		if f, ok := strings.CutPrefix(msg, "flag provided but not defined: -"); ok {
			msg = "unknown flag --" + f
		} else if f, ok := strings.CutPrefix(msg, "flag needs an argument: -"); ok {
			msg = "--" + f + " needs a value"
		}
		return options{}, nil, fmt.Errorf("%s: %s (see `prusactl help %s`)", c.Name, msg, c.Name)
	}
	return options{fs}, pos, nil
}

func completionCommand(args []string) error {
	if len(args) != 1 {
		return errors.New("completion: usage: prusactl completion bash|zsh|fish")
	}
	switch args[0] {
	case "bash":
		writeBashCompletion(os.Stdout)
	case "zsh":
		writeZshCompletion(os.Stdout)
	case "fish":
		writeFishCompletion(os.Stdout)
	default:
		return fmt.Errorf("completion: unsupported shell %q (bash, zsh, or fish)", args[0])
	}
	return nil
}

// --- bash -----------------------------------------------------------------------

func writeBashCompletion(w io.Writer) {
	fmt.Fprintf(w, `# bash completion for prusactl. Generated by `+"`prusactl completion bash`"+`.

# _prusactl_pos prints how many positional arguments come before the word being
# completed. $1 lists the flags that take a value.
_prusactl_pos() {
    local i w n=0 skip=
    for ((i = 2; i < COMP_CWORD; i++)); do
        w=${COMP_WORDS[i]}
        if [[ -n $skip ]]; then skip=; continue; fi
        if [[ $w == -* ]]; then
            [[ $w != *=* && " $1 " == *" $w "* ]] && skip=1
            continue
        fi
        ((n++))
    done
    echo "$n"
}

_prusactl() {
    local cur=${COMP_WORDS[COMP_CWORD]} prev=${COMP_WORDS[COMP_CWORD-1]}
    COMPREPLY=()
    if ((COMP_CWORD == 1)); then
        COMPREPLY=($(compgen -W "%s --help --version" -- "$cur"))
        return
    fi
    case ${COMP_WORDS[1]} in
`, strings.Join(commandNames(), " "))
	for _, c := range commands {
		var all, valued []string
		for _, f := range c.Flags {
			all = append(all, "--"+f.Name)
			if f.Value != "" {
				valued = append(valued, "--"+f.Name)
			}
		}
		all = append(all, "--help")
		fmt.Fprintf(w, "    %s)\n", c.Name)
		if len(valued) > 0 {
			fmt.Fprintf(w, "        case $prev in %s) return ;; esac\n", strings.Join(valued, "|"))
		}
		fmt.Fprintf(w, "        if [[ $cur == -* ]]; then\n            COMPREPLY=($(compgen -W %q -- \"$cur\"))\n            return\n        fi\n", strings.Join(all, " "))
		if len(c.Pos) > 0 {
			fmt.Fprintf(w, "        case $(_prusactl_pos %q) in\n", strings.Join(valued, " "))
			for i, p := range c.Pos {
				if gen := bashGen(p.Kind); gen != "" {
					fmt.Fprintf(w, "            %d) %s ;;\n", i, gen)
				}
			}
			fmt.Fprint(w, "        esac\n")
		}
		fmt.Fprint(w, "        ;;\n")
	}
	fmt.Fprint(w, "    esac\n}\n\ncomplete -F _prusactl prusactl\n")
}

func bashGen(kind string) string {
	switch kind {
	case "":
		return ""
	case "hosts":
		return `COMPREPLY=($(compgen -A hostname -- "$cur"))`
	case "files":
		return `compopt -o filenames 2>/dev/null; COMPREPLY=($(compgen -f -- "$cur"))`
	case "commands":
		return fmt.Sprintf(`COMPREPLY=($(compgen -W %q -- "$cur"))`, strings.Join(commandNames(), " "))
	}
	return fmt.Sprintf(`COMPREPLY=($(compgen -W %q -- "$cur"))`, kind)
}

// --- zsh ------------------------------------------------------------------------

// zshSingle single-quotes s for zsh.
func zshSingle(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// zshDesc escapes the characters that are special inside an _arguments spec or
// a _describe entry.
func zshDesc(s string) string {
	return strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`, `:`, `\:`).Replace(s)
}

func writeZshCompletion(w io.Writer) {
	fmt.Fprint(w, "#compdef prusactl\n# zsh completion for prusactl. Generated by `prusactl completion zsh`.\n\n")
	fmt.Fprint(w, "_prusactl() {\n    local curcontext=$curcontext state line\n    local -a commands\n    commands=(\n")
	for _, c := range commands {
		fmt.Fprintf(w, "        %s\n", zshSingle(c.Name+":"+zshDesc(c.Summary)))
	}
	fmt.Fprint(w, "    )\n")
	fmt.Fprint(w, "    _arguments -C '(- *)--help[show help]' '(- *)--version[print the version]' '1: :->command' '*:: :->args'\n")
	fmt.Fprint(w, "    case $state in\n    command)\n        _describe -t commands 'prusactl command' commands\n        ;;\n    args)\n        case $line[1] in\n")
	for _, c := range commands {
		specs := []string{zshSingle("(- *)--help[show help]")}
		for _, f := range c.Flags {
			if f.Value == "" {
				specs = append(specs, zshSingle("--"+f.Name+"["+zshDesc(f.Usage)+"]"))
			} else {
				specs = append(specs, zshSingle("--"+f.Name+"=["+zshDesc(f.Usage)+"]:"+strings.ToLower(f.Value)+": "))
			}
		}
		for i, p := range c.Pos {
			action := " "
			switch p.Kind {
			case "":
			case "hosts":
				action = "_hosts"
			case "files":
				action = "_files"
			case "commands":
				action = "(" + strings.Join(commandNames(), " ") + ")"
			default:
				action = "(" + p.Kind + ")"
			}
			specs = append(specs, zshSingle(fmt.Sprintf("%d:%s:%s", i+1, zshDesc(p.Label), action)))
		}
		fmt.Fprintf(w, "        %s)\n            _arguments -s \\\n                %s\n            ;;\n", c.Name, strings.Join(specs, " \\\n                "))
	}
	fmt.Fprint(w, "        esac\n        ;;\n    esac\n}\n\nif [[ $zsh_eval_context[-1] == loadautofunc ]]; then\n    _prusactl \"$@\"\nelse\n    compdef _prusactl prusactl\nfi\n")
}

// --- fish -----------------------------------------------------------------------

func fishQuote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

func writeFishCompletion(w io.Writer) {
	names := strings.Join(commandNames(), " ")
	fmt.Fprint(w, "# fish completion for prusactl. Generated by `prusactl completion fish`.\n\n")
	fmt.Fprint(w, "complete -c prusactl -f\n")
	top := fmt.Sprintf("-n 'not __fish_seen_subcommand_from %s'", names)
	for _, c := range commands {
		fmt.Fprintf(w, "complete -c prusactl %s -a %s -d %s\n", top, c.Name, fishQuote(c.Summary))
	}
	fmt.Fprintf(w, "complete -c prusactl %s -l help -d 'Show help'\n", top)
	fmt.Fprintf(w, "complete -c prusactl %s -l version -d 'Print the version'\n", top)
	for _, c := range commands {
		cond := fmt.Sprintf("-n '__fish_seen_subcommand_from %s'", c.Name)
		for _, f := range c.Flags {
			x := ""
			if f.Value != "" {
				x = " -x"
			}
			fmt.Fprintf(w, "complete -c prusactl %s -l %s%s -d %s\n", cond, f.Name, x, fishQuote(f.Usage))
		}
		for _, p := range c.Pos {
			switch p.Kind {
			case "":
			case "hosts":
				fmt.Fprintf(w, "complete -c prusactl %s -a '(__fish_print_hostnames)'\n", cond)
			case "files":
				fmt.Fprintf(w, "complete -c prusactl %s -F\n", cond)
			case "commands":
				fmt.Fprintf(w, "complete -c prusactl %s -a %s\n", cond, fishQuote(names))
			default:
				fmt.Fprintf(w, "complete -c prusactl %s -a %s\n", cond, fishQuote(p.Kind))
			}
		}
	}
}
