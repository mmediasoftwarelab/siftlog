# SiftLog — Technical Specification

**Version:** 0.0.1
**Author:** M Media — [mmediasoftwarelab.com](https://www.mmediasoftwarelab.com)
**GitHub:** [github.com/mmediasoftwarelab/siftlog](https://github.com/mmediasoftwarelab/siftlog)
**Status:** Active development
**Language:** Go 1.22+
**License:** MIT

---

## Problem Statement

Distributed systems fail in ways that are difficult to observe. When a cascading failure propagates across services, the signal is spread across multiple log streams, each with its own timestamp format, clock drift, and noise floor. Existing observability tools address this with dashboards: visual aggregations of metrics that tell you _that_ something went wrong and approximately _when_, but not _what_ — not the specific sequence of events, the origin service, the propagation path, or the moment the first thing failed.

Dashboards are built for on-call rotations and executive reviews. They answer the question "is the system healthy?" They do not answer the question "what actually happened?"

SiftLog answers the second question.

SiftLog ingests logs from multiple sources simultaneously, correlates events across service boundaries by timestamp and trace context, and surfaces the signal: the specific sequence of events that preceded and constituted the failure, presented in a format an engineer can read and act on in a terminal, without clicking through a UI or waiting for a query to render.

It is not a dashboard. It is a tool.

---

## Design Philosophy

**1. CLI-first, always.**
SiftLog runs in a terminal. It produces output a human can read and a script can parse. It does not have a web UI. It will not have a web UI. If you want a web UI, you can pipe SiftLog's JSON output to whatever renders JSON.

**2. Signal over completeness.**
SiftLog does not try to show you everything. It tries to show you the thing that matters. This requires making decisions about what matters, and those decisions are configurable, and the defaults represent a considered opinion rather than a refusal to have one.

**3. Honest about trade-offs.**
Every architectural decision in SiftLog involves a trade-off. The trade-offs are documented. The configuration options exist because the correct default depends on your infrastructure, not because we couldn't decide.

**4. Respect the engineer's time.**
SiftLog should be fast to install, fast to configure, and fast to produce output. A tool that takes twenty minutes to set up before it helps you is not a tool for incidents.

---

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                    CLI Interface                    │
└────────────────────────┬────────────────────────────┘
                         │
┌────────────────────────▼────────────────────────────┐
│                  Ingestion Layer                    │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌────────┐ │
│  │   ELK    │ │   Loki   │ │CloudWatch│ │  File  │ │
│  │ Adapter  │ │ Adapter  │ │ Adapter  │ │ Adapter│ │
│  └────┬─────┘ └────┬─────┘ └────┬─────┘ └───┬────┘ │
└───────┼────────────┼────────────┼────────────┼──────┘
        │            │            │            │
┌───────▼────────────▼────────────▼────────────▼──────┐
│               Normalization Layer                   │
│         (timestamps, severity, fields)              │
└────────────────────────┬────────────────────────────┘
                         │
┌────────────────────────▼────────────────────────────┐
│              Correlation Engine                     │
│   - Timestamp correlation with configurable anchor  │
│   - Trace ID / request ID propagation               │
│   - Causal chain reconstruction                     │
│   - Noise floor suppression                         │
└────────────────────────┬────────────────────────────┘
                         │
┌────────────────────────▼────────────────────────────┐
│                 Output Formatter                    │
│         (human / JSON / pipe-friendly)              │
└─────────────────────────────────────────────────────┘
```

---

## Core Concepts

### Correlation Window

The correlation window is the time range within which events from different services are considered potentially related. Default: `500ms`. Configurable per-source and globally.

Events outside the correlation window are not excluded — they appear in the full output. They are de-emphasized in the signal output.

### Timestamp Anchor

When correlating events across services, SiftLog must decide which timestamp to trust when clocks disagree. This is not a trivial decision. Two strategies are available:

**`anchor: sender`** (default)
Use the timestamp recorded by the service that generated the event. Assumes the sending service has accurate time. Appropriate when services are well-configured with NTP and clock drift is minimal.

**`anchor: receiver`**
Use the timestamp recorded by the receiving service or log aggregator at the time of ingestion. Appropriate when sending services have clock drift — common in distributed environments spanning multiple cloud providers or on-prem/cloud hybrid setups.

_This option was added after a user with a multi-provider notification pipeline correctly identified that the default produced misleading correlation output when provider clocks diverged by more than 200ms. She was right. The option exists because she asked._

Configuration:

```yaml
correlation:
  anchor: sender # or: receiver
  window_ms: 500
  drift_tolerance_ms:
    200 # events within this range of the window boundary
    # are included with a drift warning rather than excluded
```

### Per-Source Timestamp Offset

Some services have clocks that are systematically displaced from all other sources by a consistent, known amount — not random drift, but a fixed offset. This is the log correlation equivalent of an audio sync problem: one track is consistently N milliseconds early or late relative to the others, and the fix is not to widen the correlation window but to shift that source's timeline before correlation begins.

`timestamp_offset_ms` is a per-source calibration value applied to every event from that source before it enters the correlation engine. The correlation engine sees corrected timestamps only.

```yaml
sources:
  - name: on-prem-legacy
    type: loki
    url: https://loki.internal
    timestamp_offset_ms: -200  # this source's clock runs 200ms fast; shift back
```

Positive values shift timestamps forward (source clock is behind). Negative values shift timestamps backward (source clock is ahead).

This is distinct from `anchor: receiver`, which changes the trust model globally. `timestamp_offset_ms` is a surgical, per-source correction for known, measurable offsets.

**Future:** SiftLog may optionally detect consistent timestamp deltas on shared trace IDs across sources and suggest an offset value. This would be advisory only — the value must be set explicitly in config.

### Signal Detection

SiftLog identifies signal using three mechanisms:

**1. Anomaly rate detection**
Error rate per service per time window. Configurable threshold. A service producing errors at 10x its baseline rate within a 30-second window is flagged.

**2. Cascade detection**
When service A errors, and service B begins erroring within the correlation window, and service B has a known dependency on service A (from configuration or inferred from trace context), SiftLog marks this as a potential cascade and orders the output to show the originating service first.

**3. Silence detection**
A service that stops producing logs is often more significant than one producing error logs. Silence detection flags services that have dropped below their expected log volume by a configurable threshold. Default: 90% reduction from rolling 5-minute baseline.

---

## Log Source Adapters

SiftLog ships with adapters for:

| Adapter                    | Status      | Auth                  |
| -------------------------- | ----------- | --------------------- |
| File / stdin               | Planned     | None                  |
| Grafana Loki               | Planned     | Bearer token          |
| AWS CloudWatch Logs        | Planned     | IAM / env credentials |
| Elasticsearch / OpenSearch | Planned     | API key, basic auth   |
| Datadog Logs               | Future      | API key               |
| Google Cloud Logging       | Future      | Service account       |

Adapters implement a common interface defined in the codebase. Custom adapters can be built against that interface.

---

## CLI Reference

```
siftlog [command] [flags]

Commands:
  run       Start ingestion and correlation (default)
  replay    Replay a saved session file
  config    Validate and display current configuration
  sources   List configured and available log sources
  version   Print version information

Global Flags:
  --config string    Path to config file (default: ./siftlog.yaml, then ~/.siftlog/config.yaml)
  --output string    Output format: human | json | compact (default: human)
  --window string    Correlation window override (e.g. 1s, 250ms)
  --since string     Start time for historical queries (e.g. 15m, 1h, 2024-01-01T00:00:00Z)
  --until string     End time for historical queries
  --services string  Comma-separated list of services to include (default: all)
  --quiet            Suppress noise output, show signal only
  --verbose          Include all events, not just signal
  --save string      Save session to file for replay
```

### Example: Live incident triage

```bash
# Show signal from all services, last 15 minutes, quiet mode
siftlog --since 15m --quiet

# Show everything from payment-service and order-service
siftlog --services payment-service,order-service --verbose --since 30m

# Pipe signal output to jq for further filtering
siftlog --output json --quiet | jq '.events[] | select(.severity == "error")'
```

---

## Configuration File

```yaml
# siftlog.yaml

sources:
  - name: production-loki
    type: loki
    url: https://loki.internal
    auth:
      type: bearer
      token_env: LOKI_TOKEN
    labels:
      env: production

  - name: cloudwatch-payments
    type: cloudwatch
    region: us-east-1
    log_groups:
      - /ecs/payment-service
      - /ecs/order-service
    timestamp_offset_ms: 0  # set if this source has a known clock offset

correlation:
  anchor: sender
  window_ms: 500
  drift_tolerance_ms: 200

signal:
  anomaly_threshold_multiplier: 10 # flag if error rate exceeds 10x baseline
  cascade_detection: true
  silence_detection: true
  silence_threshold_pct: 90 # flag if log volume drops >90% from baseline
  baseline_window_minutes: 5

output:
  timestamps: relative # relative | absolute | unix
  severity_colors: true
  max_lines_per_event: 5 # truncate long log lines in human output
  show_service_name: true

session:
  save_dir: ~/.siftlog/sessions # auto-save sessions for replay
  retention_days: 7
```

---

## Output Format

### Human (default)

```
SIFTLOG v0.0.1  |  sources: 2  |  window: 500ms  |  anchor: sender
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
[signal] Cascade detected: payment-service → order-service → notification-service

  14:22:03.441  payment-service    ERROR  connection pool exhausted (pool_size=20, waiting=47)
  14:22:03.512  payment-service    ERROR  request timeout after 5000ms [trace: a3f9c2]
  14:22:03.891  order-service      ERROR  upstream payment-service unavailable [trace: a3f9c2]
  14:22:04.102  order-service      WARN   retry 1/3 to payment-service
  14:22:04.608  order-service      ERROR  retry 2/3 failed
  14:22:05.112  order-service      ERROR  retry 3/3 failed — marking payment-service degraded
  14:22:05.201  notification-svc   ERROR  order status webhook failed: order-service 503
  14:22:05.310  notification-svc   WARN   queue depth: 847 (threshold: 100)

[drift warning] order-service clock behind production-loki anchor by 312ms — using receiver timestamp
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
noise suppressed: 2,847 events  |  signal: 8 events  |  elapsed: 1.2s
```

---

## Known Limitations and Trade-offs

**Timestamp correlation is probabilistic.**
SiftLog correlates by time and trace context. If your services do not propagate trace IDs, correlation is timestamp-only, and timestamp-only correlation can produce false positives in high-traffic environments where many events occur within the correlation window. Inject trace IDs. This is good practice regardless of SiftLog.

**Cascade detection requires observed dependency patterns.**
SiftLog infers service dependencies from trace context and observed co-failure patterns. It does not read your service mesh configuration. In the first hours of use, cascade detection is less accurate than after SiftLog has observed normal failure patterns. This improves over time.

**Silence detection has a bootstrap problem.**
The silence detection baseline requires 5 minutes of observed traffic to establish. During the first 5 minutes of a session, silence detection is disabled. This is documented in the output header.

**The anchor decision is yours to make.**
The `anchor` configuration default (`sender`) is correct for most well-managed environments. If your infrastructure spans multiple providers or your on-call rotation has been blaming the wrong service, try `anchor: receiver`. If the output changes significantly, your clocks are drifting. Fix the clocks. Use `receiver` in the meantime.

**SiftLog does not replace your existing observability stack.**
It reads from it. SiftLog is a lens, not a replacement. If your logs are not in Elasticsearch, Loki, CloudWatch, or a file, SiftLog cannot help you until you write an adapter. The adapter interface is not complicated.

---

## Why This Exists

I spent three years complaining about the gap between "the dashboard says something is wrong" and "I know what is wrong." Dashboards are good at the first thing. They are not built for the second thing. The second thing requires reading logs across services in time order, which is tedious and error-prone to do manually and which every engineer I have worked with has done manually at 2 AM at some point.

I built SiftLog at 2 AM on a Tuesday when I couldn't sleep and the problem was still interesting.

If it helps you, it was worth the Tuesday.

---

_Issues, questions, and pull requests at [github.com/mmediasoftwarelab/siftlog](https://github.com/mmediasoftwarelab/siftlog)._
_If SiftLog helped you find something that would have taken hours, I want to know. That's the whole point._
