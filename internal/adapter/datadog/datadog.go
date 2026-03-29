package datadog

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

const defaultBase = "https://api.datadoghq.com"

// LogsAPI is the subset of the Datadog Logs API we use.
// Defined as an interface so tests can inject a mock.
type LogsAPI interface {
	search(ctx context.Context, req searchRequest) (*searchResponse, error)
}

// Adapter queries the Datadog Logs v2 API.
// Docs: https://docs.datadoghq.com/api/latest/logs/
type Adapter struct {
	cfg    config.SourceConfig
	client LogsAPI
}

// New creates an Adapter using config-supplied or environment-supplied credentials.
// DD-API-KEY is required. DD-APPLICATION-KEY is recommended but optional.
func New(cfg config.SourceConfig) (*Adapter, error) {
	apiKey := cfg.Auth.APIKey
	if apiKey == "" && cfg.Auth.TokenEnv != "" {
		apiKey = os.Getenv(cfg.Auth.TokenEnv)
	}
	if apiKey == "" {
		apiKey = os.Getenv("DD_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("datadog adapter %q: api key required (set auth.api_key, auth.token_env, or DD_API_KEY)", cfg.Name)
	}

	appKey := ""
	if cfg.Auth.AppKeyEnv != "" {
		appKey = os.Getenv(cfg.Auth.AppKeyEnv)
	}
	if appKey == "" {
		appKey = os.Getenv("DD_APP_KEY")
	}

	base := cfg.URL
	if base == "" {
		base = defaultBase
	}

	return &Adapter{
		cfg:    cfg,
		client: &httpClient{base: base, apiKey: apiKey, appKey: appKey, http: &http.Client{Timeout: 30 * time.Second}},
	}, nil
}

// newWithClient creates an Adapter with a pre-built client (used in tests).
func newWithClient(cfg config.SourceConfig, client LogsAPI) *Adapter {
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
		if err := a.fetchAll(ctx, since, until, ch); err != nil {
			ch <- errorEvent(a.cfg.Name, err)
		}
	}()

	return ch, nil
}

func (a *Adapter) fetchAll(ctx context.Context, since, until time.Time, ch chan<- adapter.Event) error {
	query := a.cfg.Query
	if query == "" {
		query = "*"
	}

	cursor := ""
	for {
		req := searchRequest{
			Filter: searchFilter{
				Query: query,
				From:  since.UTC().Format(time.RFC3339),
				To:    until.UTC().Format(time.RFC3339),
			},
			Sort: "timestamp",
			Page: searchPage{Limit: 1000, Cursor: cursor},
		}

		resp, err := a.client.search(ctx, req)
		if err != nil {
			return err
		}

		for _, entry := range resp.Data {
			ev := toEvent(entry, a.cfg)
			applyOffset(&ev, a.cfg.TimestampOffsetMs)
			select {
			case ch <- ev:
			case <-ctx.Done():
				return nil
			}
		}

		if resp.Meta.Page.After == "" || len(resp.Data) == 0 {
			return nil
		}
		cursor = resp.Meta.Page.After
	}
}

// httpClient implements LogsAPI against the real Datadog HTTP API.
type httpClient struct {
	base   string
	apiKey string
	appKey string
	http   *http.Client
}

func (c *httpClient) search(ctx context.Context, body searchRequest) (*searchResponse, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/api/v2/logs/events/search", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("DD-API-KEY", c.apiKey)
	if c.appKey != "" {
		req.Header.Set("DD-APPLICATION-KEY", c.appKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("datadog request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("datadog returned %d: %s", resp.StatusCode, b)
	}

	var result searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("datadog response parse error: %w", err)
	}
	return &result, nil
}

// API request/response types.

type searchRequest struct {
	Filter searchFilter `json:"filter"`
	Sort   string       `json:"sort"`
	Page   searchPage   `json:"page"`
}

type searchFilter struct {
	Query string `json:"query"`
	From  string `json:"from"`
	To    string `json:"to"`
}

type searchPage struct {
	Limit  int    `json:"limit"`
	Cursor string `json:"cursor,omitempty"`
}

type searchResponse struct {
	Data []logEntry `json:"data"`
	Meta struct {
		Page struct {
			After string `json:"after"`
		} `json:"page"`
	} `json:"meta"`
}

type logEntry struct {
	Attributes logAttributes `json:"attributes"`
}

type logAttributes struct {
	Timestamp  time.Time      `json:"timestamp"`
	Status     string         `json:"status"`
	Message    string         `json:"message"`
	Service    string         `json:"service"`
	Attributes map[string]any `json:"attributes"`
}

func toEvent(e logEntry, cfg config.SourceConfig) adapter.Event {
	t := e.Attributes.Timestamp
	if t.IsZero() {
		t = time.Now()
	}

	ev := adapter.Event{
		Timestamp:    t,
		RawTimestamp: t,
		Source:       cfg.Name,
		Service:      e.Attributes.Service,
		Message:      e.Attributes.Message,
		Severity:     parseSeverity(e.Attributes.Status),
		Fields:       make(map[string]string),
	}

	if attrs := e.Attributes.Attributes; attrs != nil {
		for _, k := range []string{"trace_id", "dd.trace_id", "traceId"} {
			if v, ok := attrs[k]; ok {
				ev.TraceID = fmt.Sprint(v)
				break
			}
		}
	}

	return ev
}

func parseSeverity(status string) adapter.Severity {
	switch status {
	case "emergency", "alert", "critical", "error":
		return adapter.SeverityError
	case "warning", "warn":
		return adapter.SeverityWarn
	case "notice", "info":
		return adapter.SeverityInfo
	case "debug":
		return adapter.SeverityDebug
	default:
		return adapter.SeverityInfo
	}
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
		Message:   fmt.Sprintf("adapter error: %v", err),
	}
}
