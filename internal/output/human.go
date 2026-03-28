package output

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/mmediasoftwarelab/siftlog/internal/adapter"
	"github.com/mmediasoftwarelab/siftlog/internal/config"
	"github.com/mmediasoftwarelab/siftlog/internal/correlator"
)

var (
	colorError  = color.New(color.FgRed, color.Bold)
	colorWarn   = color.New(color.FgYellow)
	colorInfo   = color.New(color.FgCyan)
	colorDebug  = color.New(color.FgWhite)
	colorSignal = color.New(color.FgMagenta, color.Bold)
	colorMuted  = color.New(color.FgHiBlack)
	colorBold   = color.New(color.Bold)
)

const separator = "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

// HumanWriter writes human-readable output to w.
type HumanWriter struct {
	w            io.Writer
	cfg          *config.Config
	quiet        bool
	verbose      bool
	start        time.Time
	eventCount   int
	signalCount  int
	noiseCount   int
	// cascadeChain tracks the current cascade sequence for display.
	// Each entry is a service name in propagation order.
	cascadeChain []string
}

func NewHuman(w io.Writer, cfg *config.Config, quiet, verbose bool) *HumanWriter {
	return &HumanWriter{
		w:       w,
		cfg:     cfg,
		quiet:   quiet,
		verbose: verbose,
		start:   time.Now(),
	}
}

// Header prints the session header.
func (h *HumanWriter) Header(sourceCount int) {
	fmt.Fprintln(h.w)
	colorBold.Fprintf(h.w, "SIFTLOG v0.0.1")
	fmt.Fprintf(h.w, "  |  sources: %d  |  window: %dms  |  anchor: %s\n",
		sourceCount,
		h.cfg.Correlation.WindowMs,
		h.cfg.Correlation.Anchor,
	)
	fmt.Fprintln(h.w, separator)
}

// Write outputs a single correlated result.
func (h *HumanWriter) Write(r correlator.Result) {
	h.eventCount++

	if r.IsSignal {
		h.signalCount++

		if r.SignalType == "cascade" {
			h.updateCascadeChain(r)
		} else {
			colorSignal.Fprintf(h.w, "[signal:%s]\n", r.SignalType)
		}
	} else {
		h.noiseCount++
		if h.quiet {
			return
		}
	}

	ev := r.Event
	h.writeEvent(ev)

	if ev.DriftWarning != "" {
		colorMuted.Fprintf(h.w, "  [drift warning] %s\n", ev.DriftWarning)
	}
}

// updateCascadeChain maintains and prints the cascade chain header.
// On the first detection of a new cascade link, it prints the updated chain.
// Subsequent events from the same cascade are printed without a header.
func (h *HumanWriter) updateCascadeChain(r correlator.Result) {
	svc := r.Event.Service
	from := r.CascadeFrom

	if r.NewCascade {
		// Extend or start the chain.
		if len(h.cascadeChain) == 0 {
			h.cascadeChain = []string{from, svc}
		} else if h.cascadeChain[len(h.cascadeChain)-1] == from {
			// Extending an existing chain.
			h.cascadeChain = append(h.cascadeChain, svc)
		} else {
			// New cascade, different origin — start fresh.
			h.cascadeChain = []string{from, svc}
		}
		colorSignal.Fprintf(h.w, "[signal:cascade] %s\n", strings.Join(h.cascadeChain, " → "))
	}
}

func (h *HumanWriter) writeEvent(ev adapter.Event) {
	ts := h.formatTimestamp(ev.Timestamp)
	svc := h.formatService(ev.Service, ev.Source)
	sev, sevColor := h.formatSeverity(ev.Severity)

	sevColor.Fprintf(h.w, "  %s  %-22s %s  %s\n", ts, svc, sev, ev.Message)

	if h.verbose && len(ev.Fields) > 0 {
		colorMuted.Fprintf(h.w, "         fields: %s\n", formatFields(ev.Fields))
	}
}

// Footer prints the session summary.
func (h *HumanWriter) Footer() {
	elapsed := time.Since(h.start)
	fmt.Fprintln(h.w, separator)
	colorMuted.Fprintf(h.w, "noise suppressed: %s  |  signal: %d events  |  elapsed: %s\n\n",
		formatCount(h.noiseCount),
		h.signalCount,
		formatDuration(elapsed),
	)
}

func (h *HumanWriter) formatTimestamp(t time.Time) string {
	switch h.cfg.Output.Timestamps {
	case "absolute":
		return t.Format("2006-01-02T15:04:05.000")
	case "unix":
		return fmt.Sprintf("%d", t.UnixMilli())
	default: // relative
		delta := time.Since(t)
		if delta < 0 {
			return t.Format("15:04:05.000")
		}
		return t.Format("15:04:05.000")
	}
}

func (h *HumanWriter) formatService(service, source string) string {
	if !h.cfg.Output.ShowServiceName {
		return source
	}
	if service != "" {
		return service
	}
	return source
}

func (h *HumanWriter) formatSeverity(s adapter.Severity) (string, *color.Color) {
	label := fmt.Sprintf("%-5s", s.String())
	switch s {
	case adapter.SeverityError, adapter.SeverityFatal:
		return label, colorError
	case adapter.SeverityWarn:
		return label, colorWarn
	case adapter.SeverityInfo:
		return label, colorInfo
	default:
		return label, colorDebug
	}
}

func formatFields(fields map[string]string) string {
	parts := make([]string, 0, len(fields))
	for k, v := range fields {
		parts = append(parts, fmt.Sprintf("%s=%q", k, v))
	}
	return strings.Join(parts, " ")
}

func formatCount(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%d,%03d", n/1000, n%1000)
	}
	return fmt.Sprintf("%d", n)
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}
