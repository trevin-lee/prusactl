package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// These cover the tools whose answer prusactl works out for itself, rather than
// passing the route's reply along: what a command came to, what a list says
// about its next page, which route a printer came from, and what a failure
// halfway through a batch leaves behind.

func event(id int64, name, ev string, at float64) map[string]any {
	return map[string]any{"command_id": id, "command": name, "event": ev, "created": at}
}

// Connect has no endpoint for one command; get_command reads the event log,
// where the newest entry for an id is the state it reached.
func TestGetCommandReadsTheEventLog(t *testing.T) {
	fc := &fakeConnect{state: "IDLE", events: []map[string]any{
		event(12, "HOME", "FINISHED", 300),
		event(12, "HOME", "EMIT", 200),
		event(11, "BEEP", "REJECTED", 150),
		event(12, "HOME", "CREATED", 100),
	}}
	cs := connectConnectTools(t, fc)

	text, isErr := call(t, cs, "get_command", map[string]any{"command_id": 12})
	if isErr {
		t.Fatalf("get_command: %s", text)
	}
	var got commandStatus
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("%v: %s", err, text)
	}
	if got.State != "FINISHED" {
		t.Errorf("state = %q, want the newest event", got.State)
	}
	if got.Command != "HOME" {
		t.Errorf("command = %q", got.Command)
	}
	if len(got.Events) != 3 {
		t.Errorf("kept %d events, want the 3 belonging to command 12: %+v", len(got.Events), got.Events)
	}
	for _, e := range got.Events {
		if e.Event == "REJECTED" {
			t.Error("picked up another command's event")
		}
	}
}

// A command too old for the log is not a command that failed, and saying so is
// the difference between looking again and giving up.
func TestGetCommandSaysWhenItCannotFindTheCommand(t *testing.T) {
	fc := &fakeConnect{state: "IDLE", events: []map[string]any{event(99, "HOME", "FINISHED", 300)}}
	cs := connectConnectTools(t, fc)

	text, isErr := call(t, cs, "get_command", map[string]any{"command_id": 12})
	if !isErr {
		t.Fatalf("missing command reported as success: %s", text)
	}
	if !strings.Contains(text, "no record of command 12") {
		t.Errorf("unhelpful message: %q", text)
	}
}

// Every list has to say the same way that there is more to fetch, or paging
// through one teaches nothing about paging through the next.
func TestListEventsSaysWhereTheNextPageStarts(t *testing.T) {
	var events []map[string]any
	for i := range 10 {
		events = append(events, event(int64(i), "BEEP", "FINISHED", float64(i)))
	}
	cs := connectConnectTools(t, &fakeConnect{state: "IDLE", events: events})

	text, isErr := call(t, cs, "list_events", map[string]any{"limit": 4})
	if isErr {
		t.Fatalf("list_events: %s", text)
	}
	var body struct {
		Events     []map[string]any `json:"events"`
		NextOffset *int             `json:"next_offset"`
	}
	if err := json.Unmarshal([]byte(text), &body); err != nil {
		t.Fatalf("%v: %s", err, text)
	}
	if len(body.Events) != 4 {
		t.Errorf("returned %d events for limit 4", len(body.Events))
	}
	if body.NextOffset == nil || *body.NextOffset != 4 {
		t.Errorf("next_offset = %v, want 4", body.NextOffset)
	}

	// The last page has nothing after it, and must not claim otherwise.
	text, _ = call(t, cs, "list_events", map[string]any{"limit": 4, "offset": 8})
	if strings.Contains(text, "next_offset") {
		t.Errorf("the last page offered another: %s", text)
	}
}

// Uploading through Connect leaves a copy in cloud storage; without a delete
// there is no way to get the team's quota back.
func TestDeleteConnectFilesAsksForTheRightTeam(t *testing.T) {
	fc := &fakeConnect{state: "IDLE"}
	cs := connectConnectTools(t, fc)

	text, isErr := call(t, cs, "delete_connect_files", map[string]any{"hashes": []string{"h1", "h2"}})
	if isErr {
		t.Fatalf("delete_connect_files: %s", text)
	}
	fc.mu.Lock()
	gone := append([]string(nil), fc.gone...)
	fc.mu.Unlock()
	if len(gone) != 2 || gone[0] != "h1" || gone[1] != "h2" {
		t.Errorf("Connect was asked to delete %v", gone)
	}
}

// Half a delete is worth reporting precisely: which files are gone decides
// whether running the command again is safe.
func TestDeletePrinterFilesNamesWhatItGotThrough(t *testing.T) {
	fp := &busyAfterFirst{}
	cs := connectTools(t, &fakePrinter{state: "IDLE", handler: fp.serve})

	text, isErr := call(t, cs, "delete_printer_files", map[string]any{
		"paths": []string{"/usb/A.BGC", "/usb/B.BGC", "/usb/C.BGC"}})
	if !isErr {
		t.Fatalf("a failed delete reported as success: %s", text)
	}
	if !strings.Contains(text, "deleted /usb/A.BGC;") {
		t.Errorf("didn't say what was already gone: %q", text)
	}
	if !strings.Contains(text, "could not delete /usb/B.BGC") {
		t.Errorf("didn't name the file it stopped on: %q", text)
	}
	// The printer's own words are "File is busy", which tells nobody what to do.
	if !strings.Contains(text, "still has the file open") {
		t.Errorf("no advice for a busy file: %q", text)
	}
	if strings.Contains(text, "C.BGC") {
		t.Errorf("claimed something about the file it never reached: %q", text)
	}
}

// busyAfterFirst deletes one file and then holds the rest open, as PrusaLink
// does with a file it has just written or has selected on its screen.
type busyAfterFirst struct{ deleted int }

func (b *busyAfterFirst) serve(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodDelete || !strings.HasPrefix(r.URL.Path, "/api/v1/files/") {
		return false
	}
	if b.deleted == 0 {
		b.deleted++
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	w.WriteHeader(http.StatusConflict)
	fmt.Fprint(w, `{"title":"409: Conflict","message":"File is busy"}`)
	return true
}

// Starting a file that isn't there is the commonest mistake, and PrusaLink's
// 404 for it carries no message at all.
func TestStartPrintExplainsAMissingFile(t *testing.T) {
	cs := connectTools(t, &fakePrinter{state: "IDLE"})

	text, isErr := call(t, cs, "start_print", map[string]any{"path": "/usb/NOPE.BGC"})
	if !isErr {
		t.Fatalf("starting a missing file reported as success: %s", text)
	}
	if !strings.Contains(text, "no file at /usb/NOPE.BGC") {
		t.Errorf("didn't name the file: %q", text)
	}
	if !strings.Contains(text, "short names") {
		t.Errorf("didn't explain the paths the printer uses: %q", text)
	}
}

// list_printers is how a second printer is found, so it has to say which route
// each one came from rather than mixing them into one list.
func TestListPrintersKeepsTheRoutesApart(t *testing.T) {
	cs := connectTools(t, &fakePrinter{state: "IDLE"})

	text, isErr := call(t, cs, "list_printers", map[string]any{})
	if isErr {
		t.Fatalf("list_printers: %s", text)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &body); err != nil {
		t.Fatalf("%v: %s", err, text)
	}
	if _, ok := body["direct"]; !ok {
		t.Errorf("the printer set up directly isn't listed: %s", text)
	}
	if !strings.Contains(text, "fake-core-one") {
		t.Errorf("listed no name for it: %s", text)
	}
}
