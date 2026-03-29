package googlecloud

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mmediasoftwarelab/siftlog/internal/adapter"
	"github.com/mmediasoftwarelab/siftlog/internal/config"
)

// mockLoggingAPI implements LoggingAPI for testing.
type mockLoggingAPI struct {
	pages []listResponse
	page  int
	err   error
}

func (m *mockLoggingAPI) listEntries(_ context.Context, _ listRequest) (*listResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.page >= len(m.pages) {
		return &listResponse{}, nil
	}
	resp := m.pages[m.page]
	m.page++
	return &resp, nil
}

func makeEntry(ts time.Time, logName, severity, text string) logEntry {
	return logEntry{
		LogName:     logName,
		Severity:    severity,
		TextPayload: text,
		Timestamp:   ts,
		Labels:      map[string]string{},
		Resource:    resourceInfo{Labels: map[string]string{}},
	}
}

func makeJSONEntry(ts time.Time, logName, severity string, payload map[string]any) logEntry {
	return logEntry{
		LogName:     logName,
		Severity:    severity,
		JsonPayload: payload,
		Timestamp:   ts,
		Labels:      map[string]string{},
		Resource:    resourceInfo{Labels: map[string]string{}},
	}
}

func TestGoogleCloudFetch(t *testing.T) {
	ts1 := time.Date(2024, 1, 15, 14, 22, 3, 0, time.UTC)
	ts2 := time.Date(2024, 1, 15, 14, 22, 4, 0, time.UTC)

	mock := &mockLoggingAPI{
		pages: []listResponse{
			{Entries: []logEntry{
				makeEntry(ts1, "projects/p/logs/payment-service", "ERROR", "connection pool exhausted"),
				makeEntry(ts2, "projects/p/logs/order-service", "WARNING", "retry 1/3"),
			}},
		},
	}

	a := newWithClient(config.SourceConfig{Name: "test-gcl", Project: "p"}, mock)
	ch, err := a.Fetch(context.Background(), ts1.Add(-time.Minute), ts2.Add(time.Minute))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	var events []adapter.Event
	for ev := range ch {
		events = append(events, ev)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Severity != adapter.SeverityError {
		t.Errorf("severity: want ERROR, got %v", events[0].Severity)
	}
	if events[0].Message != "connection pool exhausted" {
		t.Errorf("message: want %q, got %q", "connection pool exhausted", events[0].Message)
	}
}

func TestGoogleCloudJSONPayload(t *testing.T) {
	ts := time.Date(2024, 1, 15, 14, 22, 3, 0, time.UTC)
	mock := &mockLoggingAPI{
		pages: []listResponse{
			{Entries: []logEntry{
				makeJSONEntry(ts, "projects/p/logs/svc", "ERROR", map[string]any{
					"message":  "upstream timeout",
					"trace_id": "abc123",
				}),
			}},
		},
	}

	a := newWithClient(config.SourceConfig{Name: "test-gcl", Project: "p"}, mock)
	ch, _ := a.Fetch(context.Background(), ts.Add(-time.Minute), ts.Add(time.Minute))

	var events []adapter.Event
	for ev := range ch {
		events = append(events, ev)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Message != "upstream timeout" {
		t.Errorf("message: want %q, got %q", "upstream timeout", events[0].Message)
	}
	if events[0].TraceID != "abc123" {
		t.Errorf("trace_id: want abc123, got %q", events[0].TraceID)
	}
}

func TestGoogleCloudTraceFromTopLevel(t *testing.T) {
	ts := time.Now()
	entry := makeEntry(ts, "projects/p/logs/svc", "ERROR", "err")
	entry.Trace = "projects/p/traces/def456"

	mock := &mockLoggingAPI{
		pages: []listResponse{{Entries: []logEntry{entry}}},
	}

	a := newWithClient(config.SourceConfig{Name: "test-gcl", Project: "p"}, mock)
	ch, _ := a.Fetch(context.Background(), ts.Add(-time.Minute), ts.Add(time.Minute))

	var events []adapter.Event
	for ev := range ch {
		events = append(events, ev)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].TraceID != "def456" {
		t.Errorf("trace_id: want def456, got %q", events[0].TraceID)
	}
}

func TestGoogleCloudPagination(t *testing.T) {
	ts := time.Now()
	mock := &mockLoggingAPI{
		pages: []listResponse{
			{
				Entries:       []logEntry{makeEntry(ts, "projects/p/logs/svc", "INFO", "page 1")},
				NextPageToken: "tok-abc",
			},
			{Entries: []logEntry{makeEntry(ts.Add(time.Second), "projects/p/logs/svc", "INFO", "page 2")}},
		},
	}

	a := newWithClient(config.SourceConfig{Name: "test-gcl", Project: "p"}, mock)
	ch, _ := a.Fetch(context.Background(), ts.Add(-time.Minute), ts.Add(time.Minute))

	count := 0
	for range ch {
		count++
	}
	if count != 2 {
		t.Errorf("expected 2 events across 2 pages, got %d", count)
	}
}

func TestGoogleCloudTimestampOffset(t *testing.T) {
	ts := time.Date(2024, 1, 15, 14, 22, 3, 0, time.UTC)
	mock := &mockLoggingAPI{
		pages: []listResponse{
			{Entries: []logEntry{makeEntry(ts, "projects/p/logs/svc", "INFO", "hello")}},
		},
	}

	a := newWithClient(config.SourceConfig{Name: "test-gcl", Project: "p", TimestampOffsetMs: -200}, mock)
	ch, _ := a.Fetch(context.Background(), ts.Add(-time.Minute), ts.Add(time.Minute))

	var events []adapter.Event
	for ev := range ch {
		events = append(events, ev)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	expected := ts.Add(-200 * time.Millisecond)
	if !events[0].Timestamp.Equal(expected) {
		t.Errorf("timestamp after offset: want %v, got %v", expected, events[0].Timestamp)
	}
}

func TestGoogleCloudAPIError(t *testing.T) {
	mock := &mockLoggingAPI{err: fmt.Errorf("permission denied")}
	a := newWithClient(config.SourceConfig{Name: "test-gcl", Project: "p"}, mock)
	ch, _ := a.Fetch(context.Background(), time.Now().Add(-time.Minute), time.Now())

	var events []adapter.Event
	for ev := range ch {
		events = append(events, ev)
	}
	if len(events) != 1 || events[0].Severity != adapter.SeverityError {
		t.Errorf("expected one error event for API failure, got %d events", len(events))
	}
}

func TestExtractService(t *testing.T) {
	tests := []struct {
		name  string
		entry logEntry
		want  string
	}{
		{
			name: "service label wins",
			entry: logEntry{
				Labels:   map[string]string{"service": "payment-service"},
				Resource: resourceInfo{Labels: map[string]string{"container_name": "other"}},
			},
			want: "payment-service",
		},
		{
			name: "container_name from resource",
			entry: logEntry{
				Labels:   map[string]string{},
				Resource: resourceInfo{Labels: map[string]string{"container_name": "order-service"}},
			},
			want: "order-service",
		},
		{
			name: "service_name from cloud run",
			entry: logEntry{
				Labels:   map[string]string{},
				Resource: resourceInfo{Labels: map[string]string{"service_name": "notification-svc"}},
			},
			want: "notification-svc",
		},
		{
			name: "log name fallback",
			entry: logEntry{
				LogName:  "projects/p/logs/auth-service",
				Labels:   map[string]string{},
				Resource: resourceInfo{Labels: map[string]string{}},
			},
			want: "auth-service",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractService(tc.entry)
			if got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestParseSeverity(t *testing.T) {
	tests := []struct {
		s    string
		want adapter.Severity
	}{
		{"ERROR", adapter.SeverityError},
		{"CRITICAL", adapter.SeverityError},
		{"ALERT", adapter.SeverityError},
		{"EMERGENCY", adapter.SeverityError},
		{"WARNING", adapter.SeverityWarn},
		{"INFO", adapter.SeverityInfo},
		{"NOTICE", adapter.SeverityInfo},
		{"DEFAULT", adapter.SeverityInfo},
		{"DEBUG", adapter.SeverityDebug},
		{"", adapter.SeverityInfo},
	}
	for _, tc := range tests {
		got := parseSeverity(tc.s)
		if got != tc.want {
			t.Errorf("parseSeverity(%q): want %v, got %v", tc.s, tc.want, got)
		}
	}
}

func TestBuildFilter(t *testing.T) {
	since := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2024, 1, 1, 1, 0, 0, 0, time.UTC)

	got := buildFilter("", since, until)
	if got != `timestamp>="2024-01-01T00:00:00Z" AND timestamp<="2024-01-01T01:00:00Z"` {
		t.Errorf("empty query filter: %q", got)
	}

	got = buildFilter("severity>=ERROR", since, until)
	if got != `(severity>=ERROR) AND timestamp>="2024-01-01T00:00:00Z" AND timestamp<="2024-01-01T01:00:00Z"` {
		t.Errorf("query filter: %q", got)
	}
}
