package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// stamp turns Connect's epoch seconds into a time a person reads. Anything
// that isn't a time comes back unchanged, so a field the API stopped sending
// prints as itself instead of as 1970.
func TestStampOnlyConvertsTimes(t *testing.T) {
	want := time.Unix(1790653558, 0).Local().Format(time.DateTime)
	for _, in := range []string{"1790653558", "1790653558.077908"} {
		if got := stamp(in); got != want {
			t.Errorf("stamp(%q) = %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{"", "FINISHED", "not a time"} {
		if got := stamp(in); got != in {
			t.Errorf("stamp(%q) = %q, want it unchanged", in, got)
		}
	}
}

// Connect's telemetry is one sample per bucket, most of them empty; the empty
// ones are not readings of zero.
func TestNumbersSkipsTheEmptyBuckets(t *testing.T) {
	// decodeMap's decoder is the one that produces json.Number, so go through it.
	m := decodeMap(json.RawMessage(`{"s":[null,28.1,null,24.6,null]}`))
	got := numbers(m["s"])
	if len(got) != 2 || got[0] != 28.1 || got[1] != 24.6 {
		t.Errorf("numbers = %v, want the two readings", got)
	}
	if n := numbers(nil); len(n) != 0 {
		t.Errorf("numbers(nil) = %v", n)
	}
	if n := numbers([]any{}); len(n) != 0 {
		t.Errorf("numbers of an empty series = %v", n)
	}
}

func TestFieldReadsNestedValuesAndMissingOnes(t *testing.T) {
	m := decodeMap(json.RawMessage(`{"file":{"display_name":"part.bgcode"},"id":59,"end":null}`))
	if got := field(m, "file", "display_name"); got != "part.bgcode" {
		t.Errorf("display_name = %q", got)
	}
	if got := field(m, "id"); got != "59" {
		t.Errorf("id = %q, want the digits it arrived with", got)
	}
	for _, path := range [][]string{{"end"}, {"nope"}, {"file", "nope"}, {"id", "nope"}} {
		if got := field(m, path...); got != "" {
			t.Errorf("field(%v) = %q, want empty", path, got)
		}
	}
}

func TestRowsIgnoresWhatIsNotAList(t *testing.T) {
	m := decodeMap(json.RawMessage(`{"events":[{"a":1},"not an object",{"b":2}],"pager":{},"none":null}`))
	if got := rows(m, "events"); len(got) != 2 {
		t.Errorf("rows kept %d of the objects", len(got))
	}
	for _, key := range []string{"pager", "none", "missing"} {
		if got := rows(m, key); len(got) != 0 {
			t.Errorf("rows(%q) = %v", key, got)
		}
	}
}

// Long ids are the reason the CLI decodes with json.Number: printing 9.2e+18
// for a job id would be an id nobody can look up again.
func TestDecodeMapKeepsLongNumbersExact(t *testing.T) {
	m := decodeMap(json.RawMessage(`{"id":9007199254740993}`))
	if got := field(m, "id"); got != "9007199254740993" {
		t.Errorf("id = %s", got)
	}
	if decodeMap(json.RawMessage(`not json`)) != nil {
		t.Error("decodeMap accepted something that isn't JSON")
	}
}

// An old name keeps working, and is kept out of the listing so there is one
// name for the job; a typo of it still finds its way home.
func TestRenamedCommandStillRunsButIsNotAdvertised(t *testing.T) {
	c := lookup("download")
	if c == nil {
		t.Fatal("download no longer resolves; anyone's script using it breaks")
	}
	if c.Renamed == "" {
		t.Fatal("download isn't marked as an old name")
	}
	for _, listed := range listed() {
		if listed.Name == "download" {
			t.Error("an old name is advertised alongside the new one")
		}
	}
	var usage strings.Builder
	printUsage(&usage)
	if strings.Contains(usage.String(), "download") {
		t.Error("the command list offers both names")
	}
	if got := suggest("dowload"); got != "download" {
		t.Errorf("suggest(%q) = %q; a typo of an old name is still a typo", "dowload", got)
	}
}

// A flag no command has is worth refusing: --jsom silently ignored prints the
// wrong kind of output and says nothing about why.
func TestCommandsWithoutFlagsStillRefuseOne(t *testing.T) {
	for _, name := range []string{"status", "login", "logout", "mcp"} {
		err := noArguments(lookup(name), []string{"--nonsense"}, func() error {
			t.Errorf("%s ran with an unknown flag", name)
			return nil
		})
		if err == nil || !strings.Contains(err.Error(), "unknown flag --nonsense") {
			t.Errorf("%s --nonsense: %v", name, err)
		}
		err = noArguments(lookup(name), []string{"extra"}, func() error {
			t.Errorf("%s ran with an argument it doesn't take", name)
			return nil
		})
		if err == nil || !strings.Contains(err.Error(), "takes no arguments") {
			t.Errorf("%s extra: %v", name, err)
		}
	}
	ran := false
	if err := noArguments(lookup("status"), nil, func() error { ran = true; return nil }); err != nil || !ran {
		t.Errorf("plain status: %v", err)
	}
}
