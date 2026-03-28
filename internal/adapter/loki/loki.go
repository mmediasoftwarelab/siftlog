package loki

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/mmediasoftwarelab/siftlog/internal/adapter"
	"github.com/mmediasoftwarelab/siftlog/internal/config"
)

// Adapter queries Grafana Loki via its HTTP query_range API.
// Docs: https://grafana.com/docs/loki/latest/reference/loki-http-api/
type Adapter struct {
	cfg    config.SourceConfig
	client *http.Client
	token  string
}

func New(cfg config.SourceConfig) (*Adapter, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("loki adapter %q: url is required", cfg.Name)
	}

	token := ""
	if cfg.Auth.TokenEnv != "" {
		token = os.Getenv(cfg.Auth.TokenEnv)
	}

	return &Adapter{
		cfg:    cfg,
		client: &http.Client{Timeout: 30 * time.Second},
		token:  token,
	}, nil
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
		if err := a.queryRange(ctx, since, until, ch); err != nil {
			// Surface the error as a synthetic event so the user sees it.
			ch <- errorEvent(a.cfg.Name, err)
		}
	}()

	return ch, nil
}

// queryRange pages through Loki's query_range endpoint and sends events to ch.
// Loki returns results newest-first within each page, so we reverse each batch
// before sending to preserve ascending timestamp order.
func (a *Adapter) queryRange(ctx context.Context, since, until time.Time, ch chan<- adapter.Event) error {
	query := a.buildQuery()
	limit := 1000
	end := until

	for {
		batch, err := a.fetchBatch(ctx, query, since, end, limit)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}

		// Reverse: Loki returns newest first within a batch.
		for i, j := 0, len(batch)-1; i < j; i, j = i+1, j-1 {
			batch[i], batch[j] = batch[j], batch[i]
		}

		for _, ev := range batch {
			applyOffset(&ev, a.cfg.TimestampOffsetMs)
			select {
			case ch <- ev:
			case <-ctx.Done():
				return nil
			}
		}

		if len(batch) < limit {
			return nil // last page
		}

		// Next page ends just before the oldest event in this page.
		end = batch[0].Timestamp.Add(-time.Nanosecond)
		if !end.After(since) {
			return nil
		}
	}
}

func (a *Adapter) fetchBatch(ctx context.Context, query string, start, end time.Time, limit int) ([]adapter.Event, error) {
	u, err := url.Parse(a.cfg.URL + "/loki/api/v1/query_range")
	if err != nil {
		return nil, fmt.Errorf("invalid loki url: %w", err)
	}

	q := u.Query()
	q.Set("query", query)
	q.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	q.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	q.Set("limit", strconv.Itoa(limit))
	q.Set("direction", "backward")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("loki request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("loki returned %d: %s", resp.StatusCode, body)
	}

	return parseLokiResponse(resp.Body, a.cfg)
}

// buildQuery constructs a LogQL stream selector from the source labels config.
// Example: {env="production",namespace="payments"}
func (a *Adapter) buildQuery() string {
	if len(a.cfg.Labels) == 0 {
		return `{job=~".+"}`
	}
	q := "{"
	i := 0
	for k, v := range a.cfg.Labels {
		if i > 0 {
			q += ","
		}
		q += fmt.Sprintf(`%s=%q`, k, v)
		i++
	}
	return q + "}"
}

// lokiResponse is the JSON envelope returned by /loki/api/v1/query_range.
type lokiResponse struct {
	Data struct {
		Result []struct {
			Stream map[string]string `json:"stream"`
			Values [][2]string       `json:"values"` // [nanosecond_ts, log_line]
		} `json:"result"`
	} `json:"data"`
}

func parseLokiResponse(r io.Reader, cfg config.SourceConfig) ([]adapter.Event, error) {
	var resp lokiResponse
	if err := json.NewDecoder(r).Decode(&resp); err != nil {
		return nil, fmt.Errorf("loki response parse error: %w", err)
	}

	var events []adapter.Event
	for _, stream := range resp.Data.Result {
		service := streamLabel(stream.Stream, "service", "app", "job", "container")

		for _, val := range stream.Values {
			tsNano, err := strconv.ParseInt(val[0], 10, 64)
			if err != nil {
				continue
			}
			t := time.Unix(0, tsNano).UTC()

			ev := adapter.Event{
				Timestamp:    t,
				RawTimestamp: t,
				Source:       cfg.Name,
				Service:      service,
				Fields:       make(map[string]string),
			}

			// Copy stream labels into Fields, extract known signal fields.
			for k, v := range stream.Stream {
				switch k {
				case "service", "app", "job", "container":
					// already captured as Service
				default:
					ev.Fields[k] = v
				}
			}

			// Parse the log line itself (JSON or plain text).
			parseLine(val[1], &ev)
			events = append(events, ev)
		}
	}
	return events, nil
}

// streamLabel returns the value of the first matching label key.
func streamLabel(labels map[string]string, keys ...string) string {
	for _, k := range keys {
		if v, ok := labels[k]; ok {
			return v
		}
	}
	return ""
}

// parseLine extracts severity, message, and trace ID from a log line.
// Tries JSON first, falls back to plain text heuristics.
func parseLine(line string, ev *adapter.Event) {
	if len(line) > 0 && line[0] == '{' {
		var raw map[string]any
		if json.Unmarshal([]byte(line), &raw) == nil {
			for _, k := range []string{"level", "severity", "lvl"} {
				if v, ok := raw[k]; ok {
					ev.Severity = adapter.ParseSeverity(fmt.Sprint(v))
					break
				}
			}
			for _, k := range []string{"message", "msg", "log"} {
				if v, ok := raw[k]; ok {
					ev.Message = fmt.Sprint(v)
					break
				}
			}
			for _, k := range []string{"trace_id", "traceId", "request_id"} {
				if v, ok := raw[k]; ok {
					ev.TraceID = fmt.Sprint(v)
					break
				}
			}
			if ev.Message != "" {
				return
			}
		}
	}
	ev.Message = line
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
