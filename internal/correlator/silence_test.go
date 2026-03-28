package correlator

import (
	"testing"
	"time"
)

var silenceBase = time.Date(2024, 1, 15, 14, 0, 0, 0, time.UTC)

// newTestTracker creates a silence tracker with a 4-minute baseline window
// and 90% drop threshold. Bootstrap ends at silenceBase + 4 minutes.
func newTestTracker() *silenceTracker {
	return newSilenceTracker(4*time.Minute, 90.0, silenceBase)
}

// TestSilenceBootstrapping verifies detection is disabled during bootstrap.
func TestSilenceBootstrapping(t *testing.T) {
	s := newTestTracker()

	// Pump events for a service during the bootstrap window.
	for i := 0; i < 20; i++ {
		s.Record("svc-a", silenceBase.Add(time.Duration(i)*10*time.Second))
	}

	// Still within bootstrap — Check should return nothing.
	midBootstrap := silenceBase.Add(2 * time.Minute)
	signals := s.Check(midBootstrap)
	if len(signals) != 0 {
		t.Errorf("expected no signals during bootstrap, got %d", len(signals))
	}

	if !s.Bootstrapping(midBootstrap) {
		t.Error("expected Bootstrapping() to be true mid-window")
	}
}

// TestSilenceDetected verifies a service going quiet is flagged after bootstrap.
func TestSilenceDetected(t *testing.T) {
	s := newTestTracker()

	// Establish baseline: steady events in the older half (0–2 min).
	for i := 0; i < 24; i++ {
		s.Record("svc-a", silenceBase.Add(time.Duration(i)*5*time.Second))
	}

	// Service goes completely silent after 2 minutes.
	// Advance time to after bootstrap (4 min) — no new events from svc-a.
	checkTime := silenceBase.Add(5 * time.Minute)
	signals := s.Check(checkTime)

	found := false
	for _, sig := range signals {
		if sig.Service == "svc-a" {
			found = true
			if sig.DropPct < 90 {
				t.Errorf("expected drop >= 90%%, got %.1f%%", sig.DropPct)
			}
		}
	}
	if !found {
		t.Error("expected silence signal for svc-a, got none")
	}
}

// TestSilenceNotFiredWhenActive verifies no signal for a healthy service.
func TestSilenceNotFiredWhenActive(t *testing.T) {
	s := newTestTracker()

	// Steady events throughout the entire window.
	for i := 0; i < 48; i++ {
		s.Record("svc-a", silenceBase.Add(time.Duration(i)*5*time.Second))
	}

	checkTime := silenceBase.Add(5 * time.Minute)
	signals := s.Check(checkTime)
	for _, sig := range signals {
		if sig.Service == "svc-a" {
			t.Errorf("healthy service should not be flagged, got drop %.1f%%", sig.DropPct)
		}
	}
}

// TestSilenceDeduplication verifies a service is only flagged once per silence event.
func TestSilenceDeduplication(t *testing.T) {
	s := newTestTracker()

	// Establish baseline in older half.
	for i := 0; i < 24; i++ {
		s.Record("svc-a", silenceBase.Add(time.Duration(i)*5*time.Second))
	}

	checkTime := silenceBase.Add(5 * time.Minute)

	first := s.Check(checkTime)
	second := s.Check(checkTime.Add(10 * time.Second))

	if len(first) == 0 {
		t.Fatal("expected first signal for svc-a")
	}
	for _, sig := range second {
		if sig.Service == "svc-a" {
			t.Error("silence should only be reported once until service recovers")
		}
	}
}

// TestSilenceRecovery verifies that a recovered service can be flagged again.
func TestSilenceRecovery(t *testing.T) {
	s := newTestTracker()

	// Baseline in older half.
	for i := 0; i < 24; i++ {
		s.Record("svc-a", silenceBase.Add(time.Duration(i)*5*time.Second))
	}

	// First silence event.
	s.Check(silenceBase.Add(5 * time.Minute))

	// Service recovers — record new events.
	recoveryTime := silenceBase.Add(6 * time.Minute)
	s.Record("svc-a", recoveryTime)

	// Silence flag should be cleared after a Record() call.
	if s.silenced["svc-a"] {
		t.Error("silence flag should clear when service produces events again")
	}
}

// TestSilenceInsufficientBaseline verifies no signal when baseline is too thin.
func TestSilenceInsufficientBaseline(t *testing.T) {
	s := newTestTracker()

	// Only 3 events — below the minimum of 5.
	for i := 0; i < 3; i++ {
		s.Record("svc-a", silenceBase.Add(time.Duration(i)*30*time.Second))
	}

	checkTime := silenceBase.Add(5 * time.Minute)
	signals := s.Check(checkTime)
	for _, sig := range signals {
		if sig.Service == "svc-a" {
			t.Error("should not flag silence with insufficient baseline history")
		}
	}
}

// TestSilenceMultipleServices verifies independent tracking per service.
func TestSilenceMultipleServices(t *testing.T) {
	s := newTestTracker()

	// Both services active in the older half.
	for i := 0; i < 24; i++ {
		ts := silenceBase.Add(time.Duration(i) * 5 * time.Second)
		s.Record("svc-a", ts)
		s.Record("svc-b", ts)
	}

	// svc-b stays active into the recent half; svc-a goes silent.
	for i := 0; i < 24; i++ {
		s.Record("svc-b", silenceBase.Add(2*time.Minute+time.Duration(i)*5*time.Second))
	}

	checkTime := silenceBase.Add(5 * time.Minute)
	signals := s.Check(checkTime)

	silentServices := map[string]bool{}
	for _, sig := range signals {
		silentServices[sig.Service] = true
	}

	if !silentServices["svc-a"] {
		t.Error("svc-a should be flagged as silent")
	}
	if silentServices["svc-b"] {
		t.Error("svc-b should not be flagged — it stayed active")
	}
}
