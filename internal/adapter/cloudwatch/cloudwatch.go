package cloudwatch

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/mmediasoftwarelab/siftlog/internal/adapter"
	"github.com/mmediasoftwarelab/siftlog/internal/config"
)

// CloudWatchLogsAPI is the subset of the CloudWatch Logs client we use.
// Defined as an interface so tests can inject a mock.
type CloudWatchLogsAPI interface {
	FilterLogEvents(ctx context.Context, params *cloudwatchlogs.FilterLogEventsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.FilterLogEventsOutput, error)
}

// Adapter queries AWS CloudWatch Logs using FilterLogEvents across one or
// more log groups.
type Adapter struct {
	cfg    config.SourceConfig
	client CloudWatchLogsAPI
}

// New creates an Adapter using the default AWS credential chain
// (env vars, shared credentials file, EC2/ECS instance role).
func New(cfg config.SourceConfig) (*Adapter, error) {
	if cfg.Region == "" {
		return nil, fmt.Errorf("cloudwatch adapter %q: region is required", cfg.Name)
	}
	if len(cfg.LogGroups) == 0 {
		return nil, fmt.Errorf("cloudwatch adapter %q: at least one log_group is required", cfg.Name)
	}

	awscfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(cfg.Region),
	)
	if err != nil {
		return nil, fmt.Errorf("cloudwatch adapter %q: aws config: %w", cfg.Name, err)
	}

	return &Adapter{
		cfg:    cfg,
		client: cloudwatchlogs.NewFromConfig(awscfg),
	}, nil
}

// newWithClient creates an Adapter with a pre-built client (used in tests).
func newWithClient(cfg config.SourceConfig, client CloudWatchLogsAPI) *Adapter {
	return &Adapter{cfg: cfg, client: client}
}

func (a *Adapter) Name() string { return a.cfg.Name }

func (a *Adapter) Fetch(ctx context.Context, since, until time.Time) (<-chan adapter.Event, error) {
	if since.IsZero() {
		since = time.Now().Add(-15 * time.Minute)
	}
	if until.IsZero() {
		until = time.Now()
	}

	ch := make(chan adapter.Event, 256)
	go func() {
		defer close(ch)
		for _, group := range a.cfg.LogGroups {
			if err := a.fetchGroup(ctx, group, since, until, ch); err != nil {
				ch <- errorEvent(a.cfg.Name, group, err)
			}
		}
	}()

	return ch, nil
}

func (a *Adapter) fetchGroup(ctx context.Context, group string, since, until time.Time, ch chan<- adapter.Event) error {
	input := &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName:  aws.String(group),
		StartTime:     aws.Int64(since.UnixMilli()),
		EndTime:       aws.Int64(until.UnixMilli()),
		FilterPattern: aws.String(""),
	}

	paginator := cloudwatchlogs.NewFilterLogEventsPaginator(a.client, input)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("FilterLogEvents: %w", err)
		}
		for _, e := range page.Events {
			ev := parseEvent(e, group, a.cfg)
			applyOffset(&ev, a.cfg.TimestampOffsetMs)
			select {
			case ch <- ev:
			case <-ctx.Done():
				return nil
			}
		}
	}
	return nil
}

// parseEvent converts a CloudWatch FilteredLogEvent to a normalized Event.
func parseEvent(e types.FilteredLogEvent, group string, cfg config.SourceConfig) adapter.Event {
	t := time.UnixMilli(aws.ToInt64(e.Timestamp)).UTC()
	ev := adapter.Event{
		Timestamp:    t,
		RawTimestamp: t,
		Source:       cfg.Name,
		Service:      serviceFromGroup(group),
		Fields:       map[string]string{"log_group": group},
	}
	if e.LogStreamName != nil {
		ev.Fields["log_stream"] = *e.LogStreamName
	}
	if e.Message != nil {
		parseMessage(*e.Message, &ev)
	}
	return ev
}

// serviceFromGroup extracts a service name from a CloudWatch log group path.
// e.g. "/ecs/payment-service" → "payment-service"
func serviceFromGroup(group string) string {
	for i := len(group) - 1; i >= 0; i-- {
		if group[i] == '/' {
			return group[i+1:]
		}
	}
	return group
}

// parseMessage handles JSON-structured and plain-text log messages.
func parseMessage(msg string, ev *adapter.Event) {
	if len(msg) > 0 && msg[0] == '{' {
		if tryParseJSON(msg, ev) {
			return
		}
	}
	ev.Message = msg
}

func tryParseJSON(msg string, ev *adapter.Event) bool {
	// Reuse the same field-name conventions as the file adapter.
	// Inline to avoid a cross-package import cycle.
	var raw map[string]any
	if err := parseJSONRaw(msg, &raw); err != nil {
		return false
	}
	for _, k := range []string{"level", "severity", "lvl"} {
		if v, ok := raw[k]; ok {
			ev.Severity = adapter.ParseSeverity(fmt.Sprint(v))
			break
		}
	}
	for _, k := range []string{"message", "msg", "log"} {
		if v, ok := raw[k]; ok {
			ev.Message = fmt.Sprint(v)
			break
		}
	}
	for _, k := range []string{"trace_id", "traceId", "request_id"} {
		if v, ok := raw[k]; ok {
			ev.TraceID = fmt.Sprint(v)
			break
		}
	}
	return ev.Message != ""
}

func applyOffset(ev *adapter.Event, offsetMs int64) {
	if offsetMs != 0 {
		ev.RawTimestamp = ev.Timestamp
		ev.Timestamp = ev.Timestamp.Add(time.Duration(offsetMs) * time.Millisecond)
	}
}

func errorEvent(source, group string, err error) adapter.Event {
	return adapter.Event{
		Timestamp: time.Now(),
		Source:    source,
		Service:   source,
		Severity:  adapter.SeverityError,
		Message:   fmt.Sprintf("cloudwatch adapter error (group %s): %v", group, err),
	}
}
