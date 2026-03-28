package correlator

import (
	"time"

	"github.com/mmediasoftwarelab/siftlog/internal/adapter"
)

// cascadeTracker detects when one service's failures cause another service
// to begin failing, using two strategies in priority order:
//
//  1. Trace ID correlation: two services share a trace ID on error events.
//     The service with the earlier first-error on that trace is the origin.
//
//  2. Temporal correlation: service B enters an error state within the
//     correlation window after service A, with no trace ID available.
//     Lower confidence — flagged separately in future.
type cascadeTracker struct {
	window time.Duration

	// traceErrors: traceID -> service -> time of first error on that trace.
	traceErrors map[string]map[string]time.Time

	// errorOnset: service -> time it first entered its current error burst.
	// Reset when a service goes quiet for longer than the window.
	errorOnset map[string]time.Time

	// lastError: service -> most recent error time (used for burst expiry).
	lastError map[string]time.Time

	// knownCascades: "origin->dependent" pairs we've already reported,
	// so we only emit the cascade signal once per relationship.
	knownCascades map[string]bool
}

func newCascadeTracker(window time.Duration) *cascadeTracker {
	return &cascadeTracker{
		window:        window,
		traceErrors:   make(map[string]map[string]time.Time),
		errorOnset:    make(map[string]time.Time),
		lastError:     make(map[string]time.Time),
		knownCascades: make(map[string]bool),
	}
}

// cascadeResult is returned by record for each event.
type cascadeResult struct {
	// CascadeFrom is set when this event's service is failing because of another service.
	CascadeFrom string

	// IsNew is true only on the first detection of this cascade relationship —
	// used to trigger the chain header in the output formatter.
	IsNew bool
}

// record processes one event and returns cascade information if detected.
func (c *cascadeTracker) record(ev adapter.Event) cascadeResult {
	if ev.Service == "" {
		return cascadeResult{}
	}

	// Expire stale error bursts: if a service hasn't errored in > window, reset it.
	if last, ok := c.lastError[ev.Service]; ok {
		if ev.Timestamp.Sub(last) > c.window {
			delete(c.errorOnset, ev.Service)
			delete(c.lastError, ev.Service)
		}
	}

	if ev.Severity < adapter.SeverityError {
		return cascadeResult{}
	}

	// Track error onset for this service.
	if _, bursting := c.errorOnset[ev.Service]; !bursting {
		c.errorOnset[ev.Service] = ev.Timestamp
	}
	c.lastError[ev.Service] = ev.Timestamp

	// Strategy 1: trace ID correlation.
	if ev.TraceID != "" {
		if c.traceErrors[ev.TraceID] == nil {
			c.traceErrors[ev.TraceID] = make(map[string]time.Time)
		}
		// Record first error time for this service on this trace.
		if _, seen := c.traceErrors[ev.TraceID][ev.Service]; !seen {
			c.traceErrors[ev.TraceID][ev.Service] = ev.Timestamp
		}

		// Find the earliest-erroring service on this trace that isn't us.
		origin, originTime := c.earliestOnTrace(ev.TraceID, ev.Service)
		if origin != "" {
			myOnset := c.errorOnset[ev.Service]
			delta := myOnset.Sub(originTime)
			if delta >= 0 && delta <= c.window {
				return c.markCascade(origin, ev.Service)
			}
		}
	}

	// Strategy 2: temporal correlation (no trace ID).
	// Only fire if the service has no trace IDs at all on current errors.
	if ev.TraceID == "" {
		myOnset := c.errorOnset[ev.Service]
		origin := c.earliestOnsetBefore(ev.Service, myOnset)
		if origin != "" {
			return c.markCascade(origin, ev.Service)
		}
	}

	return cascadeResult{}
}

// earliestOnTrace returns the service (and its first-error time) that errored
// first on a given trace, excluding the named service.
func (c *cascadeTracker) earliestOnTrace(traceID, excludeService string) (string, time.Time) {
	var earliest string
	var earliestTime time.Time
	for svc, t := range c.traceErrors[traceID] {
		if svc == excludeService {
			continue
		}
		if earliestTime.IsZero() || t.Before(earliestTime) {
			earliestTime = t
			earliest = svc
		}
	}
	return earliest, earliestTime
}

// earliestOnsetBefore returns the service whose error burst started before
// myOnset and within the correlation window, excluding the named service.
func (c *cascadeTracker) earliestOnsetBefore(excludeService string, myOnset time.Time) string {
	var earliest string
	var earliestOnset time.Time
	for svc, onset := range c.errorOnset {
		if svc == excludeService {
			continue
		}
		delta := myOnset.Sub(onset)
		if delta < 0 || delta > c.window {
			continue
		}
		if earliestOnset.IsZero() || onset.Before(earliestOnset) {
			earliestOnset = onset
			earliest = svc
		}
	}
	return earliest
}

// markCascade records a cascade relationship and returns whether it's newly detected.
func (c *cascadeTracker) markCascade(origin, dependent string) cascadeResult {
	key := origin + "->" + dependent
	isNew := !c.knownCascades[key]
	c.knownCascades[key] = true
	return cascadeResult{CascadeFrom: origin, IsNew: isNew}
}
