// Package compat reports answers from Prusa's services, or from the printer,
// that prusactl wasn't built for. That almost always means Prusa changed an
// API, so every such case produces one recognizable message saying so and
// what to do, instead of a confusing symptom further on.
package compat

import (
	"errors"
	"fmt"
)

// Version is prusactl's version, included in the message to help a report.
// The CLI sets it at startup.
var Version = "dev"

// Issues is where to report a break that the newest version doesn't fix.
const Issues = "https://github.com/trevin-lee/prusactl/issues"

// Error is an answer prusactl doesn't recognize.
type Error struct {
	Service string // who answered: "Prusa Connect", "Prusa Account", "The printer"
	Detail  string // what was unexpected, naming the request
	Err     error  // the underlying error, if any
}

func (e *Error) Error() string {
	cause := "Prusa has probably changed its API."
	if e.Service == Printer {
		cause = "Its firmware has probably changed the local API (after a firmware update?)."
	}
	return fmt.Sprintf("%s answered in a way prusactl %s doesn't recognize: %s. %s Update prusactl "+
		"(`brew upgrade prusactl`, or the latest release); if the newest version still fails, please report it at %s.",
		e.Service, Version, e.Detail, cause, Issues)
}

func (e *Error) Unwrap() error { return e.Err }

// Services, as named in messages.
const (
	Connect = "Prusa Connect"
	Account = "Prusa Account"
	Printer = "The printer"
)

// Is reports whether err is (or wraps) an unrecognized answer.
func Is(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

// New builds an Error.
func New(service, format string, args ...any) *Error {
	return &Error{Service: service, Detail: fmt.Sprintf(format, args...)}
}
