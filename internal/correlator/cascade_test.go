package correlator

import (
	"testing"
	"time"

	"github.com/mmediasoftwarelab/siftlog/internal/adapter"
)

var (
	base   = time.Date(2024, 1, 15, 14, 22, 0, 0, time.UTC)
	window = 500 * time.Millisecond
)

func errEvent(service, traceID string, offsetMs int) adapter.Event {
	return adapter.Event{
		Service:   service,
		TraceID:   traceID,
		Severity:  adapter.SeverityError,
		Timestamp: base.Add(time.Duration(offsetMs) * time.Millisecond),
		Message:   "test error",
	}
}

func infoEvent(service string, offsetMs int) adapter.Event {
	return adapter.Event{
		Service:   service,
		Severity:  adapter.SeverityInfo,
		Timestamp: base.Add(time.Duration(offsetMs) * time.Millisecond),
		Message:   "test info",
	}
}

// TestCascadeTraceID verifies detection when services share a trace ID.
func TestCascadeTraceID(t *testing.T) {
	c := newCascadeTracker(window)

	// Service A errors first on trace abc.
	r := c.record(errEvent("payment-service", "abc", 0))
	if r.CascadeFrom != "" {
		t.Errorf("first service to error should not have a cascade source, got %q", r.CascadeFrom)
	}

	// Service B errors on the same trace shortly after.
	r = c.record(errEvent("order-service", "abc", 200))
	if r.CascadeFrom != "payment-service" {
		t.Errorf("order-service should cascade from payment-service, got %q", r.CascadeFrom)
	}
	if !r.IsNew {
		t.Error("first detection of this cascade should be marked IsNew")
	}
}

// TestCascadeChain verifies A → B → C propagation.
func TestCascadeChain(t *testing.T) {
	c := newCascadeTracker(window)

	c.record(errEvent("payment-service", "abc", 0))
	c.record(errEvent("order-service", "abc", 100))
	r := c.record(errEvent("notification-svc", "abc", 300))

	if r.CascadeFrom != "payment-service" {
		t.Errorf("notification-svc should trace back to payment-service, got %q", r.CascadeFrom)
	}
}

// TestCascadeIsNewOnlyOnce verifies the IsNew flag fires once per relationship.
func TestCascadeIsNewOnlyOnce(t *testing.T) {
	c := newCascadeTracker(window)

	c.record(errEvent("payment-service", "abc", 0))

	// First detection.
	r1 := c.record(errEvent("order-service", "abc", 100))
	if !r1.IsNew {
		t.Error("first cascade detection should be IsNew=true")
	}

	// Subsequent error from the same dependent service on same cascade.
	r2 := c.record(errEvent("order-service", "abc", 200))
	if r2.IsNew {
		t.Error("subsequent cascade events should not be IsNew")
	}
	if r2.CascadeFrom != "payment-service" {
		t.Errorf("cascade source should still be set, got %q", r2.CascadeFrom)
	}
}

// TestCascadeOutsideWindow verifies events too far apart are not linked.
func TestCascadeOutsideWindow(t *testing.T) {
	c := newCascadeTracker(window)

	c.record(errEvent("payment-service", "abc", 0))

	// Service B errors 2 seconds later — well outside the 500ms window.
	r := c.record(errEvent("order-service", "abc", 2000))
	if r.CascadeFrom != "" {
		t.Errorf("events outside window should not cascade, got %q", r.CascadeFrom)
	}
}

// TestCascadeNoTraceID verifies temporal cascade detection without trace IDs.
func TestCascadeNoTraceID(t *testing.T) {
	c := newCascadeTracker(window)

	// A errors with no trace ID.
	c.record(errEvent("payment-service", "", 0))

	// B errors shortly after, also no trace ID.
	r := c.record(errEvent("order-service", "", 200))
	if r.CascadeFrom != "payment-service" {
		t.Errorf("temporal cascade should detect payment-service as origin, got %q", r.CascadeFrom)
	}
}

// TestCascadeSameService verifies a service is never its own cascade source.
func TestCascadeSameService(t *testing.T) {
	c := newCascadeTracker(window)

	c.record(errEvent("payment-service", "abc", 0))
	r := c.record(errEvent("payment-service", "abc", 100))
	if r.CascadeFrom != "" {
		t.Errorf("service should not cascade from itself, got %q", r.CascadeFrom)
	}
}

// TestCascadeBurstExpiry verifies that a recovered service doesn't trigger
// a cascade when it later starts failing again independently.
func TestCascadeBurstExpiry(t *testing.T) {
	c := newCascadeTracker(window)

	// Service A errors, then goes quiet for longer than the window.
	c.record(errEvent("payment-service", "", 0))
	c.record(infoEvent("payment-service", 1000)) // info event after window expires burst

	// Service B errors much later — should not be linked to A's old burst.
	r := c.record(errEvent("order-service", "", 2000))
	if r.CascadeFrom != "" {
		t.Errorf("expired burst should not cascade, got %q", r.CascadeFrom)
	}
}
