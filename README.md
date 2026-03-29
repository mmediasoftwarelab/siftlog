# SiftLog

**CLI tool for distributed log correlation. Ingests from Loki, CloudWatch, Elasticsearch, and files — merges by timestamp, detects cascades, surfaces the failure signal. Not a dashboard.**

---

When a cascading failure hits your system, the signal is spread across multiple log streams — each with its own timestamp format, clock drift, and noise floor. Existing observability tools show you *that* something is wrong. SiftLog shows you *what* happened and in what order, in your terminal, in seconds.

```
SIFTLOG v0.2.0  |  sources: 2  |  window: 5000ms  |  anchor: sender
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
[signal:cascade] payment-service → order-service → notification-service

  14:22:03.441  payment-service    ERROR  connection pool exhausted (pool_size=20, waiting=47)
  14:22:03.512  payment-service    ERROR  request timeout after 5000ms [trace: a3f9c2]
  14:22:03.891  order-service      ERROR  upstream payment-service unavailable [trace: a3f9c2]
  14:22:04.102  order-service      WARN   retry 1/3 to payment-service
  14:22:04.608  order-service      ERROR  retry 2/3 failed
  14:22:05.112  order-service      ERROR  retry 3/3 failed — marking payment-service degraded
  14:22:05.201  notification-svc   ERROR  order status webhook failed: order-service 503
  14:22:05.310  notification-svc   WARN   queue depth: 847 (threshold: 100)

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
noise suppressed: 2,847 events  |  signal: 8 events  |  elapsed: 1.2s
```

---

## Install

```bash
go install github.com/mmediasoftwarelab/siftlog@latest
```

Or build from source:

```bash
git clone https://github.com/mmediasoftwarelab/siftlog.git
cd siftlog
go build -o siftlog .
```

**Requires Go 1.22+**

---

## Quickstart

```bash
# Correlate two log files, show everything
siftlog app.log worker.log

# Last 15 minutes, signal only
siftlog --since 15m --quiet app.log worker.log

# Pipe signal to jq
siftlog --output json --quiet app.log | jq '.events[] | select(.severity == "error")'

# Read from stdin
kubectl logs -l app=payment-service --since=15m | siftlog -

# Tail a file in real time
siftlog --live app.log

# Live with a custom flush window
siftlog --live --flush-ms 200 app.log
```

---

## How it works

SiftLog reads log events from one or more sources simultaneously, normalizes them to a common format, and merges them in timestamp order using a min-heap. It then runs signal detection across the merged stream:

**Anomaly rate detection** — flags services whose error rate exceeds a configurable multiple of their recent baseline.

**Cascade detection** — when service A errors and service B begins erroring within the correlation window, SiftLog marks it as a cascade and orders the output to show the originating service first. Uses trace ID correlation as the primary signal; falls back to temporal correlation when trace IDs are absent.

**Silence detection** — a service that stops producing logs is often more significant than one producing errors. Flags services that drop below their expected volume based on a rolling baseline.

---

## Configuration

No config file required to get started. For multi-source and persistent configuration:

```yaml
# siftlog.yaml

sources:
  - name: production-loki
    type: loki
    url: https://loki.internal
    auth:
      type: bearer
      token_env: LOKI_TOKEN

  - name: cloudwatch-payments
    type: cloudwatch
    region: us-east-1
    log_groups:
      - /ecs/payment-service
      - /ecs/order-service
    timestamp_offset_ms: 0  # correct systematic clock offset per source

correlation:
  anchor: sender      # sender | receiver
  window_ms: 5000
  drift_tolerance_ms: 200

signal:
  anomaly_threshold_multiplier: 10
  cascade_detection: true
  silence_detection: true
```

### Per-source clock offset

If one of your sources has a clock that runs consistently fast or slow relative to the others, correct it before correlation — not by widening the window:

```yaml
sources:
  - name: on-prem-legacy
    timestamp_offset_ms: -200  # clock runs 200ms fast; shift back
```

Positive shifts timestamps forward (clock is behind). Negative shifts it back (clock is ahead). This is applied before the correlation engine sees the event — the rest of the pipeline works with corrected timestamps only.

---

## CLI reference

```
siftlog [file...] [flags]

Commands:
  version   Print version information

Flags:
  --config string    Path to config file (default: ./siftlog.yaml)
  --output string    Output format: human | json | compact (default: human)
  --since string     Start time: 15m, 1h, 2024-01-01T00:00:00Z
  --until string     End time
  --window string    Correlation window duration (default: 5000ms)
  --quiet            Signal only — suppress noise
  --verbose          Include all events and structured fields
  --live             Tail sources and stream events in real time
  --flush-ms int     Live mode: max ms before oldest buffered event is emitted (default 500)
```

---

## Log source support

| Source                     | Status      |
| -------------------------- | ----------- |
| File / stdin               | Available   |
| Grafana Loki               | Available   |
| AWS CloudWatch Logs        | Available   |
| Elasticsearch / OpenSearch | Available   |
| Datadog Logs               | Planned     |
| Google Cloud Logging       | Planned     |

Log formats handled automatically: JSON (structured), plain text with heuristic timestamp and severity extraction. Handles RFC3339, space-separated datetime, Unix epoch (seconds and milliseconds), and common framework variants.

---

## Why this exists

I spent three years complaining about the gap between "the dashboard says something is wrong" and "I know what is wrong." Dashboards are good at the first thing. They are not built for the second thing. The second thing requires reading logs across services in time order, which is tedious and error-prone to do manually and which every engineer I have worked with has done manually at 2 AM at some point.

I built SiftLog at 2 AM on a Tuesday when I couldn't sleep and the problem was still interesting.

If it helps you, it was worth the Tuesday.

---

## Contributing

Issues and pull requests welcome. If SiftLog helped you find something that would have taken hours, open an issue and tell me — that's the whole point.

**[mmediasoftwarelab.com](https://www.mmediasoftwarelab.com)**
