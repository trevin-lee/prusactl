package server

import (
	"context"
	"fmt"
	"time"
)

// canStart is what the firmware itself accepts a remote print from (see
// printer_state::remote_print_ready in Prusa-Firmware-Buddy). FINISHED and
// STOPPED may still have the last part on the plate, which only a look at the
// printer or camera can rule out; the tool descriptions ask for that.
func canStart(state string) bool {
	switch state {
	case "IDLE", "READY", "FINISHED", "STOPPED":
		return true
	}
	return false
}

// printerState reads the printer's state over the route in use.
func (s *Server) printerState(ctx context.Context, t target) (string, error) {
	if t.direct {
		var st struct {
			Printer struct {
				State string `json:"state"`
			} `json:"printer"`
		}
		if _, err := s.link.Get(ctx, "/api/v1/status", &st); err != nil {
			return "", err
		}
		return st.Printer.State, nil
	}
	p, err := s.printerDetail(ctx, t.connect.UUID)
	if err != nil {
		return "", err
	}
	return stateOf(p), nil
}

// readyToStart refuses a print the printer can't take, before anything is
// uploaded or queued.
func (s *Server) readyToStart(ctx context.Context, t target) error {
	state, err := s.printerState(ctx, t)
	if err != nil {
		return fmt.Errorf("checking %s before printing: %w", t.name, err)
	}
	if !canStart(state) {
		hint := ""
		switch state {
		case "OFFLINE":
			hint = "; Prusa Connect can't reach it right now"
		case "ATTENTION":
			hint = "; a question is waiting on its screen (get_printer shows it, respond_to_dialog answers it)"
		case "PRINTING", "PAUSED", "BUSY":
			hint = "; wait for it to finish, or use then=queue"
		}
		return fmt.Errorf("%s is %s, so it can't start a print%s", t.name, state, hint)
	}
	return nil
}

// startedState waits briefly for a print just started directly to leave the
// idle states, and returns the state the printer settled in. The firmware
// accepts Print-After-Upload but may still stop on a question on its screen.
func (s *Server) startedState(ctx context.Context, t target) string {
	state := ""
	for i := 0; i < 10; i++ {
		if st, err := s.printerState(ctx, t); err == nil {
			state = st
			if !canStart(st) {
				return st
			}
		}
		select {
		case <-ctx.Done():
			return state
		case <-time.After(time.Second):
		}
	}
	return state
}

// startReport describes the outcome of starting a print directly.
func startReport(state string) map[string]any {
	out := map[string]any{"printer_state": state, "printing": state == "PRINTING"}
	switch {
	case state == "ATTENTION":
		out["note"] = "the printer stopped on a question on its screen before printing; get_printer shows it and respond_to_dialog answers it"
	case canStart(state):
		out["note"] = "the printer hasn't started yet; check get_printer"
	}
	return out
}
