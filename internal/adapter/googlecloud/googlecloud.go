package googlecloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2/google"

	"github.com/mmediasoftwarelab/siftlog/internal/adapter"
	"github.com/mmediasoftwarelab/siftlog/internal/config"
)

const apiBase = "https://logging.googleapis.com/v2"
const loggingScope = "https://www.googleapis.com/auth/logging.read"

// LoggingAPI is the subset of the Cloud Logging API we use.
// Defined as an interface so tests can inject a mock.
type LoggingAPI interface {
	listEntries(ctx context.Context, req listRequest) (*listResponse, error)
}

// Adapter queries Google Cloud Logging via the REST API v2.
// Docs: https://cloud.google.com/logging/docs/reference/v2/rest/v2/entries/list
type Adapter struct {
	cfg    config.SourceConfig
	client LoggingAPI
}

// New creates an Adapter using Application Default Credentials.
// Set GOOGLE_APPLICATION_CREDENTIALS to a service account key file,
// or run on GCP where workload identity is available.
func New(cfg config.SourceConfig) (*Adapter, error) {
	if cfg.Project == "" {
		return nil, fmt.Errorf("googlecloud adapter %q: project is required", cfg.Name)
	}

	creds, err := google.FindDefaultCredentials(context.Background(), loggingScope)
	if err != nil {
		return nil, fmt.Errorf("googlecloud adapter %q: credentials: %w", cfg.Name, err)
	}

	return &Adapter{
		cfg:    cfg,
		client: &httpClient{creds: creds, http: &http.Client{Timeout: 30 * time.Second}},
	}, nil
}

// newWithClient creates an Adapter with a pre-built client (used in tests).
func newWithClient(cfg config.SourceConfig, client LoggingAPI) *Adapter {
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
	filter := buildFilter(a.cfg.Query, since, until)
	token := ""

	for {
		req := listRequest{
			ResourceNames: []string{"projects/" + a.cfg.Project},
			Filter:        filter,
			OrderBy:       "timestamp asc",
			PageSize:      1000,
			PageToken:     token,
		}

		resp, err := a.client.listEntries(ctx, req)
		if err != nil {
			return err
		}

		for _, entry := range resp.Entries {
			ev := toEvent(entry, a.cfg)
			applyOffset(&ev, a.cfg.TimestampOffsetMs)
			select {
			case ch <- ev:
			case <-ctx.Done():
				return nil
			}
		}

		if resp.NextPageToken == "" {
			return nil
		}
		token = resp.NextPageToken
	}
}

// buildFilter combines the user-supplied query with a timestamp range filter.
func buildFilter(query string, since, until time.Time) string {
	ts := fmt.Sprintf(`timestamp>="%s" AND timestamp<="%s"`,
		since.UTC().Format(time.RFC3339),
		until.UTC().Format(time.RFC3339),
	)
	if query == "" {
		return ts
	}
	return "(" + query + ") AND " + ts
}

// httpClient implements LoggingAPI against the real Cloud Logging REST API.
type httpClient struct {
	creds *google.Credentials
	http  *http.Client
}

func (c *httpClient) listEntries(ctx context.Context, body listRequest) (*listResponse, error) {
	token, err := c.creds.TokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("googlecloud: token refresh: %w", err)
	}

	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		apiBase+"/entries:list", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("googlecloud request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("googlecloud returned %d: %s", resp.StatusCode, b)
	}

	var result listResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("googlecloud response parse error: %w", err)
	}
	return &result, nil
}

// API request/response types.

type listRequest struct {
	ResourceNames []string `json:"resourceNames"`
	Filter        string   `json:"filter,omitempty"`
	OrderBy       string   `json:"orderBy,omitempty"`
	PageSize      int      `json:"pageSize,omitempty"`
	PageToken     string   `json:"pageToken,omitempty"`
}

type listResponse struct {
	Entries       []logEntry `json:"entries"`
	NextPageToken string     `json:"nextPageToken"`
}

type logEntry struct {
	LogName  string            `json:"logName"`
	Resource resourceInfo      `json:"resource"`
	Severity string            `json:"severity"`
	// Only one of these is set per entry.
	TextPayload string         `json:"textPayload"`
	JsonPayload map[string]any `json:"jsonPayload"`
	Timestamp   time.Time      `json:"timestamp"`
	Labels      map[string]string `json:"labels"`
	Trace       string         `json:"trace"`
}

type resourceInfo struct {
	Type   string            `json:"type"`
	Labels map[string]string `json:"labels"`
}

func toEvent(e logEntry, cfg config.SourceConfig) adapter.Event {
	t := e.Timestamp
	if t.IsZero() {
		t = time.Now()
	}

	ev := adapter.Event{
		Timestamp:    t,
		RawTimestamp: t,
		Source:       cfg.Name,
		Service:      extractService(e),
		Severity:     parseSeverity(e.Severity),
		Fields:       make(map[string]string),
	}

	// Extract message.
	if e.TextPayload != "" {
		ev.Message = e.TextPayload
	} else if e.JsonPayload != nil {
		for _, k := range []string{"message", "msg", "log"} {
			if v, ok := e.JsonPayload[k]; ok {
				ev.Message = fmt.Sprint(v)
				break
			}
		}
		// Also check for trace ID in the JSON payload.
		for _, k := range []string{"trace_id", "traceId", "request_id"} {
			if v, ok := e.JsonPayload[k]; ok {
				ev.TraceID = fmt.Sprint(v)
				break
			}
		}
	}

	// Extract trace ID from the top-level trace field if not found in payload.
	// Format: "projects/PROJECT/traces/TRACE_ID"
	if ev.TraceID == "" && e.Trace != "" {
		if i := strings.LastIndex(e.Trace, "/"); i >= 0 {
			ev.TraceID = e.Trace[i+1:]
		}
	}

	return ev
}

// extractService returns the service name from labels, resource labels, or logName.
func extractService(e logEntry) string {
	// Explicit service label is most reliable.
	if v := e.Labels["service"]; v != "" {
		return v
	}
	// Resource-type-specific labels.
	for _, k := range []string{"container_name", "service_name", "module_id"} {
		if v := e.Resource.Labels[k]; v != "" {
			return v
		}
	}
	// Fall back to the last segment of the log name after "/logs/".
	// e.g. "projects/p/logs/payment-service" -> "payment-service"
	if i := strings.LastIndex(e.LogName, "/logs/"); i >= 0 {
		name := e.LogName[i+6:]
		// URL-decode %2F separators used by GCL for structured log names.
		name = strings.ReplaceAll(name, "%2F", "/")
		return name
	}
	return ""
}

func parseSeverity(s string) adapter.Severity {
	switch strings.ToUpper(s) {
	case "EMERGENCY", "ALERT", "CRITICAL", "ERROR":
		return adapter.SeverityError
	case "WARNING":
		return adapter.SeverityWarn
	case "NOTICE", "INFO", "DEFAULT":
		return adapter.SeverityInfo
	case "DEBUG":
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
