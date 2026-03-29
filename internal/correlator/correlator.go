package correlator

import (
	"container/heap"
	"context"
	"time"

	"github.com/mmediasoftwarelab/siftlog/internal/adapter"
	"github.com/mmediasoftwarelab/siftlog/internal/config"
)

// Result is a correlated event, annotated with signal information.
type Result struct {
	Event        adapter.Event
	IsSignal     bool
	SignalType   string // "anomaly", "cascade", "silence"
	CascadeFrom  string // set when this event's service is failing due to another
	NewCascade   bool   // true only on first detection of this cascade relationship

	// SilenceSignals carries any services detected as unexpectedly quiet
	// at the time this event was processed. Unrelated to the event itself.
	SilenceSignals []SilenceSignal
}

// Correlator merges N event streams in timestamp order and feeds them
// through signal detection. It uses a min-heap over the heads of each
// source channel so the output is globally time-ordered.
type Correlator struct {
	cfg     *config.Config
	sources []namedSource
}

type namedSource struct {
	name string
	ch   <-chan adapter.Event
}

func New(cfg *config.Config) *Correlator {
	return &Correlator{cfg: cfg}
}

// AddSource registers an event channel from a named adapter.
func (c *Correlator) AddSource(name string, ch <-chan adapter.Event) {
	c.sources = append(c.sources, namedSource{name: name, ch: ch})
}

// Run merges all sources and sends correlated Results to the returned channel.
// The channel is closed when all sources are exhausted or ctx is cancelled.
//
// In live mode (cfg.Live.FlushMs > 0) a flush ticker ensures the oldest
// buffered event is emitted after at most FlushMs milliseconds, preventing
// a quiet source from stalling output from busy ones.
func (c *Correlator) Run(ctx context.Context) <-chan Result {
	out := make(chan Result, 256)

	go func() {
		defer close(out)
		c.merge(ctx, out)
	}()

	return out
}

// merge sets up signal detectors and dispatches to the appropriate merge strategy.
func (c *Correlator) merge(ctx context.Context, out chan<- Result) {
	h := &eventHeap{}
	heap.Init(h)

	baselineWindow := time.Duration(c.cfg.Signal.BaselineWindowMinutes) * time.Minute
	baseline := newBaselineTracker(baselineWindow, c.cfg.Signal.AnomalyThresholdMultiplier)
	cascade := newCascadeTracker(time.Duration(c.cfg.Correlation.WindowMs) * time.Millisecond)
	var silence *silenceTracker
	if c.cfg.Signal.SilenceDetection {
		silence = newSilenceTracker(baselineWindow, c.cfg.Signal.SilenceThresholdPct, time.Now())
	}

	emit := func(ev adapter.Event) {
		result := Result{Event: ev}
		if silence != nil {
			silence.Record(ev.Service, ev.Timestamp)
			if signals := silence.Check(ev.Timestamp); len(signals) > 0 {
				result.SilenceSignals = signals
			}
		}
		if ev.Severity >= adapter.SeverityError {
			if isAnomaly, _ := baseline.record(ev.Service, ev.Timestamp); isAnomaly {
				result.IsSignal = true
				result.SignalType = "anomaly"
			}
			if c.cfg.Signal.CascadeDetection {
				if cr := cascade.record(ev); cr.CascadeFrom != "" {
					result.IsSignal = true
					result.SignalType = "cascade"
					result.CascadeFrom = cr.CascadeFrom
					result.NewCascade = cr.IsNew
				}
			}
		} else {
			cascade.record(ev)
		}
		select {
		case out <- result:
		case <-ctx.Done():
		}
	}

	if c.cfg.Live.FlushMs > 0 {
		c.mergeLive(ctx, h, emit)
	} else {
		c.mergeHistorical(ctx, h, emit)
	}
}

// mergeHistorical is the original synchronous k-way merge.
// Each source is refilled inline after its event is popped.
func (c *Correlator) mergeHistorical(ctx context.Context, h *eventHeap, emit func(adapter.Event)) {
	active := make([]namedSource, 0, len(c.sources))
	for _, src := range c.sources {
		src := src
		if ev, ok := <-src.ch; ok {
			heap.Push(h, heapItem{event: ev, srcIdx: len(active)})
			active = append(active, src)
		}
	}

	for h.Len() > 0 {
		select {
		case <-ctx.Done():
			return
		default:
		}
		item := heap.Pop(h).(heapItem)
		if next, ok := <-active[item.srcIdx].ch; ok {
			heap.Push(h, heapItem{event: next, srcIdx: item.srcIdx})
		}
		emit(item.event)
	}
}

// mergeLive fans all source channels into a single incoming channel and uses
// a flush ticker to emit the oldest buffered event when sources are quiet.
func (c *Correlator) mergeLive(ctx context.Context, h *eventHeap, emit func(adapter.Event)) {
	type doneMsg struct{ name string }
	incoming := make(chan adapter.Event, 256)
	done := make(chan doneMsg, len(c.sources))

	for _, src := range c.sources {
		src := src
		go func() {
			for ev := range src.ch {
				select {
				case incoming <- ev:
				case <-ctx.Done():
					return
				}
			}
			done <- doneMsg{src.name}
		}()
	}

	flushAge := time.Duration(c.cfg.Live.FlushMs) * time.Millisecond
	ticker := time.NewTicker(flushAge)
	defer ticker.Stop()

	// flushReady emits all heap events older than flushAge.
	flushReady := func() {
		horizon := time.Now().Add(-flushAge)
		for h.Len() > 0 {
			top := (*h)[0]
			if top.event.Timestamp.After(horizon) {
				break
			}
			item := heap.Pop(h).(heapItem)
			emit(item.event)
		}
	}

	remaining := len(c.sources)
	for remaining > 0 || h.Len() > 0 {
		select {
		case <-ctx.Done():
			return

		case ev := <-incoming:
			heap.Push(h, heapItem{event: ev})
			flushReady()

		case <-done:
			remaining--

		case <-ticker.C:
			flushReady()
		}
	}

	// All sources done — drain any remaining buffered events.
	for h.Len() > 0 {
		item := heap.Pop(h).(heapItem)
		emit(item.event)
	}
}

// heapItem is one entry in the merge heap.
type heapItem struct {
	event  adapter.Event
	srcIdx int
}

type eventHeap []heapItem

func (h eventHeap) Len() int            { return len(h) }
func (h eventHeap) Less(i, j int) bool  { return h[i].event.Timestamp.Before(h[j].event.Timestamp) }
func (h eventHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *eventHeap) Push(x any)         { *h = append(*h, x.(heapItem)) }
func (h *eventHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// baselineTracker maintains per-service error rates for anomaly detection.
type baselineTracker struct {
	window     time.Duration
	multiplier float64
	buckets    map[string][]time.Time // service -> recent error timestamps
}

func newBaselineTracker(window time.Duration, multiplier float64) *baselineTracker {
	return &baselineTracker{
		window:     window,
		multiplier: multiplier,
		buckets:    make(map[string][]time.Time),
	}
}

// record adds an error event for a service and returns whether the current
// rate exceeds the baseline by the configured multiplier.
func (b *baselineTracker) record(service string, t time.Time) (bool, float64) {
	cutoff := t.Add(-b.window)

	// Evict old entries.
	existing := b.buckets[service]
	pruned := existing[:0]
	for _, ts := range existing {
		if ts.After(cutoff) {
			pruned = append(pruned, ts)
		}
	}
	pruned = append(pruned, t)
	b.buckets[service] = pruned

	count := len(pruned)
	if count < 10 {
		// Not enough history to establish a baseline.
		return false, 0
	}

	halfMinutes := b.window.Minutes() / 2
	halfCutoff := t.Add(-b.window / 2)

	// Split events into the older half (baseline) and the recent half (current).
	olderCount := 0
	recentCount := 0
	for _, ts := range pruned {
		if ts.Before(halfCutoff) {
			olderCount++
		} else {
			recentCount++
		}
	}

	if olderCount < 5 {
		// Not enough baseline history in the older half.
		return false, float64(count) / b.window.Minutes()
	}

	baselineRate := float64(olderCount) / halfMinutes
	recentRate := float64(recentCount) / halfMinutes

	return recentRate >= baselineRate*b.multiplier, recentRate
}
