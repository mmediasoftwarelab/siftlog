package loki

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mmediasoftwarelab/siftlog/internal/adapter"
	"github.com/mmediasoftwarelab/siftlog/internal/config"
)

// makeLokiResponse builds a minimal Loki query_range JSON response.
func makeLokiResponse(streams []struct {
	labels map[string]string
	values [][2]string
}) string {
	type result struct {
		Stream map[string]string `json:"stream"`
		Values [][2]string       `json:"values"`
	}
	results := make([]result, len(streams))
	for i, s := range streams {
		results[i] = result{Stream: s.labels, Values: s.values}
	}
	body, _ := json.Marshal(map[string]any{
		"status": "success",
		"data":   map[string]any{"resultType": "streams", "result": results},
	})
	return string(body)
}

// nanoStr formats a time as a nanosecond Unix epoch string, as Loki expects.
func nanoStr(t time.Time) string {
	return fmt.Sprintf("%d", t.UnixNano())
}

// TestLokiAdapterFetch verifies the adapter fetches and parses events correctly.
func TestLokiAdapterFetch(t *testing.T) {
	ts1 := time.Date(2024, 1, 15, 14, 22, 3, 441000000, time.UTC)
	ts2 := time.Date(2024, 1, 15, 14, 22, 4, 0, time.UTC)

	body := makeLokiResponse([]struct {
		labels map[string]string
		values [][2]string
	}{
		{
			labels: map[string]string{"service": "payment-service", "env": "prod"},
			values: [][2]string{
				{nanoStr(ts2), `{"level":"error","message":"timeout","trace_id":"abc"}`},
				{nanoStr(ts1), `{"level":"warn","message":"pool pressure"}`},
			},
		},
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/loki/api/v1/query_range") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	defer srv.Close()

	a, err := New(config.SourceConfig{
		Name: "test-loki",
		Type: "loki",
		URL:  srv.URL,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	since := ts1.Add(-time.Minute)
	until := ts2.Add(time.Minute)

	ch, err := a.Fetch(t.Context(), since, until)
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
	// Events should be in ascending timestamp order (oldest first).
	if !events[0].Timestamp.Before(events[1].Timestamp) {
		t.Errorf("events should be ascending by timestamp")
	}
	if events[1].Severity != adapter.SeverityError {
		t.Errorf("second event severity: want ERROR, got %v", events[1].Severity)
	}
	if events[1].TraceID != "abc" {
		t.Errorf("trace_id: want abc, got %q", events[1].TraceID)
	}
	if events[0].Service != "payment-service" {
		t.Errorf("service: want payment-service, got %q", events[0].Service)
	}
	// env label should be in Fields.
	if events[0].Fields["env"] != "prod" {
		t.Errorf("env field: want prod, got %q", events[0].Fields["env"])
	}
}

// TestLokiAdapterAuthHeader verifies the Bearer token is sent.
func TestLokiAdapterAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"data":{"result":[]}}`))
	}))
	defer srv.Close()

	t.Setenv("TEST_LOKI_TOKEN", "secret-token")

	a, _ := New(config.SourceConfig{
		Name: "test-loki",
		URL:  srv.URL,
		Auth: config.AuthConfig{TokenEnv: "TEST_LOKI_TOKEN"},
	})

	ch, _ := a.Fetch(t.Context(), time.Now().Add(-time.Minute), time.Now())
	for range ch {
	}

	if gotAuth != "Bearer secret-token" {
		t.Errorf("auth header: want %q, got %q", "Bearer secret-token", gotAuth)
	}
}

// TestLokiAdapterErrorResponse verifies HTTP errors surface as error events.
func TestLokiAdapterErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	a, _ := New(config.SourceConfig{Name: "test-loki", URL: srv.URL})
	ch, _ := a.Fetch(t.Context(), time.Now().Add(-time.Minute), time.Now())

	var events []adapter.Event
	for ev := range ch {
		events = append(events, ev)
	}

	if len(events) != 1 || events[0].Severity != adapter.SeverityError {
		t.Errorf("expected one error event for HTTP 401, got %d events", len(events))
	}
}

// TestLokiBuildQuery verifies the LogQL selector is built from labels config.
func TestLokiBuildQuery(t *testing.T) {
	a := &Adapter{cfg: config.SourceConfig{
		Labels: map[string]string{"env": "production", "namespace": "payments"},
	}}
	q := a.buildQuery()
	if !strings.Contains(q, `env="production"`) {
		t.Errorf("query missing env label: %s", q)
	}
	if !strings.Contains(q, `namespace="payments"`) {
		t.Errorf("query missing namespace label: %s", q)
	}
}

// TestLokiTimestampOffset verifies per-source offset is applied.
func TestLokiTimestampOffset(t *testing.T) {
	ts := time.Date(2024, 1, 15, 14, 22, 3, 0, time.UTC)
	body := makeLokiResponse([]struct {
		labels map[string]string
		values [][2]string
	}{
		{
			labels: map[string]string{"service": "svc"},
			values: [][2]string{{nanoStr(ts), "plain text log line"}},
		},
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	a, _ := New(config.SourceConfig{
		Name:              "test-loki",
		URL:               srv.URL,
		TimestampOffsetMs: -200,
	})

	ch, _ := a.Fetch(t.Context(), ts.Add(-time.Minute), ts.Add(time.Minute))
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
	if !events[0].RawTimestamp.Equal(ts) {
		t.Errorf("raw timestamp should be preserved: want %v, got %v", ts, events[0].RawTimestamp)
	}
}

