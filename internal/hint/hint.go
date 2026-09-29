// Package hint phrases "how to fix your setup" advice for wherever prusactl is
// running. Installed from a terminal, the answer is `prusactl setup` or
// `prusactl login`; inside the MCP bundle there is no terminal and no installed
// copy, so the answer is the extension's own settings.
package hint

import "os"

// Managed reports whether prusactl was started by the MCP bundle, which passes
// the printer's address and password in the environment and sets this marker.
func Managed() bool { return os.Getenv("PRUSACTL_MANAGED") == "mcpb" }

// Printer says how to set up (or fix) the direct connection to the printer.
func Printer() string {
	if Managed() {
		return "set the printer's address and PrusaLink password in this extension's settings"
	}
	return "run `prusactl setup` in a terminal"
}

// WrongSecret says what to do when the printer rejects the saved password.
func WrongSecret() string {
	if Managed() {
		return "wrong password? check the PrusaLink password in this extension's settings"
	}
	return "wrong password? run `prusactl setup` again"
}

// Connect says how to sign in to Prusa Connect. The bundle can't: signing in
// needs a terminal, and the session is kept per installed copy.
func Connect() string {
	if Managed() {
		return "sign in with `prusactl login` from an installed copy of prusactl (this extension reaches the printer directly only)"
	}
	return "run `prusactl login` in a terminal"
}
