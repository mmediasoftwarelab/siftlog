package adapter

import (
	"context"
	"time"
)

// Severity levels, normalized across all source formats.
type Severity int

const (
	SeverityUnknown Severity = iota
	SeverityTrace
	SeverityDebug
	SeverityInfo
	SeverityWarn
	SeverityError
	SeverityFatal
)

func (s Severity) String() string {
	switch s {
	case SeverityTrace:
		return "TRACE"
	case SeverityDebug:
		return "DEBUG"
	case SeverityInfo:
		return "INFO"
	case SeverityWarn:
		return "WARN"
	case SeverityError:
		return "ERROR"
	case SeverityFatal:
		return "FATAL"
	default:
		return "UNKNOWN"
	}
}

func ParseSeverity(s string) Severity {
	switch s {
	case "TRACE", "trace":
		return SeverityTrace
	case "DEBUG", "debug":
		return SeverityDebug
	case "INFO", "info", "INFORMATION", "information":
		return SeverityInfo
	case "WARN", "warn", "WARNING", "warning":
		return SeverityWarn
	case "ERROR", "error", "ERR", "err":
		return SeverityError
	case "FATAL", "fatal", "CRITICAL", "critical":
		return SeverityFatal
	default:
		return SeverityUnknown
	}
}

// Event is the normalized representation of a log event from any source.
// All adapters produce Events; the correlation engine consumes them.
type Event struct {
	// Timestamp is the event time after applying any source offset correction.
	Timestamp time.Time

	// RawTimestamp is the original timestamp from the source, before offset correction.
	RawTimestamp time.Time

	// Source is the configured source name (e.g. "production-loki").
	Source string

	// Service is the originating service name, extracted from the log event.
	Service string

	// Severity is the normalized log level.
	Severity Severity

	// Message is the primary log line.
	Message string

	// TraceID is the distributed trace identifier, if present.
	TraceID string

	// Fields holds any additional structured fields from the log event.
	Fields map[string]string

	// DriftWarning is set when the event timestamp was adjusted due to clock drift detection.
	DriftWarning string
}

// Adapter is the interface all log source adapters implement.
// Fetch returns a channel of Events for the given time range.
// The channel is closed when all events have been delivered or ctx is cancelled.
type Adapter interface {
	// Name returns the configured source name.
	Name() string

	// Fetch begins streaming events for the given time range.
	// since and until are zero-value for live/unbounded queries.
	Fetch(ctx context.Context, since, until time.Time) (<-chan Event, error)
}
