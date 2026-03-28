package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mmediasoftwarelab/siftlog/internal/adapter"
	"github.com/mmediasoftwarelab/siftlog/internal/correlator"
)

func makeResult(svc, msg string, sev adapter.Severity, isSignal bool) correlator.Result {
	return correlator.Result{
		Event: adapter.Event{
			Timestamp: time.Date(2024, 1, 15, 14, 22, 3, 441000000, time.UTC),
			Service:   svc,
			Source:    "test-source",
			Severity:  sev,
			Message:   msg,
			TraceID:   "abc123",
			Fields:    map[string]string{"pool_size": "20"},
		},
		IsSignal:  isSignal,
		SignalType: "cascade",
	}
}

// TestJSONOutputIsValidJSON verifies each line is parseable JSON.
func TestJSONOutputIsValidJSON(t *testing.T) {
	var buf bytes.Buffer
	w := NewJSON(&buf)
	w.Header(2)
	w.Write(makeResult("payment-service", "connection pool exhausted", adapter.SeverityError, true))
	w.Write(makeResult("order-service", "upstream unavailable", adapter.SeverityError, false))
	w.Footer()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 JSON lines, got %d", len(lines))
	}
	for i, line := range lines {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Errorf("line %d is not valid JSON: %v\nline: %s", i+1, err, line)
		}
	}
}

// TestJSONFields verifies required fields are present and correctly typed.
func TestJSONFields(t *testing.T) {
	var buf bytes.Buffer
	w := NewJSON(&buf)
	w.Write(makeResult("payment-service", "timeout", adapter.SeverityError, true))

	var obj map[string]any
	if err := json.Unmarshal(buf.Bytes(), &obj); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	required := []string{"timestamp", "service", "severity", "message", "is_signal"}
	for _, field := range required {
		if _, ok := obj[field]; !ok {
			t.Errorf("missing required field: %q", field)
		}
	}

	if obj["service"] != "payment-service" {
		t.Errorf("service: want %q, got %v", "payment-service", obj["service"])
	}
	if obj["severity"] != "ERROR" {
		t.Errorf("severity: want ERROR, got %v", obj["severity"])
	}
	if obj["is_signal"] != true {
		t.Errorf("is_signal: want true, got %v", obj["is_signal"])
	}
	if obj["trace_id"] != "abc123" {
		t.Errorf("trace_id: want abc123, got %v", obj["trace_id"])
	}
}

// TestJSONTimestampFormat verifies timestamps are RFC3339Nano UTC.
func TestJSONTimestampFormat(t *testing.T) {
	var buf bytes.Buffer
	w := NewJSON(&buf)
	w.Write(makeResult("svc", "msg", adapter.SeverityInfo, false))

	var obj map[string]any
	json.Unmarshal(buf.Bytes(), &obj)

	ts, ok := obj["timestamp"].(string)
	if !ok {
		t.Fatal("timestamp field missing or not a string")
	}
	parsed, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		t.Errorf("timestamp %q is not RFC3339Nano: %v", ts, err)
	}
	if parsed.Location() != time.UTC {
		t.Errorf("timestamp should be UTC, got %v", parsed.Location())
	}
}

// TestJSONSilenceSignals verifies silence signals are included in output.
func TestJSONSilenceSignals(t *testing.T) {
	var buf bytes.Buffer
	w := NewJSON(&buf)

	r := makeResult("svc-a", "normal event", adapter.SeverityInfo, false)
	r.SilenceSignals = []correlator.SilenceSignal{
		{Service: "svc-b", DropPct: 95.0, BaselineRate: 10.0, CurrentRate: 0.5},
	}
	w.Write(r)

	var obj map[string]any
	json.Unmarshal(buf.Bytes(), &obj)

	silence, ok := obj["silence"].([]any)
	if !ok || len(silence) == 0 {
		t.Fatal("expected silence array in output")
	}
	entry := silence[0].(map[string]any)
	if entry["service"] != "svc-b" {
		t.Errorf("silence service: want svc-b, got %v", entry["service"])
	}
	if entry["drop_pct"].(float64) != 95.0 {
		t.Errorf("drop_pct: want 95.0, got %v", entry["drop_pct"])
	}
}

// TestJSONNoExtraFieldsWhenEmpty verifies omitempty works — no clutter.
func TestJSONNoExtraFieldsWhenEmpty(t *testing.T) {
	var buf bytes.Buffer
	w := NewJSON(&buf)

	r := correlator.Result{
		Event: adapter.Event{
			Timestamp: time.Now(),
			Service:   "svc",
			Severity:  adapter.SeverityInfo,
			Message:   "hello",
		},
	}
	w.Write(r)

	var obj map[string]any
	json.Unmarshal(buf.Bytes(), &obj)

	for _, shouldBeAbsent := range []string{"trace_id", "cascade_from", "signal_type", "silence", "drift_warning", "fields"} {
		if _, present := obj[shouldBeAbsent]; present {
			t.Errorf("field %q should be absent when empty, but was present", shouldBeAbsent)
		}
	}
}

// TestJSONHeaderFooterNoOutput verifies Header/Footer write nothing.
func TestJSONHeaderFooterNoOutput(t *testing.T) {
	var buf bytes.Buffer
	w := NewJSON(&buf)
	w.Header(3)
	w.Footer()
	if buf.Len() != 0 {
		t.Errorf("Header/Footer should write nothing in JSON mode, got %q", buf.String())
	}
}
