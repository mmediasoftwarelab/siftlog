package datadog

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mmediasoftwarelab/siftlog/internal/adapter"
	"github.com/mmediasoftwarelab/siftlog/internal/config"
)

// mockLogsAPI implements LogsAPI for testing.
type mockLogsAPI struct {
	pages []searchResponse
	page  int
	err   error
}

func (m *mockLogsAPI) search(_ context.Context, _ searchRequest) (*searchResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.page >= len(m.pages) {
		return &searchResponse{}, nil
	}
	resp := m.pages[m.page]
	m.page++
	return &resp, nil
}

func makeEntry(ts time.Time, service, status, message, traceID string) logEntry {
	attrs := map[string]any{}
	if traceID != "" {
		attrs["trace_id"] = traceID
	}
	return logEntry{Attributes: logAttributes{
		Timestamp:  ts,
		Status:     status,
		Message:    message,
		Service:    service,
		Attributes: attrs,
	}}
}

func TestDatadogFetch(t *testing.T) {
	ts1 := time.Date(2024, 1, 15, 14, 22, 3, 0, time.UTC)
	ts2 := time.Date(2024, 1, 15, 14, 22, 4, 0, time.UTC)

	mock := &mockLogsAPI{
		pages: []searchResponse{
			{Data: []logEntry{
				makeEntry(ts1, "payment-service", "error", "connection pool exhausted", "abc123"),
				makeEntry(ts2, "order-service", "warning", "retry 1/3", ""),
			}},
		},
	}

	a := newWithClient(config.SourceConfig{Name: "test-dd"}, mock)
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
	if events[0].TraceID != "abc123" {
		t.Errorf("trace_id: want abc123, got %q", events[0].TraceID)
	}
	if events[0].Service != "payment-service" {
		t.Errorf("service: want payment-service, got %q", events[0].Service)
	}
}

func TestDatadogPagination(t *testing.T) {
	ts := time.Now()
	mock := &mockLogsAPI{
		pages: []searchResponse{
			{
				Data: []logEntry{makeEntry(ts, "svc", "info", "page 1", "")},
				Meta: struct {
					Page struct {
						After string `json:"after"`
					} `json:"page"`
				}{Page: struct {
					After string `json:"after"`
				}{After: "cursor-abc"}},
			},
			{Data: []logEntry{makeEntry(ts.Add(time.Second), "svc", "info", "page 2", "")}},
		},
	}

	a := newWithClient(config.SourceConfig{Name: "test-dd"}, mock)
	ch, _ := a.Fetch(context.Background(), ts.Add(-time.Minute), ts.Add(time.Minute))

	count := 0
	for range ch {
		count++
	}
	if count != 2 {
		t.Errorf("expected 2 events across 2 pages, got %d", count)
	}
}

func TestDatadogTimestampOffset(t *testing.T) {
	ts := time.Date(2024, 1, 15, 14, 22, 3, 0, time.UTC)
	mock := &mockLogsAPI{
		pages: []searchResponse{
			{Data: []logEntry{makeEntry(ts, "svc", "info", "hello", "")}},
		},
	}

	a := newWithClient(config.SourceConfig{Name: "test-dd", TimestampOffsetMs: -200}, mock)
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

func TestDatadogAPIError(t *testing.T) {
	mock := &mockLogsAPI{err: fmt.Errorf("unauthorized")}
	a := newWithClient(config.SourceConfig{Name: "test-dd"}, mock)
	ch, _ := a.Fetch(context.Background(), time.Now().Add(-time.Minute), time.Now())

	var events []adapter.Event
	for ev := range ch {
		events = append(events, ev)
	}
	if len(events) != 1 || events[0].Severity != adapter.SeverityError {
		t.Errorf("expected one error event for API failure, got %d events", len(events))
	}
}

func TestDatadogParseSeverity(t *testing.T) {
	tests := []struct {
		status string
		want   adapter.Severity
	}{
		{"error", adapter.SeverityError},
		{"critical", adapter.SeverityError},
		{"alert", adapter.SeverityError},
		{"emergency", adapter.SeverityError},
		{"warning", adapter.SeverityWarn},
		{"warn", adapter.SeverityWarn},
		{"info", adapter.SeverityInfo},
		{"notice", adapter.SeverityInfo},
		{"debug", adapter.SeverityDebug},
		{"unknown", adapter.SeverityInfo},
	}
	for _, tc := range tests {
		got := parseSeverity(tc.status)
		if got != tc.want {
			t.Errorf("parseSeverity(%q): want %v, got %v", tc.status, tc.want, got)
		}
	}
}
