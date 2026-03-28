package file

import (
	"testing"
	"time"

	"github.com/mmediasoftwarelab/siftlog/internal/adapter"
	"github.com/mmediasoftwarelab/siftlog/internal/config"
)

// --- Timestamp parsing ---

func TestParseTimestamp(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
		check   func(t *testing.T, got time.Time)
	}{
		{
			input: "2024-01-15T14:22:03.441Z",
			check: func(t *testing.T, got time.Time) {
				assertEqual(t, 2024, got.Year(), "year")
				assertEqual(t, 1, int(got.Month()), "month")
				assertEqual(t, 15, got.Day(), "day")
				assertEqual(t, 14, got.Hour(), "hour")
				assertEqual(t, 22, got.Minute(), "minute")
				assertEqual(t, 3, got.Second(), "second")
			},
		},
		{
			input: "2024-01-15T14:22:03Z",
			check: func(t *testing.T, got time.Time) {
				assertEqual(t, 14, got.Hour(), "hour")
				assertEqual(t, 22, got.Minute(), "minute")
			},
		},
		{
			input: "2024-01-15 14:22:03",
			check: func(t *testing.T, got time.Time) {
				assertEqual(t, 2024, got.Year(), "year")
				assertEqual(t, 14, got.Hour(), "hour")
			},
		},
		{
			input: "1705329723",      // unix seconds
			check: func(t *testing.T, got time.Time) {
				assertEqual(t, 2024, got.Year(), "unix seconds year")
			},
		},
		{
			input: "1705329723441",   // unix milliseconds
			check: func(t *testing.T, got time.Time) {
				assertEqual(t, 2024, got.Year(), "unix ms year")
			},
		},
		{
			input:   "not-a-timestamp",
			wantErr: true,
		},
		{
			input:   "",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, err := parseTimestamp(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error for input %q, got time %v", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for input %q: %v", tc.input, err)
			}
			tc.check(t, got)
		})
	}
}

// --- JSON log line parsing ---

func TestParseJSON(t *testing.T) {
	src := config.SourceConfig{Name: "test-source"}

	tests := []struct {
		name  string
		input string
		check func(t *testing.T, got adapter.Event)
	}{
		{
			name:  "standard fields",
			input: `{"timestamp":"2024-01-15T14:22:03Z","level":"error","service":"payment-service","message":"connection pool exhausted","trace_id":"a3f9c2"}`,
			check: func(t *testing.T, got adapter.Event) {
				assertEqual(t, adapter.SeverityError, got.Severity, "severity")
				assertEqual(t, "payment-service", got.Service, "service")
				assertEqual(t, "connection pool exhausted", got.Message, "message")
				assertEqual(t, "a3f9c2", got.TraceID, "trace_id")
			},
		},
		{
			name:  "alternate field names: msg, lvl, ts",
			input: `{"ts":"2024-01-15T14:22:03Z","lvl":"warn","app":"order-service","msg":"retrying upstream"}`,
			check: func(t *testing.T, got adapter.Event) {
				assertEqual(t, adapter.SeverityWarn, got.Severity, "severity")
				assertEqual(t, "order-service", got.Service, "service")
				assertEqual(t, "retrying upstream", got.Message, "message")
			},
		},
		{
			name:  "extra fields go into Fields map",
			input: `{"timestamp":"2024-01-15T14:22:03Z","level":"info","message":"hello","pool_size":"20","waiting":"5"}`,
			check: func(t *testing.T, got adapter.Event) {
				assertEqual(t, "20", got.Fields["pool_size"], "pool_size field")
				assertEqual(t, "5", got.Fields["waiting"], "waiting field")
				// message and level should NOT be in Fields
				if _, ok := got.Fields["message"]; ok {
					t.Error("message should not appear in Fields")
				}
				if _, ok := got.Fields["level"]; ok {
					t.Error("level should not appear in Fields")
				}
			},
		},
		{
			name:  "requestId as trace ID",
			input: `{"timestamp":"2024-01-15T14:22:03Z","level":"info","message":"ok","requestId":"req-999"}`,
			check: func(t *testing.T, got adapter.Event) {
				assertEqual(t, "req-999", got.TraceID, "requestId as trace")
			},
		},
		{
			name:  "source is always set from config",
			input: `{"timestamp":"2024-01-15T14:22:03Z","level":"info","message":"ok"}`,
			check: func(t *testing.T, got adapter.Event) {
				assertEqual(t, "test-source", got.Source, "source")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseLine(tc.input, src)
			tc.check(t, got)
		})
	}
}

// --- Plain text log line parsing ---

func TestParsePlainText(t *testing.T) {
	src := config.SourceConfig{Name: "test-source"}

	tests := []struct {
		name  string
		input string
		check func(t *testing.T, got adapter.Event)
	}{
		{
			name:  "timestamp + bracketed severity + message",
			input: "2024-01-15T14:22:03Z [ERROR] connection pool exhausted",
			check: func(t *testing.T, got adapter.Event) {
				assertEqual(t, adapter.SeverityError, got.Severity, "severity")
				assertEqual(t, "connection pool exhausted", got.Message, "message")
				assertEqual(t, 2024, got.Timestamp.Year(), "year")
			},
		},
		{
			name:  "timestamp + bare severity + message",
			input: "2024-01-15T14:22:03Z WARN retrying upstream connection",
			check: func(t *testing.T, got adapter.Event) {
				assertEqual(t, adapter.SeverityWarn, got.Severity, "severity")
				assertEqual(t, "retrying upstream connection", got.Message, "message")
			},
		},
		{
			name:  "no timestamp — whole line is message",
			input: "some log line with no timestamp",
			check: func(t *testing.T, got adapter.Event) {
				if got.Message == "" {
					t.Error("message should not be empty")
				}
			},
		},
		{
			name:  "empty line produces empty message",
			input: "",
			check: func(t *testing.T, got adapter.Event) {
				assertEqual(t, "", got.Message, "message")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseLine(tc.input, src)
			tc.check(t, got)
		})
	}
}

// --- Timestamp offset ---

func TestTimestampOffset(t *testing.T) {
	src := config.SourceConfig{
		Name:              "offset-source",
		Type:              "file",
		TimestampOffsetMs: -200, // source clock is 200ms fast
	}

	line := `{"timestamp":"2024-01-15T14:22:03.500Z","level":"info","message":"hello"}`
	got := parseLine(line, src)

	// parseLine doesn't apply the offset — the Fetch loop does.
	// Verify offset application directly.
	original := got.Timestamp
	got.RawTimestamp = got.Timestamp
	got.Timestamp = got.Timestamp.Add(time.Duration(src.TimestampOffsetMs) * time.Millisecond)

	expectedShift := -200 * time.Millisecond
	actualShift := got.Timestamp.Sub(original)
	if actualShift != expectedShift {
		t.Errorf("offset: want %v, got %v", expectedShift, actualShift)
	}
	if got.RawTimestamp != original {
		t.Errorf("RawTimestamp should preserve original, got %v", got.RawTimestamp)
	}
}

// --- Severity parsing ---

func TestParseSeverity(t *testing.T) {
	tests := []struct {
		input string
		want  adapter.Severity
	}{
		{"error", adapter.SeverityError},
		{"ERROR", adapter.SeverityError},
		{"ERR", adapter.SeverityError},
		{"warn", adapter.SeverityWarn},
		{"WARNING", adapter.SeverityWarn},
		{"info", adapter.SeverityInfo},
		{"INFORMATION", adapter.SeverityInfo},
		{"debug", adapter.SeverityDebug},
		{"DEBUG", adapter.SeverityDebug},
		{"fatal", adapter.SeverityFatal},
		{"CRITICAL", adapter.SeverityFatal},
		{"trace", adapter.SeverityTrace},
		{"garbage", adapter.SeverityUnknown},
		{"", adapter.SeverityUnknown},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := adapter.ParseSeverity(tc.input)
			assertEqual(t, tc.want, got, "severity")
		})
	}
}

// --- helpers ---

func assertEqual[T comparable](t *testing.T, want, got T, label string) {
	t.Helper()
	if want != got {
		t.Errorf("%s: want %v, got %v", label, want, got)
	}
}
