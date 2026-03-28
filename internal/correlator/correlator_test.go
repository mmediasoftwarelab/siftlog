package correlator

import (
	"context"
	"testing"
	"time"

	"github.com/mmediasoftwarelab/siftlog/internal/adapter"
	"github.com/mmediasoftwarelab/siftlog/internal/config"
)

// makeEvent creates a test event at a given offset from a base time.
func makeEvent(service string, offsetMs int, severity adapter.Severity) adapter.Event {
	base := time.Date(2024, 1, 15, 14, 22, 0, 0, time.UTC)
	return adapter.Event{
		Service:   service,
		Severity:  severity,
		Timestamp: base.Add(time.Duration(offsetMs) * time.Millisecond),
		Message:   "test event",
	}
}

func chanOf(events ...adapter.Event) <-chan adapter.Event {
	ch := make(chan adapter.Event, len(events))
	for _, e := range events {
		ch <- e
	}
	close(ch)
	return ch
}

func defaultCfg() *config.Config {
	return &config.Config{
		Correlation: config.CorrelationConfig{
			Anchor:           "sender",
			WindowMs:         500,
			DriftToleranceMs: 200,
		},
		Signal: config.SignalConfig{
			AnomalyThresholdMultiplier: 10,
			BaselineWindowMinutes:      5,
		},
	}
}

// TestMergeOrder verifies that events from multiple sources come out in
// timestamp order regardless of the order sources were added.
func TestMergeOrder(t *testing.T) {
	cfg := defaultCfg()
	corr := New(cfg)

	// Source A: events at 0ms, 200ms, 400ms
	corr.AddSource("a", chanOf(
		makeEvent("svc-a", 0, adapter.SeverityInfo),
		makeEvent("svc-a", 200, adapter.SeverityInfo),
		makeEvent("svc-a", 400, adapter.SeverityInfo),
	))

	// Source B: events at 100ms, 300ms, 500ms — interleaved with A
	corr.AddSource("b", chanOf(
		makeEvent("svc-b", 100, adapter.SeverityInfo),
		makeEvent("svc-b", 300, adapter.SeverityInfo),
		makeEvent("svc-b", 500, adapter.SeverityInfo),
	))

	ctx := context.Background()
	results := corr.Run(ctx)

	var got []time.Time
	for r := range results {
		got = append(got, r.Event.Timestamp)
	}

	if len(got) != 6 {
		t.Fatalf("expected 6 events, got %d", len(got))
	}

	for i := 1; i < len(got); i++ {
		if got[i].Before(got[i-1]) {
			t.Errorf("events out of order at index %d: %v before %v", i, got[i], got[i-1])
		}
	}
}

// TestMergeSingleSource verifies basic operation with one source.
func TestMergeSingleSource(t *testing.T) {
	cfg := defaultCfg()
	corr := New(cfg)

	corr.AddSource("a", chanOf(
		makeEvent("svc-a", 0, adapter.SeverityInfo),
		makeEvent("svc-a", 100, adapter.SeverityWarn),
		makeEvent("svc-a", 200, adapter.SeverityError),
	))

	ctx := context.Background()
	results := corr.Run(ctx)

	var got []adapter.Severity
	for r := range results {
		got = append(got, r.Event.Severity)
	}

	if len(got) != 3 {
		t.Fatalf("expected 3 events, got %d", len(got))
	}
	if got[2] != adapter.SeverityError {
		t.Errorf("last event should be ERROR, got %v", got[2])
	}
}

// TestMergeEmptySource verifies that an empty source doesn't block or panic.
func TestMergeEmptySource(t *testing.T) {
	cfg := defaultCfg()
	corr := New(cfg)

	corr.AddSource("empty", chanOf()) // no events
	corr.AddSource("real", chanOf(
		makeEvent("svc-a", 0, adapter.SeverityInfo),
	))

	ctx := context.Background()
	results := corr.Run(ctx)

	count := 0
	for range results {
		count++
	}
	if count != 1 {
		t.Errorf("expected 1 event from non-empty source, got %d", count)
	}
}

// TestMergeNoSources verifies that a correlator with no sources closes immediately.
func TestMergeNoSources(t *testing.T) {
	cfg := defaultCfg()
	corr := New(cfg)

	ctx := context.Background()
	results := corr.Run(ctx)

	count := 0
	for range results {
		count++
	}
	if count != 0 {
		t.Errorf("expected 0 events, got %d", count)
	}
}

// TestContextCancellation verifies the correlator stops when context is cancelled.
func TestContextCancellation(t *testing.T) {
	cfg := defaultCfg()
	corr := New(cfg)

	// Unbuffered channel — will block waiting for consumer.
	ch := make(chan adapter.Event)
	go func() {
		defer close(ch)
		for i := 0; i < 1000; i++ {
			ch <- makeEvent("svc-a", i*10, adapter.SeverityInfo)
		}
	}()
	corr.AddSource("slow", ch)

	ctx, cancel := context.WithCancel(context.Background())

	results := corr.Run(ctx)

	// Read a few then cancel.
	<-results
	<-results
	cancel()

	// Drain remaining — should close eventually.
	for range results {
	}
	// If we get here without deadlock, the test passes.
}

// TestAnomalyDetection verifies that a spike in error rate is detected.
//
// The detector compares recent-half rate vs older-half rate. To fire:
//   - Need ≥5 events in the older half (first 2.5 min of the 5-min window)
//   - Recent-half rate must be ≥ multiplier × older-half rate
func TestAnomalyDetection(t *testing.T) {
	b := newBaselineTracker(5*time.Minute, 10)
	base := time.Date(2024, 1, 15, 14, 0, 0, 0, time.UTC)

	// Baseline: 6 errors at 0s, 10s, 20s, 30s, 40s, 50s (all stay in the older half
	// once spike events push t to ~230s, making halfCutoff ~79s).
	for i := 0; i < 6; i++ {
		b.record("svc", base.Add(time.Duration(i)*10*time.Second))
	}

	// Spike: 80 errors in the recent half (150s–229s from base).
	// At t=229s: halfCutoff=79s; olderCount=6 (0-50s); recentCount=80.
	// recentRate = 80/2.5 = 32/min; baselineRate = 6/2.5 = 2.4/min; 32 >= 24 ✓
	spikeStart := base.Add(150 * time.Second) // 2.5 minutes in
	var lastAnomaly bool
	for i := 0; i < 80; i++ {
		lastAnomaly, _ = b.record("svc", spikeStart.Add(time.Duration(i)*time.Second))
	}

	if !lastAnomaly {
		t.Error("expected anomaly to be detected after error rate spike")
	}
}

// TestAnomalyDetectionInsufficientHistory verifies no false positive with few events.
func TestAnomalyDetectionInsufficientHistory(t *testing.T) {
	b := newBaselineTracker(5*time.Minute, 10)
	base := time.Date(2024, 1, 15, 14, 0, 0, 0, time.UTC)

	// Only 3 events — below the minimum history threshold.
	b.record("svc", base)
	b.record("svc", base.Add(1*time.Second))
	anomaly, _ := b.record("svc", base.Add(2*time.Second))

	if anomaly {
		t.Error("should not detect anomaly with insufficient history")
	}
}
