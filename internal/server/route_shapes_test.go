package server

import (
	"encoding/json"
	"testing"
)

// A tool that answers in one shape on the direct route and another through
// Prusa Connect makes its caller write the reading twice, and whichever half
// was written first is the one that got tested. These check the shapes that
// are supposed to be the same either way.

func TestCurrentTransferIgnoresTheHistory(t *testing.T) {
	// Connect's /transfers is the printer's transfer history, newest first.
	history := json.RawMessage(`{"transfers":[
		{"id":3,"state":"FIN_OK","end":1790654238,"path":"/usb/done.bgcode"},
		{"id":2,"state":"FIN_ERROR","end":1790654100,"path":"/usb/failed.bgcode"}
	]}`)
	if got, err := currentTransfer(history); err != nil || string(got) != "null" {
		t.Errorf("reported a finished transfer as running: %s %v", got, err)
	}

	running := json.RawMessage(`{"transfers":[
		{"id":4,"state":"TRANSFERING","path":"/usb/now.bgcode"},
		{"id":3,"state":"FIN_OK","end":1790654238,"path":"/usb/done.bgcode"}
	]}`)
	got, err := currentTransfer(running)
	if err != nil {
		t.Fatal(err)
	}
	if b := compactTransfer(got); b == nil {
		t.Fatalf("missed the running transfer: %s", got)
	} else if b.(*transferBrief).Path != "/usb/now.bgcode" {
		t.Errorf("picked %+v", b)
	}

	if got, err := currentTransfer(json.RawMessage(`{"transfers":[]}`)); err != nil || string(got) != "null" {
		t.Errorf("an empty history reported a transfer: %s %v", got, err)
	}
}

// Connect's transfer record carries the whole sliced file's metadata; the
// question "is anything being sent to the printer?" needs none of it.
func TestCompactTransferReadsBothRoutes(t *testing.T) {
	direct := compactTransfer(json.RawMessage(
		`{"display_name":"part.bgcode","path":"/usb/PART~1.BGC","size":1000,"transferred":250,"progress":25}`))
	connect := compactTransfer(json.RawMessage(
		`{"source_file":{"display_name":"part.bgcode"},"path":"/usb/PART~1.BGC","size":1000,"transferred":250,"state":"TRANSFERING"}`))

	for name, v := range map[string]any{"direct": direct, "connect": connect} {
		b, ok := v.(*transferBrief)
		if !ok || b == nil {
			t.Fatalf("%s: %v", name, v)
		}
		if b.Name != "part.bgcode" {
			t.Errorf("%s name = %q", name, b.Name)
		}
		// Connect reports size and transferred but no progress; it is worked out.
		if b.Progress == nil || *b.Progress != 25 {
			t.Errorf("%s progress = %v", name, b.Progress)
		}
	}

	if got := compactTransfer(json.RawMessage("null")); got != nil {
		t.Errorf("no transfer became %v", got)
	}
}

// The printer calls a storage listing storage_list, Connect calls it storages.
func TestWithStoragesGivesOneName(t *testing.T) {
	out := withStorages(json.RawMessage(`{"storage_list":[{"name":"usb"}]}`))
	var body map[string]json.RawMessage
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["storages"]; !ok {
		t.Errorf("the printer's listing has no storages: %s", out)
	}
	if _, ok := body["storage_list"]; !ok {
		t.Errorf("the route's own name was dropped: %s", out)
	}

	// Connect already calls it that, and must not be rewritten.
	same := json.RawMessage(`{"storages":[{"name":"usb"}]}`)
	if got := withStorages(same); string(got) != string(same) {
		t.Errorf("Connect's listing was rewritten: %s", got)
	}
}

// Connect wraps a command it waited for and returns an asynchronous one bare;
// the id is what get_command takes, so it has to come out of either.
func TestCommandFactsReadsBothReplies(t *testing.T) {
	for name, tc := range map[string]struct{ raw, want string }{
		"async": {`{"id":3099,"command":"HOME","state":"CREATED"}`, "CREATED"},
		"sync":  {`{"command":{"id":3099,"command":"HOME","state":"CREATED"},"event":{"event":"FINISHED"}}`, "FINISHED"},
		// The record says the command was created; the event says the printer
		// turned it down. Reporting "CREATED" here reads as success.
		"rejected": {`{"command":{"id":3099,"command":"HOME","state":"CREATED"},"event":{"event":"REJECTED","reason":"BUSY"}}`, "REJECTED"},
	} {
		id, state := commandFacts(json.RawMessage(tc.raw))
		if id != 3099 {
			t.Errorf("%s id = %d", name, id)
		}
		if state != tc.want {
			t.Errorf("%s state = %q, want %q", name, state, tc.want)
		}
	}

	// A reply with neither is not a reason to fail; it just says less.
	if id, state := commandFacts(json.RawMessage(`{}`)); id != 0 || state != "" {
		t.Errorf("empty reply gave %d %q", id, state)
	}
}

// A reply that can't be read is not a reply saying nothing is happening: that
// answer is indistinguishable from the truth, and an agent watching an upload
// would take it as arrived.
func TestCurrentTransferReportsAChangedAPI(t *testing.T) {
	if _, err := currentTransfer(json.RawMessage(`{"items":[]}`)); err == nil {
		t.Error("a renamed transfers field passed as 'nothing is being transferred'")
	}
	if _, err := currentTransfer(json.RawMessage(`not json`)); err == nil {
		t.Error("an unreadable reply passed as 'nothing is being transferred'")
	}
	// An empty list really does mean nothing is happening.
	got, err := currentTransfer(json.RawMessage(`{"transfers":[]}`))
	if err != nil || string(got) != "null" {
		t.Errorf("empty list: %s %v", got, err)
	}
}
