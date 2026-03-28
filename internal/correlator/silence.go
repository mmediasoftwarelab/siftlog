package correlator

import (
	"time"
)

// silenceTracker detects when a service drops significantly below its
// expected log volume. It maintains a rolling per-service event count
// baseline and flags services that fall below a configured threshold.
//
// Bootstrap period: the baseline requires a minimum observation window
// before silence detection is active. During bootstrap, IsSilent always
// returns false. The caller can check Bootstrapping() to surface this
// in the output header.
type silenceTracker struct {
	baselineWindow time.Duration
	thresholdPct   float64 // e.g. 90 means flag if volume drops >90%

	// events: service -> timestamps of all events (any severity) in the window.
	events map[string][]time.Time

	// bootstrapUntil is the time after which baseline detection is active.
	bootstrapUntil time.Time

	// silenced: services currently flagged as silent (to avoid repeat signals).
	silenced map[string]bool
}

// SilenceSignal is returned when a service is detected as unexpectedly quiet.
type SilenceSignal struct {
	Service     string
	DropPct     float64 // how far below baseline (e.g. 95.0 = 95% drop)
	BaselineRate float64 // events/minute during baseline
	CurrentRate  float64 // events/minute in the recent half
}

func newSilenceTracker(baselineWindow time.Duration, thresholdPct float64, now time.Time) *silenceTracker {
	return &silenceTracker{
		baselineWindow: baselineWindow,
		thresholdPct:   thresholdPct,
		bootstrapUntil: now.Add(baselineWindow),
		events:         make(map[string][]time.Time),
		silenced:       make(map[string]bool),
	}
}

// Bootstrapping returns true while the baseline window hasn't been fully observed.
func (s *silenceTracker) Bootstrapping(now time.Time) bool {
	return now.Before(s.bootstrapUntil)
}

// Record registers any event (any severity) for a service at time t.
// Returns a SilenceSignal if the service has gone unexpectedly quiet,
// or nil if no silence is detected.
func (s *silenceTracker) Record(service string, t time.Time) *SilenceSignal {
	if service == "" {
		return nil
	}

	cutoff := t.Add(-s.baselineWindow)

	// Evict old events outside the window.
	existing := s.events[service]
	pruned := existing[:0]
	for _, ts := range existing {
		if ts.After(cutoff) {
			pruned = append(pruned, ts)
		}
	}
	pruned = append(pruned, t)
	s.events[service] = pruned

	// Service is active — clear any silence flag.
	delete(s.silenced, service)

	return nil
}

// Check scans all tracked services at time t and returns signals for any
// that have gone silent since we last saw them. Call this periodically
// (e.g. on each event) to surface services that have stopped logging.
func (s *silenceTracker) Check(t time.Time) []SilenceSignal {
	if s.Bootstrapping(t) {
		return nil
	}

	var signals []SilenceSignal
	halfWindow := s.baselineWindow / 2
	halfCutoff := t.Add(-halfWindow)
	cutoff := t.Add(-s.baselineWindow)

	for service, timestamps := range s.events {
		// Count events in the older half (baseline) vs recent half.
		olderCount := 0
		recentCount := 0
		for _, ts := range timestamps {
			if ts.Before(cutoff) {
				continue // outside window entirely
			}
			if ts.Before(halfCutoff) {
				olderCount++
			} else {
				recentCount++
			}
		}

		if olderCount < 5 {
			// Not enough baseline history for this service.
			continue
		}

		halfMinutes := halfWindow.Minutes()
		baselineRate := float64(olderCount) / halfMinutes
		currentRate := float64(recentCount) / halfMinutes

		dropPct := (1 - currentRate/baselineRate) * 100
		if dropPct >= s.thresholdPct && !s.silenced[service] {
			s.silenced[service] = true
			signals = append(signals, SilenceSignal{
				Service:      service,
				DropPct:      dropPct,
				BaselineRate: baselineRate,
				CurrentRate:  currentRate,
			})
		}
	}

	return signals
}
