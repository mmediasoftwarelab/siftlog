# SiftLog Roadmap

## CLI Track (open source, MIT)

### v0.2.0 - Live Streaming (shipped 2026-03-29)
- --live flag: tail semantics for file sources
- Flush ticker: oldest buffered event emitted within FlushMs across all sources
- Remote adapters wired into dispatch (Loki, CloudWatch, Elasticsearch)
- Correlation window default raised to 5000ms

### v0.3.0 - Adapter Expansion
- Datadog Logs adapter (DD API v2, service/tag filtering)
- Google Cloud Logging adapter (gRPC, workload identity)
- Improved LogQL builder for Loki (label matchers, line filters from config)
- Adapter health reporting (source connected / degraded / silent)

### v1.0.0 - Stable CLI
- Stable config schema (no breaking changes after this)
- --replay flag: re-run a saved session through detection engine
- Battle-tested in production by at least one external user
- Homebrew tap + GitHub release artifacts (linux/mac/windows)

---

## Platform Track (commercial, self-hosted)

The Platform is a separately licensed binary that organizations deploy in their own
infrastructure. It wraps the CLI engine with persistence, continuous monitoring,
a web surface, and an alerting layer. The CLI remains fully open source.

### Phase 1 - Always-On Engine
- Long-running daemon process (siftlogd) wrapping the correlation engine
- Persistent baseline storage (SQLite or embedded key-value) - baselines survive restarts
- Signal history: every detected cascade, anomaly, and silence written to local store
- Config hot-reload: add/remove sources without restart
- REST API for signal query and health status
- License key enforcement via license.mmediasoftwarelab.com

### Phase 2 - Alerting
- Webhook output (generic JSON payload)
- PagerDuty integration (trigger/resolve lifecycle)
- Slack integration (signal-only messages with cascade chain formatted)
- Alert deduplication: one notification per cascade relationship, not one per event
- Alert suppression rules: silence specific services or time windows

### Phase 3 - Web UI
- Signal timeline: cascade chains, anomaly spikes, silence events in chronological view
- Source health dashboard: per-adapter status, event rate, last-seen
- Baseline explorer: per-service error rate history
- Session replay: rewind signal history to any point in time
- Read-only by default, write operations require admin auth

### Phase 4 - Multi-Tenant / Enterprise
- Namespace isolation: multiple teams, separate correlation contexts
- LDAP/SSO auth
- Audit log
- Role-based access (admin, analyst, read-only)
- Enterprise license model (site license, not per-seat)

---

## Strategic Principles

- The CLI engine is the foundation. Platform phases build on top of it, not around it.
- Self-hosted is a feature, not a limitation. Target buyers have compliance and data
  residency requirements that rule out third-party SaaS.
- Single binary deployment at every phase. No Kubernetes required to get started.
- The open source CLI is top-of-funnel. Engineers find it, use it, and bring it to
  their organization. The Platform converts those organizations.
