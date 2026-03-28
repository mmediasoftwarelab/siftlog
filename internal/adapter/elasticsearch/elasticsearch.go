package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/mmediasoftwarelab/siftlog/internal/adapter"
	"github.com/mmediasoftwarelab/siftlog/internal/config"
)

// Adapter queries Elasticsearch / OpenSearch via the _search API.
// Uses the scroll API for large result sets to avoid memory issues.
type Adapter struct {
	cfg    config.SourceConfig
	client httpClient
	auth   authConfig
}

type httpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type authConfig struct {
	apiKey   string
	username string
	password string
}

func New(cfg config.SourceConfig) (*Adapter, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("elasticsearch adapter %q: url is required", cfg.Name)
	}

	auth := authConfig{}
	switch cfg.Auth.Type {
	case "api_key":
		auth.apiKey = cfg.Auth.APIKey
		if cfg.Auth.TokenEnv != "" {
			auth.apiKey = os.Getenv(cfg.Auth.TokenEnv)
		}
	case "basic":
		auth.username = os.Getenv("ES_USERNAME")
		auth.password = os.Getenv("ES_PASSWORD")
	}

	return &Adapter{
		cfg:    cfg,
		client: &http.Client{Timeout: 30 * time.Second},
		auth:   auth,
	}, nil
}

func newWithClient(cfg config.SourceConfig, client httpClient) *Adapter {
	return &Adapter{cfg: cfg, client: client}
}

func (a *Adapter) Name() string { return a.cfg.Name }

func (a *Adapter) Fetch(ctx context.Context, since, until time.Time) (<-chan adapter.Event, error) {
	if since.IsZero() {
		since = time.Now().Add(-15 * time.Minute)
	}
	if until.IsZero() {
		until = time.Now()
	}

	ch := make(chan adapter.Event, 256)
	go func() {
		defer close(ch)
		if err := a.scroll(ctx, since, until, ch); err != nil {
			ch <- errorEvent(a.cfg.Name, err)
		}
	}()

	return ch, nil
}

// scroll uses the ES scroll API to page through all matching documents.
// Documents are sorted ascending by @timestamp so output is time-ordered.
func (a *Adapter) scroll(ctx context.Context, since, until time.Time, ch chan<- adapter.Event) error {
	// Initial search with scroll.
	query := a.buildQuery(since, until)
	body, err := json.Marshal(query)
	if err != nil {
		return err
	}

	url := a.cfg.URL + "/_search?scroll=1m&size=1000"
	resp, err := a.do(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}

	scrollID, events, err := parseScrollResponse(resp, a.cfg)
	if err != nil {
		return err
	}
	for _, ev := range events {
		applyOffset(&ev, a.cfg.TimestampOffsetMs)
		select {
		case ch <- ev:
		case <-ctx.Done():
			a.clearScroll(ctx, scrollID)
			return nil
		}
	}

	// Continue scrolling until empty page.
	for len(events) > 0 {
		scrollBody, _ := json.Marshal(map[string]string{
			"scroll":    "1m",
			"scroll_id": scrollID,
		})
		resp, err = a.do(ctx, http.MethodPost, a.cfg.URL+"/_search/scroll", bytes.NewReader(scrollBody))
		if err != nil {
			break
		}
		scrollID, events, err = parseScrollResponse(resp, a.cfg)
		if err != nil {
			break
		}
		for _, ev := range events {
			applyOffset(&ev, a.cfg.TimestampOffsetMs)
			select {
			case ch <- ev:
			case <-ctx.Done():
				a.clearScroll(ctx, scrollID)
				return nil
			}
		}
	}

	a.clearScroll(ctx, scrollID)
	return err
}

func (a *Adapter) clearScroll(ctx context.Context, scrollID string) {
	if scrollID == "" {
		return
	}
	body, _ := json.Marshal(map[string]string{"scroll_id": scrollID})
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, a.cfg.URL+"/_search/scroll", bytes.NewReader(body))
	a.setAuth(req)
	a.client.Do(req) //nolint:errcheck — best-effort cleanup
}

func (a *Adapter) buildQuery(since, until time.Time) map[string]any {
	return map[string]any{
		"query": map[string]any{
			"range": map[string]any{
				"@timestamp": map[string]any{
					"gte": since.UTC().Format(time.RFC3339Nano),
					"lte": until.UTC().Format(time.RFC3339Nano),
				},
			},
		},
		"sort": []map[string]any{
			{"@timestamp": map[string]string{"order": "asc"}},
		},
	}
}

func (a *Adapter) do(ctx context.Context, method, url string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	a.setAuth(req)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("elasticsearch request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 50*1024*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("elasticsearch returned %d: %s", resp.StatusCode, truncate(data, 256))
	}
	return data, nil
}

func (a *Adapter) setAuth(req *http.Request) {
	if a.auth.apiKey != "" {
		req.Header.Set("Authorization", "ApiKey "+a.auth.apiKey)
	} else if a.auth.username != "" {
		req.SetBasicAuth(a.auth.username, a.auth.password)
	}
}

// esScrollResponse is the JSON envelope from ES _search and _search/scroll.
type esScrollResponse struct {
	ScrollID string `json:"_scroll_id"`
	Hits     struct {
		Hits []struct {
			Source map[string]any `json:"_source"`
		} `json:"hits"`
	} `json:"hits"`
}

func parseScrollResponse(data []byte, cfg config.SourceConfig) (string, []adapter.Event, error) {
	var resp esScrollResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", nil, fmt.Errorf("elasticsearch parse error: %w", err)
	}

	events := make([]adapter.Event, 0, len(resp.Hits.Hits))
	for _, hit := range resp.Hits.Hits {
		ev := parseSource(hit.Source, cfg)
		events = append(events, ev)
	}
	return resp.ScrollID, events, nil
}

// parseSource maps an ES _source document to a normalized Event.
func parseSource(src map[string]any, cfg config.SourceConfig) adapter.Event {
	ev := adapter.Event{
		Source:    cfg.Name,
		Timestamp: time.Now(),
		Fields:    make(map[string]string),
	}

	// Timestamp.
	for _, k := range []string{"@timestamp", "timestamp", "time", "ts"} {
		if v, ok := src[k]; ok {
			if t, err := time.Parse(time.RFC3339Nano, fmt.Sprint(v)); err == nil {
				ev.Timestamp = t.UTC()
				ev.RawTimestamp = ev.Timestamp
				break
			}
			if t, err := time.Parse(time.RFC3339, fmt.Sprint(v)); err == nil {
				ev.Timestamp = t.UTC()
				ev.RawTimestamp = ev.Timestamp
				break
			}
		}
	}

	// Severity.
	for _, k := range []string{"level", "severity", "log.level", "lvl"} {
		if v, ok := src[k]; ok {
			ev.Severity = adapter.ParseSeverity(fmt.Sprint(v))
			break
		}
	}

	// Message.
	for _, k := range []string{"message", "msg", "log.message", "log"} {
		if v, ok := src[k]; ok {
			ev.Message = fmt.Sprint(v)
			break
		}
	}

	// Service.
	for _, k := range []string{"service.name", "service", "app", "application"} {
		if v, ok := src[k]; ok {
			ev.Service = fmt.Sprint(v)
			break
		}
	}

	// Trace ID.
	for _, k := range []string{"trace.id", "trace_id", "traceId", "request_id"} {
		if v, ok := src[k]; ok {
			ev.TraceID = fmt.Sprint(v)
			break
		}
	}

	// Remaining fields.
	skip := map[string]bool{
		"@timestamp": true, "timestamp": true, "time": true, "ts": true,
		"level": true, "severity": true, "log.level": true, "lvl": true,
		"message": true, "msg": true, "log": true, "log.message": true,
		"service.name": true, "service": true, "app": true, "application": true,
		"trace.id": true, "trace_id": true, "traceId": true, "request_id": true,
	}
	for k, v := range src {
		if !skip[k] {
			ev.Fields[k] = fmt.Sprint(v)
		}
	}

	return ev
}

func applyOffset(ev *adapter.Event, offsetMs int64) {
	if offsetMs != 0 {
		ev.RawTimestamp = ev.Timestamp
		ev.Timestamp = ev.Timestamp.Add(time.Duration(offsetMs) * time.Millisecond)
	}
}

func errorEvent(source string, err error) adapter.Event {
	return adapter.Event{
		Timestamp: time.Now(),
		Source:    source,
		Service:   source,
		Severity:  adapter.SeverityError,
		Message:   fmt.Sprintf("elasticsearch adapter error: %v", err),
	}
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}
