package output

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/mmediasoftwarelab/siftlog/internal/correlator"
)

// JSONWriter writes one JSON object per result to w, newline-delimited.
// This makes the output streamable and directly pipeable to jq.
//
// Schema per line:
//
//	{
//	  "timestamp":   "2024-01-15T14:22:03.441Z",
//	  "service":     "payment-service",
//	  "source":      "production-loki",
//	  "severity":    "ERROR",
//	  "message":     "connection pool exhausted",
//	  "trace_id":    "a3f9c2",
//	  "is_signal":   true,
//	  "signal_type": "cascade",
//	  "cascade_from": "payment-service",
//	  "fields":      { "pool_size": "20" },
//	  "silence":     [ { "service": "...", "drop_pct": 95.0, ... } ]
//	}
type JSONWriter struct {
	w           io.Writer
	enc         *json.Encoder
	start       time.Time
	signalCount int
	noiseCount  int
}

func NewJSON(w io.Writer) *JSONWriter {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return &JSONWriter{w: w, enc: enc, start: time.Now()}
}

// jsonEvent is the wire format for a single result.
type jsonEvent struct {
	Timestamp   string            `json:"timestamp"`
	Service     string            `json:"service,omitempty"`
	Source      string            `json:"source,omitempty"`
	Severity    string            `json:"severity"`
	Message     string            `json:"message"`
	TraceID     string            `json:"trace_id,omitempty"`
	IsSignal    bool              `json:"is_signal"`
	SignalType  string            `json:"signal_type,omitempty"`
	CascadeFrom string            `json:"cascade_from,omitempty"`
	Fields      map[string]string `json:"fields,omitempty"`
	Silence     []jsonSilence     `json:"silence,omitempty"`
	DriftWarning string           `json:"drift_warning,omitempty"`
}

type jsonSilence struct {
	Service      string  `json:"service"`
	DropPct      float64 `json:"drop_pct"`
	BaselineRate float64 `json:"baseline_rate"`
	CurrentRate  float64 `json:"current_rate"`
}

func (j *JSONWriter) Header(_ int) {} // JSON output has no header line

func (j *JSONWriter) Write(r correlator.Result) {
	if r.IsSignal {
		j.signalCount++
	} else {
		j.noiseCount++
	}

	ev := r.Event
	out := jsonEvent{
		Timestamp:    ev.Timestamp.UTC().Format(time.RFC3339Nano),
		Service:      ev.Service,
		Source:       ev.Source,
		Severity:     ev.Severity.String(),
		Message:      ev.Message,
		TraceID:      ev.TraceID,
		IsSignal:     r.IsSignal,
		SignalType:   r.SignalType,
		CascadeFrom:  r.CascadeFrom,
		DriftWarning: ev.DriftWarning,
	}
	if len(ev.Fields) > 0 {
		out.Fields = ev.Fields
	}
	for _, sig := range r.SilenceSignals {
		out.Silence = append(out.Silence, jsonSilence{
			Service:      sig.Service,
			DropPct:      sig.DropPct,
			BaselineRate: sig.BaselineRate,
			CurrentRate:  sig.CurrentRate,
		})
	}

	// Errors here are unrecoverable (e.g. broken pipe) — print to stderr.
	if err := j.enc.Encode(out); err != nil {
		fmt.Fprintf(j.w, `{"error":%q}`+"\n", err.Error())
	}
}

func (j *JSONWriter) Footer() {} // JSON output has no footer line
