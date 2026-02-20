# Kitsune Architecture v0.02

## Generic DB Worker for Multi-Service Use (Draft)

### Goal
Make `dbWorker` reusable by any service while keeping strong security, clear ownership, and predictable operations.

### Chosen Approach
- Execution model: **Kafka commands only**
- Contract strictness: **Strict schema validation**
- Isolation: **Moderate isolation** (service namespace + allowlists)
- Outcome delivery: **Result topic enabled from day one**

### Why not gRPC + sqlx object?
- An `sqlx.DB`/connection object is process-local and cannot be safely passed between services.
- Sending raw SQL intent from producers weakens the trust boundary and increases injection/misuse risk.
- Keep SQL execution authority inside `dbWorker`; producers send only command events.

### Minimal Event Contract (Required)
- `event_id`
- `service`
- `command`
- `command_version`
- `schema_version`
- `payload` (validated per command)
- `idempotency_key`
- `occurred_at`
- `trace_id`
- `metadata` (producer identity, optional tenant/context)

### Command Resolution Model
- Worker resolves command by: `service + command + command_version`
- Unknown command/version => reject and send failure to result topic (or DLQ based on policy)
- Producers are not allowed to send executable SQL

### Idempotency Model
- Idempotency scope: `service + idempotency_key`
- Store processing state: `processed`, `duplicate`, `failed_retryable`, `failed_terminal`
- Duplicate events return deterministic result without re-executing DB mutation

### Result Topic Contract
- Worker publishes result events with:
  - `event_id`, `service`, `command`, `status`, `error_code`, `error_message`, `processed_at`, `trace_id`
- Producers consume asynchronously for workflow continuation and observability

### Reliability & Failure Policy
- Retry with bounded backoff for retryable DB/Kafka errors
- Terminal failures routed to DLQ with reason taxonomy
- Replay supported from Kafka with idempotency protection

### Security & Governance
- Per-service command namespace (e.g., `user.create`, `billing.charge.capture`)
- Producer allowlist per service namespace
- Strict schema checks (required fields, type checks, version checks)
- No raw SQL in events

### Observability Baseline
- Metrics: consumer lag, processing latency by command, success/error rate, duplicate suppression rate, DLQ volume
- Logs: structured logs with `event_id`, `service`, `command`, `trace_id`
- Tracing: propagate trace context from API -> Kafka -> dbWorker -> result topic

### Service Onboarding Checklist
- Register service namespace and allowed commands
- Define payload schema and command versions
- Publish to `db-events` using required envelope
- Consume from `db-results` for async outcomes
- Pass contract compatibility tests before production rollout
