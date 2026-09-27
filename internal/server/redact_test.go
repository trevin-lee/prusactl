package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactJSONMasksCredentials(t *testing.T) {
	in := `{"name":"Core One","api_key":"abc123","prusalink_api_key":"def456","prusaconnect_api_key":"ghi789",
		"id":9007199254740993,"cameras":[{"token":"cam-token","name":"Buddy3D"}],"password":""}`
	out, changed := RedactJSON([]byte(in))
	if !changed {
		t.Fatal("reported nothing redacted")
	}
	for _, secret := range []string{"abc123", "def456", "ghi789", "cam-token"} {
		if strings.Contains(string(out), secret) {
			t.Errorf("%q leaked: %s", secret, out)
		}
	}
	var got map[string]any
	dec := json.NewDecoder(strings.NewReader(string(out)))
	dec.UseNumber()
	if err := dec.Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["name"] != "Core One" || got["password"] != "" {
		t.Errorf("non-secret fields changed: %v", got)
	}
	if got["id"].(json.Number).String() != "9007199254740993" {
		t.Errorf("large number lost precision: %v", got["id"])
	}
}

func TestRedactJSONLeavesCleanAndNonJSONAlone(t *testing.T) {
	for _, in := range []string{`{"state":"IDLE","temp":24.5}`, `not json`, `{"a":1} {"b":2}`, ``} {
		out, changed := RedactJSON([]byte(in))
		if changed || string(out) != in {
			t.Errorf("RedactJSON(%q) = %q, %v; want it unchanged", in, out, changed)
		}
	}
}
