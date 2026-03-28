package cloudwatch

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/mmediasoftwarelab/siftlog/internal/adapter"
	"github.com/mmediasoftwarelab/siftlog/internal/config"
)

// mockCWClient implements CloudWatchLogsAPI for testing.
type mockCWClient struct {
	pages []*cloudwatchlogs.FilterLogEventsOutput
	page  int
	err   error
}

func (m *mockCWClient) FilterLogEvents(_ context.Context, _ *cloudwatchlogs.FilterLogEventsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.FilterLogEventsOutput, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.page >= len(m.pages) {
		return &cloudwatchlogs.FilterLogEventsOutput{}, nil
	}
	out := m.pages[m.page]
	m.page++
	return out, nil
}

func makeLogEvent(tsMs int64, msg string) types.FilteredLogEvent {
	return types.FilteredLogEvent{
		Timestamp:     aws.Int64(tsMs),
		Message:       aws.String(msg),
		LogStreamName: aws.String("ecs/payment-service/abc123"),
	}
}

func TestCloudWatchFetch(t *testing.T) {
	ts1 := time.Date(2024, 1, 15, 14, 22, 3, 0, time.UTC)
	ts2 := time.Date(2024, 1, 15, 14, 22, 4, 0, time.UTC)

	mock := &mockCWClient{
		pages: []*cloudwatchlogs.FilterLogEventsOutput{
			{
				Events: []types.FilteredLogEvent{
					makeLogEvent(ts1.UnixMilli(), `{"level":"error","message":"timeout","trace_id":"abc"}`),
					makeLogEvent(ts2.UnixMilli(), `{"level":"warn","message":"retrying"}`),
				},
				NextToken: nil,
			},
		},
	}

	a := newWithClient(config.SourceConfig{
		Name:      "test-cw",
		Region:    "us-east-1",
		LogGroups: []string{"/ecs/payment-service"},
	}, mock)

	ch, err := a.Fetch(context.Background(), ts1.Add(-time.Minute), ts2.Add(time.Minute))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	var events []adapter.Event
	for ev := range ch {
		events = append(events, ev)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Severity != adapter.SeverityError {
		t.Errorf("first event severity: want ERROR, got %v", events[0].Severity)
	}
	if events[0].TraceID != "abc" {
		t.Errorf("trace_id: want abc, got %q", events[0].TraceID)
	}
	if events[0].Fields["log_group"] != "/ecs/payment-service" {
		t.Errorf("log_group field: want /ecs/payment-service, got %q", events[0].Fields["log_group"])
	}
}

func TestCloudWatchServiceFromGroup(t *testing.T) {
	tests := []struct {
		group string
		want  string
	}{
		{"/ecs/payment-service", "payment-service"},
		{"/ecs/order-service", "order-service"},
		{"payment-service", "payment-service"},
		{"/", ""},
	}
	for _, tc := range tests {
		got := serviceFromGroup(tc.group)
		if got != tc.want {
			t.Errorf("serviceFromGroup(%q): want %q, got %q", tc.group, tc.want, got)
		}
	}
}

func TestCloudWatchTimestampOffset(t *testing.T) {
	ts := time.Date(2024, 1, 15, 14, 22, 3, 0, time.UTC)
	mock := &mockCWClient{
		pages: []*cloudwatchlogs.FilterLogEventsOutput{
			{Events: []types.FilteredLogEvent{makeLogEvent(ts.UnixMilli(), "hello")}},
		},
	}

	a := newWithClient(config.SourceConfig{
		Name:              "test-cw",
		LogGroups:         []string{"/ecs/svc"},
		TimestampOffsetMs: -200,
	}, mock)

	ch, _ := a.Fetch(context.Background(), ts.Add(-time.Minute), ts.Add(time.Minute))
	var events []adapter.Event
	for ev := range ch {
		events = append(events, ev)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	expected := ts.Add(-200 * time.Millisecond)
	if !events[0].Timestamp.Equal(expected) {
		t.Errorf("timestamp after offset: want %v, got %v", expected, events[0].Timestamp)
	}
}

func TestCloudWatchMultipleLogGroups(t *testing.T) {
	// One page per log group — the mock advances its page counter on each call.
	mock := &mockCWClient{
		pages: []*cloudwatchlogs.FilterLogEventsOutput{
			{Events: []types.FilteredLogEvent{makeLogEvent(time.Now().UnixMilli(), "event from group 1")}},
			{Events: []types.FilteredLogEvent{makeLogEvent(time.Now().UnixMilli(), "event from group 2")}},
		},
	}

	a := newWithClient(config.SourceConfig{
		Name:      "test-cw",
		LogGroups: []string{"/ecs/payment-service", "/ecs/order-service"},
	}, mock)

	ch, _ := a.Fetch(context.Background(), time.Now().Add(-time.Minute), time.Now())
	count := 0
	for range ch {
		count++
	}

	// Each group fetches from the mock, which returns 1 event per call.
	if count != 2 {
		t.Errorf("expected 1 event per log group = 2 total, got %d", count)
	}
}

func TestCloudWatchAPIError(t *testing.T) {
	mock := &mockCWClient{err: fmt.Errorf("access denied")}

	a := newWithClient(config.SourceConfig{
		Name:      "test-cw",
		LogGroups: []string{"/ecs/svc"},
	}, mock)

	ch, _ := a.Fetch(context.Background(), time.Now().Add(-time.Minute), time.Now())
	var events []adapter.Event
	for ev := range ch {
		events = append(events, ev)
	}

	if len(events) != 1 || events[0].Severity != adapter.SeverityError {
		t.Errorf("expected one error event for API failure, got %d events", len(events))
	}
}
