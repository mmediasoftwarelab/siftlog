package file

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mmediasoftwarelab/siftlog/internal/adapter"
	"github.com/mmediasoftwarelab/siftlog/internal/config"
)

// Adapter reads log events from a file or stdin.
// It handles two formats:
//   - JSON: one JSON object per line (structured logs)
//   - Plain text: best-effort timestamp + severity extraction
type Adapter struct {
	cfg config.SourceConfig
}

func New(cfg config.SourceConfig) *Adapter {
	return &Adapter{cfg: cfg}
}

func (a *Adapter) Name() string {
	return a.cfg.Name
}

func (a *Adapter) Fetch(ctx context.Context, since, until time.Time) (<-chan adapter.Event, error) {
	var r io.ReadCloser

	if a.cfg.Path == "" || a.cfg.Path == "-" {
		r = io.NopCloser(os.Stdin)
	} else {
		f, err := os.Open(a.cfg.Path)
		if err != nil {
			return nil, fmt.Errorf("file adapter %q: %w", a.cfg.Name, err)
		}
		r = f
	}

	ch := make(chan adapter.Event, 256)

	go func() {
		defer close(ch)
		defer r.Close()

		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB line buffer

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}

			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			event := parseLine(line, a.cfg)

			// Apply per-source timestamp offset.
			if a.cfg.TimestampOffsetMs != 0 {
				event.RawTimestamp = event.Timestamp
				event.Timestamp = event.Timestamp.Add(
					time.Duration(a.cfg.TimestampOffsetMs) * time.Millisecond,
				)
			}

			// Filter by time range if specified.
			if !since.IsZero() && event.Timestamp.Before(since) {
				continue
			}
			if !until.IsZero() && event.Timestamp.After(until) {
				continue
			}

			select {
			case ch <- event:
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch, nil
}

// parseLine attempts JSON first, then falls back to plain text heuristics.
func parseLine(line string, cfg config.SourceConfig) adapter.Event {
	event := adapter.Event{
		Source:    cfg.Name,
		Timestamp: time.Now(), // fallback if no timestamp found
		Fields:    make(map[string]string),
	}

	if strings.HasPrefix(line, "{") {
		if parseJSON(line, &event) {
			return event
		}
	}

	parsePlainText(line, &event)
	return event
}

// parseJSON handles structured JSON log lines.
// Recognizes common field name conventions across logging frameworks.
func parseJSON(line string, event *adapter.Event) bool {
	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return false
	}

	// Timestamp — try common field names in order of preference.
	for _, key := range []string{"timestamp", "time", "ts", "@timestamp", "logged_at"} {
		if v, ok := raw[key]; ok {
			if t, err := parseTimestamp(fmt.Sprint(v)); err == nil {
				event.Timestamp = t
				delete(raw, key)
				break
			}
		}
	}

	// Severity.
	for _, key := range []string{"level", "severity", "lvl", "log_level"} {
		if v, ok := raw[key]; ok {
			event.Severity = adapter.ParseSeverity(fmt.Sprint(v))
			delete(raw, key)
			break
		}
	}

	// Message.
	for _, key := range []string{"message", "msg", "log", "text"} {
		if v, ok := raw[key]; ok {
			event.Message = fmt.Sprint(v)
			delete(raw, key)
			break
		}
	}

	// Service name.
	for _, key := range []string{"service", "service_name", "app", "application", "component"} {
		if v, ok := raw[key]; ok {
			event.Service = fmt.Sprint(v)
			delete(raw, key)
			break
		}
	}

	// Trace ID.
	for _, key := range []string{"trace_id", "traceId", "trace", "x-trace-id", "request_id", "requestId"} {
		if v, ok := raw[key]; ok {
			event.TraceID = fmt.Sprint(v)
			delete(raw, key)
			break
		}
	}

	// Everything else goes into Fields.
	for k, v := range raw {
		event.Fields[k] = fmt.Sprint(v)
	}

	return true
}

// parsePlainText handles unstructured log lines with heuristic extraction.
// Common formats: "2024-01-01T14:22:03Z [ERROR] service: message"
func parsePlainText(line string, event *adapter.Event) {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		event.Message = line
		return
	}

	idx := 0

	// Try to parse leading timestamp (one or two tokens — date + time).
	if t, err := parseTimestamp(parts[0]); err == nil {
		event.Timestamp = t
		idx = 1
	} else if len(parts) > 1 {
		combined := parts[0] + "T" + parts[1]
		if t, err := parseTimestamp(combined); err == nil {
			event.Timestamp = t
			idx = 2
		}
	}

	// Try to find severity token.
	for i := idx; i < len(parts) && i < idx+3; i++ {
		clean := strings.Trim(parts[i], "[]()<>")
		if sev := adapter.ParseSeverity(clean); sev != adapter.SeverityUnknown {
			event.Severity = sev
			idx = i + 1
			break
		}
	}

	event.Message = strings.Join(parts[idx:], " ")
}

var timestampFormats = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006/01/02 15:04:05",
	"02/Jan/2006:15:04:05 -0700",
}

func parseTimestamp(s string) (time.Time, error) {
	// Unix epoch (seconds or milliseconds).
	var epoch float64
	if _, err := fmt.Sscanf(s, "%f", &epoch); err == nil && epoch > 1e9 {
		if epoch > 1e12 {
			return time.UnixMilli(int64(epoch)), nil
		}
		return time.Unix(int64(epoch), 0), nil
	}

	for _, layout := range timestampFormats {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}

	return time.Time{}, fmt.Errorf("unrecognized timestamp: %q", s)
}
