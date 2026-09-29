package server

import (
	"encoding/json"
	"testing"
)

// The point of summary is that one reading of the answer works whichever route
// replied, so the two routes are checked against the same expectations.

func TestSummaryReadsBothRoutesTheSameWay(t *testing.T) {
	status := decode(json.RawMessage(`{"printer":{"state":"PRINTING","temp_nozzle":215.4,"target_nozzle":215,"temp_bed":60.1,"target_bed":60}}`))
	job := decode(json.RawMessage(`{"progress":42.5,"time_remaining":1800,"file":{"name":"PART~1.BGC","display_name":"part.bgcode"}}`))
	direct := summarizeDirect(status, job)

	connect := summarizeConnect(decode(json.RawMessage(`{
		"connect_state":"PRINTING",
		"temp":{"temp_nozzle":215.4,"target_nozzle":215,"temp_bed":60.1,"target_bed":60},
		"job_info":{"progress":42.5,"time_remaining":1800,"display_name":"part.bgcode","path":"/usb/PART~1.BGC"}
	}`)))

	for name, s := range map[string]*summary{"direct": direct, "connect": connect} {
		if s.State != "PRINTING" {
			t.Errorf("%s state = %q", name, s.State)
		}
		if s.Nozzle == nil || s.Nozzle.Actual != 215.4 || s.Nozzle.Target == nil || *s.Nozzle.Target != 215 {
			t.Errorf("%s nozzle = %+v", name, s.Nozzle)
		}
		if s.Bed == nil || s.Bed.Actual != 60.1 {
			t.Errorf("%s bed = %+v", name, s.Bed)
		}
		if s.Job == nil || s.Job.Name != "part.bgcode" {
			t.Errorf("%s job = %+v", name, s.Job)
		}
		if s.Job == nil || s.Job.Progress == nil || *s.Job.Progress != 42.5 {
			t.Errorf("%s progress = %+v", name, s.Job)
		}
	}
}

// PrusaLink has no chamber field at all, so its absence is the printer's limit
// and not something to invent a zero for.
func TestSummaryLeavesOutWhatTheRouteDoesNotReport(t *testing.T) {
	direct := summarizeDirect(decode(json.RawMessage(`{"printer":{"state":"IDLE","temp_nozzle":26}}`)), nil)
	if direct.Chamber != nil {
		t.Errorf("direct invented a chamber reading: %+v", direct.Chamber)
	}
	if direct.Bed != nil {
		t.Errorf("direct invented a bed reading: %+v", direct.Bed)
	}
	if direct.Job != nil {
		t.Errorf("direct invented a job: %+v", direct.Job)
	}
	if direct.Nozzle == nil || direct.Nozzle.Target != nil {
		t.Errorf("nozzle target should stay absent, not become 0: %+v", direct.Nozzle)
	}

	conn := summarizeConnect(decode(json.RawMessage(`{"connect_state":"IDLE","chamber":{"temp":24.7}}`)))
	if conn.Chamber == nil || conn.Chamber.Actual != 24.7 {
		t.Errorf("connect chamber = %+v", conn.Chamber)
	}
}

// A job Connect names only by path still gets a name a person can read.
func TestSummaryFallsBackToTheFileName(t *testing.T) {
	conn := summarizeConnect(decode(json.RawMessage(`{"connect_state":"PRINTING","job_info":{"path":"/usb/PART~1.BGC","progress":1}}`)))
	if conn.Job == nil || conn.Job.Name != "usb/PART~1.BGC" {
		t.Errorf("job = %+v", conn.Job)
	}
	direct := summarizeDirect(
		decode(json.RawMessage(`{"printer":{"state":"PRINTING"}}`)),
		decode(json.RawMessage(`{"progress":1,"file":{"name":"PART~1.BGC"}}`)))
	if direct.Job == nil || direct.Job.Name != "PART~1.BGC" {
		t.Errorf("job = %+v", direct.Job)
	}
}

// Ids larger than a float64 can hold keep their digits.
func TestDecodeKeepsLongNumbersExact(t *testing.T) {
	m := decode(json.RawMessage(`{"id":9007199254740993}`))
	if got := m["id"].(json.Number).String(); got != "9007199254740993" {
		t.Errorf("id = %s", got)
	}
}
