package elasticsearch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mmediasoftwarelab/siftlog/internal/adapter"
	"github.com/mmediasoftwarelab/siftlog/internal/config"
)

// mockHTTPClient implements httpClient for testing.
type mockHTTPClient struct {
	responses []*http.Response
	idx       int
	err       error
}

func (m *mockHTTPClient) Do(_ *http.Request) (*http.Response, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.idx >= len(m.responses) {
		return jsonResp(200, map[string]any{
			"hits": map[string]any{"hits": []any{}},
		}), nil
	}
	r := m.responses[m.idx]
	m.idx++
	return r, nil
}

func jsonResp(status int, body any) *http.Response {
	b, _ := json.Marshal(body)
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(b)),
		Header:     make(http.Header),
	}
}

func makeHit(ts time.Time, level, msg, service, traceID string) map[string]any {
	src := map[string]any{
		"@timestamp": ts.UTC().Format(time.RFC3339Nano),
		"level":      level,
		"message":    msg,
		"service":    service,
	}
	if traceID != "" {
		src["trace_id"] = traceID
	}
	return map[string]any{"_source": src}
}

func makeScrollResp(scrollID string, hits []map[string]any) map[string]any {
	return map[string]any{
		"_scroll_id": scrollID,
		"hits":       map[string]any{"hits": hits},
	}
}

func TestElasticsearchFetch(t *testing.T) {
	ts1 := time.Date(2024, 1, 15, 14, 22, 3, 0, time.UTC)
	ts2 := time.Date(2024, 1, 15, 14, 22, 4, 0, time.UTC)

	mock := &mockHTTPClient{
		responses: []*http.Response{
			// Initial scroll response.
			jsonResp(200, makeScrollResp("scroll-1", []map[string]any{
				makeHit(ts1, "error", "timeout", "payment-service", "abc"),
				makeHit(ts2, "warn", "retrying", "order-service", ""),
			})),
			// Empty page signals end of scroll.
			jsonResp(200, makeScrollResp("scroll-1", nil)),
			// DELETE scroll cleanup.
			jsonResp(200, map[string]any{}),
		},
	}

	a := newWithClient(config.SourceConfig{Name: "test-es", URL: "http://localhost:9200"}, mock)
	ch, err := a.Fetch(t.Context(), ts1.Add(-time.Minute), ts2.Add(time.Minute))
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
		t.Errorf("first event severity: want ERROR, got %v", events[0].Severity)
	}
	if events[0].TraceID != "abc" {
		t.Errorf("trace_id: want abc, got %q", events[0].TraceID)
	}
	if events[0].Service != "payment-service" {
		t.Errorf("service: want payment-service, got %q", events[0].Service)
	}
}

func TestElasticsearchBuildQuery(t *testing.T) {
	since := time.Date(2024, 1, 15, 14, 0, 0, 0, time.UTC)
	until := time.Date(2024, 1, 15, 15, 0, 0, 0, time.UTC)

	a := &Adapter{cfg: config.SourceConfig{}}
	q := a.buildQuery(since, until)

	qJSON, _ := json.Marshal(q)
	qs := string(qJSON)

	if !strings.Contains(qs, `"gte"`) || !strings.Contains(qs, `"lte"`) {
		t.Errorf("query missing range bounds: %s", qs)
	}
	if !strings.Contains(qs, `"@timestamp"`) {
		t.Errorf("query should range on @timestamp: %s", qs)
	}
	if !strings.Contains(qs, `"asc"`) {
		t.Errorf("query should sort ascending: %s", qs)
	}
}

func TestElasticsearchTimestampOffset(t *testing.T) {
	ts := time.Date(2024, 1, 15, 14, 22, 3, 0, time.UTC)

	mock := &mockHTTPClient{
		responses: []*http.Response{
			jsonResp(200, makeScrollResp("s1", []map[string]any{
				makeHit(ts, "info", "hello", "svc", ""),
			})),
			jsonResp(200, makeScrollResp("s1", nil)),
			jsonResp(200, map[string]any{}),
		},
	}

	a := newWithClient(config.SourceConfig{
		Name:              "test-es",
		URL:               "http://localhost:9200",
		TimestampOffsetMs: -200,
	}, mock)

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
}

func TestElasticsearchHTTPError(t *testing.T) {
	mock := &mockHTTPClient{err: fmt.Errorf("connection refused")}
	a := newWithClient(config.SourceConfig{Name: "test-es", URL: "http://localhost:9200"}, mock)

	ch, _ := a.Fetch(t.Context(), time.Now().Add(-time.Minute), time.Now())
	var events []adapter.Event
	for ev := range ch {
		events = append(events, ev)
	}

	if len(events) != 1 || events[0].Severity != adapter.SeverityError {
		t.Errorf("expected one error event, got %d", len(events))
	}
}

func TestElasticsearchAuthAPIKey(t *testing.T) {
	var gotAuth string
	mock := &mockHTTPClient{}
	mock.responses = []*http.Response{
		{
			StatusCode: 200,
			Body: io.NopCloser(bytes.NewReader(func() []byte {
				b, _ := json.Marshal(makeScrollResp("s", nil))
				return b
			}())),
			Header: make(http.Header),
		},
	}

	// Intercept via a custom httpClient that captures the header.
	interceptor := &authCapture{inner: mock}
	a := &Adapter{
		cfg:    config.SourceConfig{Name: "test", URL: "http://localhost:9200"},
		client: interceptor,
		auth:   authConfig{apiKey: "my-api-key"},
	}

	ch, _ := a.Fetch(t.Context(), time.Now().Add(-time.Minute), time.Now())
	for range ch {
	}
	gotAuth = interceptor.lastAuth

	if !strings.HasPrefix(gotAuth, "ApiKey ") {
		t.Errorf("auth header: want ApiKey prefix, got %q", gotAuth)
	}
}

type authCapture struct {
	inner    httpClient
	lastAuth string
}

func (c *authCapture) Do(req *http.Request) (*http.Response, error) {
	c.lastAuth = req.Header.Get("Authorization")
	return c.inner.Do(req)
}
