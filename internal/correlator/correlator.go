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
func (c *Correlator) Run(ctx context.Context) <-chan Result {
	out := make(chan Result, 256)

	go func() {
		defer close(out)
		c.merge(ctx, out)
	}()

	return out
}

// merge implements a k-way merge using a min-heap.
// Each heap entry holds the next unprocessed event from one source.
func (c *Correlator) merge(ctx context.Context, out chan<- Result) {
	h := &eventHeap{}
	heap.Init(h)

	// Seed: pull the first event from each source.
	active := make([]namedSource, 0, len(c.sources))
	for _, src := range c.sources {
		src := src
		if ev, ok := <-src.ch; ok {
			heap.Push(h, heapItem{event: ev, srcIdx: len(active)})
			active = append(active, src)
		}
	}

	// Signal detectors.
	baseline := newBaselineTracker(
		time.Duration(c.cfg.Signal.BaselineWindowMinutes)*time.Minute,
		c.cfg.Signal.AnomalyThresholdMultiplier,
	)
	cascade := newCascadeTracker(
		time.Duration(c.cfg.Correlation.WindowMs) * time.Millisecond,
	)

	for h.Len() > 0 {
		select {
		case <-ctx.Done():
			return
		default:
		}

		item := heap.Pop(h).(heapItem)
		ev := item.event
		srcIdx := item.srcIdx

		// Refill from the same source.
		if next, ok := <-active[srcIdx].ch; ok {
			heap.Push(h, heapItem{event: next, srcIdx: srcIdx})
		}

		result := Result{Event: ev}

		if ev.Severity >= adapter.SeverityError {
			// Anomaly rate detection.
			if isAnomaly, _ := baseline.record(ev.Service, ev.Timestamp); isAnomaly {
				result.IsSignal = true
				result.SignalType = "anomaly"
			}

			// Cascade detection.
			if c.cfg.Signal.CascadeDetection {
				if cr := cascade.record(ev); cr.CascadeFrom != "" {
					result.IsSignal = true
					result.SignalType = "cascade"
					result.CascadeFrom = cr.CascadeFrom
					result.NewCascade = cr.IsNew
				}
			}
		} else {
			// Still pass non-error events through the cascade tracker
			// so it can expire stale bursts correctly.
			cascade.record(ev)
		}

		select {
		case out <- result:
		case <-ctx.Done():
			return
		}
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
