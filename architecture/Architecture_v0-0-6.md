# Kitsune — Architecture Document

Kitsune is a self-hosted observability platform that collects logs and metrics from Kubernetes workloads and applications into a single storage and query layer, with alerting on top.

> **What changed in v0.0.6**
> - **Storage consolidation.** ClickHouse is the single store for all telemetry (logs, metrics, alert history) across hot → warm → cold tiers. PostgreSQL is kept only for small, mutable config (service tokens, alert rules). PostgreSQL `logs_cold` and `retention_config` are removed. OpenSearch is not adopted.
> - **Collection via OpenTelemetry.** The custom `kitsune-kube-agent` and `POST /ingest/metrics/kube` are replaced by the upstream **OpenTelemetry Collector**: kubelet + k8s cluster receivers, Prometheus `/metrics` scraping, OTLP push, and `filelog` for container stdout/stderr. No Prometheus server/TSDB is deployed — Prometheus is used as a scrape format only.
> - **Instana / Turbonomic dropped from the core.** OTel covers the data Instana collected; Turbonomic-style rightsizing becomes a future feature built on Kitsune's own metrics. Both may return later as optional integrations.
> - **Alerting moved out of the consumer** into a dedicated `kitsune-alerter` that evaluates windowed rules against ClickHouse.
> - **Live tail reads Kafka**, not ClickHouse.

---

## High-Level Architecture
![Kitsune Architecture](./kitsune_architecture.svg)

```
User apps (JSON) ──► kitsune-ingestor ──► kitsune.logs.raw ─────┐
                                                                ├──► kitsune-consumer ──► ClickHouse ◄── kitsune-query ──► UI / Grafana
K8s workloads ──► OTel Collector ──► kitsune.logs.otlp ─────────┤                            ▲              ▲
 (stdout, /metrics,                  kitsune.metrics.otlp ──────┘                            │              │ live tail (Kafka)
  OTLP SDK, kubelet)                                                     kitsune-alerter ────┘              │
                                                                          │   ▲ rules                       │
                                                                          ▼   └── PostgreSQL ◄──────────────┘ rules CRUD
                                                               kitsune.alerts.events
```

---

## Services

### 1. `kitsune-ingestor`
**Language**: Go  
**Framework**: Fiber  
**Role**: HTTP API for applications that push structured JSON logs directly — typically services outside Kubernetes, or apps that want to send rich labels without an OTel SDK. Kubernetes workloads don't need it: their stdout is collected by the OTel Collector.

**Endpoints**:
```
POST /ingest/logs              ← accepts JSON array of log entries
GET  /health                   ← liveness probe
```

**Behavior**:
- Authenticates `Authorization: Bearer <api_token>` against the `services` table (cached in memory, refreshed every 60s)
- Per-token rate limit (in-memory token bucket) → `429` when exceeded
- Request body size limit (e.g. 5 MB) → `413`
- Validates entries (service, message, level), fills defaults (timestamp, `source = "kitsune"`)
- Publishes to `kitsune.logs.raw`, **keyed by `service`** so each service's logs stay ordered within a partition
- Returns `202 Accepted` once Kafka acks the produce; returns `503` if Kafka is unavailable (the client retries — the ingestor never buffers to disk)

**Log Payload Schema**:
```json
{
  "timestamp": "2026-03-27T10:00:00.000Z",
  "level": "ERROR",
  "service": "payment-service",
  "message": "connection refused to db",
  "trace_id": "abc123",
  "labels": {
    "env": "production",
    "region": "us-east-1"
  }
}
```

---

### 2. `kitsune-otel-collector`
**Image**: `otel/opentelemetry-collector-contrib` (upstream — no custom code)  
**Helm chart**: `open-telemetry/opentelemetry-collector`  
**Role**: Collects all Kubernetes logs, Kubernetes metrics, and application metrics, and publishes them to Kafka as OTLP protobuf.

Deployed as **two releases** so nothing is collected twice:

| Release | Mode | Receivers | Collects |
|---|---|---|---|
| `kitsune-otel-node` | DaemonSet (one per node) | `filelog`, `kubeletstats`, `prometheus` (node-local pods only), `otlp` | Container stdout/stderr from `/var/log/pods`; pod/container/node CPU, memory, network, fs from the local kubelet; Prometheus `/metrics` from pods on this node; OTLP logs/metrics pushed by apps |
| `kitsune-otel-cluster` | Deployment (1 replica) | `k8s_cluster` | Cluster state: container restarts, CPU/memory requests and limits, deployment desired vs. available, node conditions, pod phase |

**Pipelines**:
```
logs:     filelog, otlp                      → memory_limiter → k8sattributes → batch → kafka (kitsune.logs.otlp)
metrics:  kubeletstats, prometheus, otlp     → memory_limiter → k8sattributes → batch → kafka (kitsune.metrics.otlp)
          k8s_cluster (cluster release)      → memory_limiter → batch                 → kafka (kitsune.metrics.otlp)
```

**Container logs**: the chart's `logsCollection` preset configures `filelog` with the container-runtime parser (CRI/docker) and excludes the collector's own logs to avoid a feedback loop. `k8sattributes` adds namespace, pod, container, node, and deployment names.

**Prometheus scraping**: any pod annotated `prometheus.io/scrape: "true"` (+ optional `prometheus.io/port`, `prometheus.io/path`) is scraped every 15s. Each DaemonSet pod only discovers pods on its own node (`field: spec.nodeName=${env:K8S_NODE_NAME}`), so there are no duplicate scrapes.

**App telemetry via OTLP**: services instrumented with an OTel SDK push to `kitsune-otel-node:4317` (gRPC) / `:4318` (HTTP).

**Key metric names** (used by `/query/kube/*`, alert rules, and rightsizing):

| Metric | Receiver |
|---|---|
| `k8s.pod.cpu.usage`, `k8s.pod.memory.working_set` | kubeletstats |
| `container.cpu.usage`, `container.memory.working_set` | kubeletstats |
| `k8s.node.cpu.usage`, `k8s.node.memory.working_set` | kubeletstats |
| `k8s.container.cpu_request`, `k8s.container.memory_request`, `k8s.container.cpu_limit`, `k8s.container.memory_limit` | k8s_cluster |
| `k8s.container.restarts` | k8s_cluster |
| `k8s.deployment.desired`, `k8s.deployment.available` | k8s_cluster |
| `k8s.node.condition_memory_pressure`, `k8s.node.condition_disk_pressure` | k8s_cluster |

**RBAC**: the chart presets (`logsCollection`, `kubeletMetrics`, `clusterMetrics`, `kubernetesAttributes`) create the `ServiceAccount`, `ClusterRole`, and host-path mounts.

---

### 3. `kitsune-consumer`
**Language**: Go (`twmb/franz-go` for Kafka, `ClickHouse/clickhouse-go/v2` native batches)  
**Deployment**: Kubernetes Deployment (replicas ≤ partition count)  
**Role**: Reads the telemetry topics, decodes and normalizes them, and batch-writes to ClickHouse. **This is the most critical piece of Kitsune** — see [Where the complexity is](#where-the-complexity-is).

**Subscribes to**:
- `kitsune.logs.raw` — `KitsuneLog` JSON from the ingestor
- `kitsune.logs.otlp` — OTLP protobuf logs from the collector
- `kitsune.metrics.otlp` — OTLP protobuf metrics from the collector

**Normalization**:
- **OTLP logs** (`plog.ProtoUnmarshaler`) → `KitsuneLog`:
  - `service` = resource `service.name` → else `k8s.deployment.name` / `k8s.statefulset.name` / `k8s.daemonset.name` → else `k8s.container.name`
  - `level` = `SeverityText` normalized (`WARNING`→`WARN`, `FATAL`/`CRITICAL`→`ERROR`, `TRACE`→`DEBUG`); if empty, derived from `SeverityNumber`; if still empty, `INFO`
  - `message` = body as string; `trace_id` = hex trace ID if set
  - `labels` = k8s resource attributes (`k8s.namespace.name`, `k8s.pod.name`, `k8s.container.name`, `k8s.node.name`) + record attributes stringified
  - `source` = `otel`
- **OTLP metrics** (`pmetric.ProtoUnmarshaler`) → one `KitsuneMetric` per data point:
  - `labels` = resource attributes + data point attributes
  - `type` = `gauge` (Gauge, non-monotonic Sum), `counter` (monotonic Sum), `histogram` (expanded Prometheus-style into `<name>_count`, `<name>_sum`, `<name>_bucket{le=…}` rows)
  - `source` from the instrumentation scope: kubeletstats / k8s_cluster → `kube`, prometheus receiver → `prometheus`, anything else → `otlp`

**Delivery guarantees** (at-least-once, with insert dedup):
1. Poll records → decode → append to an in-memory batch per table
2. Flush when the batch hits 1000 rows **or** 1 second elapses
3. `INSERT` with `insert_deduplication_token = <topic>-<partition>-<firstOffset>-<lastOffset>` — a retried batch is dropped by ClickHouse instead of duplicated
4. **Only after ClickHouse acks**, commit the Kafka offsets for that batch
5. A crash between insert and commit can replay records with different batch boundaries → rare duplicates. This is accepted.

**Failure handling**:
- **Bad records** (undecodable, failed validation) → published to `kitsune.deadletter` with the error and source topic/offset in headers. One bad record never blocks a partition.
- **ClickHouse unavailable** → retry with exponential backoff (1s → 30s cap) and **pause** the partitions so memory doesn't grow. Kafka's 48h retention is the outage budget.
- **Lag** (`kafka_consumergroup_lag`) is exported as a metric and is the primary health signal.
- Writes everything regardless of record age — tiering is ClickHouse's job (TTL moves).

**Config (env vars)**:
```
KAFKA_BROKERS=kafka:9092
KAFKA_GROUP_ID=kitsune-consumer
CLICKHOUSE_DSN=clickhouse://user:pass@clickhouse:9000/kitsune
BATCH_MAX_ROWS=1000
BATCH_MAX_WAIT_MS=1000
```

---

### 4. `kitsune-alerter`
**Language**: Go  
**Deployment**: Kubernetes Deployment, 1 active replica (leader lock via `pg_try_advisory_lock`)  
**Role**: Evaluates alert rules on a schedule against ClickHouse and emits alert events.

**Why a separate service**: evaluating rules inside the consumer on each batch can't express windows ("avg CPU > 80% **for 5 minutes**"). A batch only contains a few seconds of one partition's data. Querying ClickHouse over the window sees all data, regardless of partition or consumer replica.

**Evaluation loop** (every `EVAL_INTERVAL_SECONDS`, default 15s):
1. Load enabled rules from PostgreSQL (cached, refreshed every 30s)
2. For each rule, run one aggregate query:
   ```sql
   SELECT {aggregation}(value)            -- avg | max | min | sum | count | quantile(0.95)
   FROM kitsune.metrics
   WHERE name = {metric_name}
     AND ({source} = '' OR source = {source})
     AND labels[{k}] = {v} ...           -- from label_filters
     AND timestamp >= now64(3) - INTERVAL {window_secs} SECOND
   ```
3. Apply `condition` / `threshold` and advance the rule's state machine:
   ```
   inactive ──(true)──► pending ──(true for ≥ for_secs)──► firing ──(false)──► resolved → inactive
                │                                                  
                └──(false)──► inactive
   ```
4. **On firing**: insert an `alert_history` row (`resolved_at = NULL`) and publish an `AlertEvent{status: "firing"}` to `kitsune.alerts.events`
5. **On resolve**: insert the same `id` again with `resolved_at` set and a higher `version`, and publish `status: "resolved"`

**Restarts**: on startup, firing state is rebuilt from `alert_history FINAL WHERE resolved_at IS NULL`. Pending state is not persisted — the `for_secs` clock restarts, which only delays a firing.

**Scope for v1**: one aggregate per rule across all matching series. Per-series alerting (`GROUP BY labels['k8s.pod.name']`) is a later extension.

**Config (env vars)**:
```
CLICKHOUSE_DSN=clickhouse://user:pass@clickhouse:9000/kitsune
POSTGRES_DSN=postgres://user:pass@postgres:5432/kitsune
KAFKA_BROKERS=kafka:9092
EVAL_INTERVAL_SECONDS=15
```

---

### 5. `kitsune-query`
**Language**: Go  
**Framework**: Fiber  
**Role**: REST API that the UI and Grafana query for logs, metrics, and alert data. Also owns alert rule CRUD.

**Endpoints**:
```
GET  /query/logs
     ?service=<name>
     &level=ERROR|WARN|INFO|DEBUG
     &q=<full-text search>
     &labels[k8s.namespace.name]=production
     &from=<unix ms>
     &to=<unix ms>
     &limit=100
     &cursor=<opaque>              ← keyset pagination

GET  /query/logs/stream            ← SSE live tail
     ?service=<name>
     &level=<level>

GET  /query/metrics
     ?name=<metric name>
     &source=kube|prometheus|otlp
     &from=<unix ms>
     &to=<unix ms>
     &step=<seconds>               ← server-side downsampling
     &labels[k8s.namespace.name]=production

GET  /query/kube/pods              ← current pod health snapshot
GET  /query/kube/nodes             ← current node resource summary

GET  /alerts/rules                 ← list alert rules
POST /alerts/rules                 ← create alert rule
PUT  /alerts/rules/:id             ← update alert rule
DELETE /alerts/rules/:id           ← delete alert rule
GET  /alerts/history               ← fired alerts log

GET  /health                       ← liveness probe
```

**Query sources**:
- Logs, metrics, kube snapshots, alert history (all ages) → ClickHouse. Tiers are transparent; old data on S3 is just slower.
- Alert rules → PostgreSQL
- Live tail → Kafka

**Query notes**:
- **Use the sort key.** Every log query must carry a time range, and should carry `service` when possible. `ORDER BY (service, level, timestamp)` lets ClickHouse skip granules rather than scan.
- **Pagination** is keyset on `(timestamp, cursor)`, not `OFFSET`. `OFFSET` gets slower the deeper you page.
- **`q=` full-text search** uses `hasToken(lower(message), …)` (token index) for whole words and `lower(message) LIKE '%…%'` (ngram index) for substrings. There is no relevance ranking; results are ordered `timestamp DESC`.
- **`/query/metrics`** downsamples with `toStartOfInterval(timestamp, INTERVAL {step} SECOND)` + `avg(value)`, so charts never pull raw points.
- **`/query/kube/pods`** and **`/query/kube/nodes`** use `argMax(value, timestamp)` over the last 5 minutes where `source = 'kube'`, grouped by `labels['k8s.pod.name']` / `labels['k8s.node.name']`.
- **`/alerts/history`** reads `alert_history FINAL` so each alert shows its latest state.
- Every ClickHouse query sets `max_execution_time` and `max_rows_to_read` so one bad query can't take the cluster down.

**Live tail**: ClickHouse has no change feed, and polling it every second is wasteful. Instead, each `kitsune-query` instance runs a Kafka consumer:
- It reads `kitsune.logs.raw` and `kitsune.logs.otlp`, starting at the **latest** offset, with a unique group ID per instance (`kitsune-query-tail-<pod>`), so every instance sees every log.
- The consumer only runs while at least one SSE client is connected.
- Each client gets a bounded channel. Matching uses the same filters as `/query/logs`. A slow client gets messages dropped plus a `{"dropped": n}` event, rather than causing backpressure.

---

## Kafka Topics

| Topic | Producer | Consumer | Key | Retention |
|---|---|---|---|---|
| `kitsune.logs.raw` | kitsune-ingestor | kitsune-consumer, kitsune-query (tail) | `service` | 48 hours |
| `kitsune.logs.otlp` | kitsune-otel-collector | kitsune-consumer, kitsune-query (tail) | — | 48 hours |
| `kitsune.metrics.otlp` | kitsune-otel-collector | kitsune-consumer | — | 48 hours |
| `kitsune.alerts.events` | kitsune-alerter | (notifier, future) | `rule_id` | 7 days |
| `kitsune.deadletter` | kitsune-consumer | humans / replay tool | source topic | 7 days |

Local dev uses 1 partition per topic. In prod, start telemetry topics at 6 partitions; consumer replicas scale up to the partition count.

---

## Storage Design

| Store | Role | Holds | Why |
|---|---|---|---|
| **ClickHouse** | Primary store (OLAP) | `logs`, `metrics`, `alert_history` — all ages | Append-only, high-volume, time-ordered data; 10–20× compression; sub-second aggregations; native TTL tiering |
| **PostgreSQL** | Config store (OLTP) | `services`, `alert_rules` | Tiny, frequently edited rows that need uniqueness and in-place `UPDATE`/`DELETE` — exactly what ClickHouse is bad at |
| ~~OpenSearch~~ | Not used | — | Kitsune needs filtered log search, not relevance-ranked search. ClickHouse token/ngram skip indexes cover `q=`. Revisit only if fuzzy/stemmed/ranked search becomes a requirement |

### Tiering (one ClickHouse cluster, multiple disks)

Tiering is done with a ClickHouse **storage policy**, not separate databases. Inserts land on `hot`, and a background task moves whole parts down the tiers by TTL. Queries are unaware of tiers.

| Tier | Logs | Metrics | Disk (prod) | Disk (local dev) |
|---|---|---|---|---|
| hot | 0–3 days | 0–7 days | NVMe / gp3 PVC | `/bitnami/clickhouse/hot/` |
| warm | 3–30 days | 7–30 days | st1 / cheaper PVC | `/bitnami/clickhouse/warm/` |
| cold | 30–365 days | 30–365 days | S3 disk + local cache (still queryable) | `/bitnami/clickhouse/cold/` |
| delete | > 365 days | > 365 days | — | — |

Retention changes are made with `ALTER TABLE ... MODIFY TTL` via migration.

**Prod storage configuration** (`config.d/storage.xml`):
```xml
<clickhouse>
  <storage_configuration>
    <disks>
      <hot><path>/var/lib/clickhouse/hot/</path></hot>
      <warm><path>/var/lib/clickhouse/warm/</path></warm>
      <cold_s3>
        <type>s3</type>
        <endpoint>https://s3.amazonaws.com/kitsune-ch/data/</endpoint>
        <use_environment_credentials>true</use_environment_credentials>
      </cold_s3>
      <cold>
        <type>cache</type>
        <disk>cold_s3</disk>
        <path>/var/lib/clickhouse/s3_cache/</path>
        <max_size>50Gi</max_size>
      </cold>
    </disks>
    <policies>
      <tiered>
        <volumes>
          <hot><disk>hot</disk></hot>
          <warm><disk>warm</disk></warm>
          <cold><disk>cold</disk></cold>
        </volumes>
        <move_factor>0.1</move_factor>
      </tiered>
    </policies>
  </storage_configuration>
</clickhouse>
```

### ClickHouse Schema

```sql
CREATE DATABASE IF NOT EXISTS kitsune;

-- Logs (all ages; tiered hot → warm → cold, deleted after 365d)
CREATE TABLE kitsune.logs (
    timestamp  DateTime64(3),
    service    LowCardinality(String),
    level      LowCardinality(String),
    message    String CODEC(ZSTD(3)),
    trace_id   String,
    labels     Map(LowCardinality(String), String),
    source     LowCardinality(String) DEFAULT 'kitsune',   -- kitsune | otel
    INDEX idx_trace      trace_id            TYPE bloom_filter(0.01)          GRANULARITY 1,
    INDEX idx_msg_token  lower(message)      TYPE tokenbf_v1(32768, 3, 0)     GRANULARITY 1,
    INDEX idx_msg_ngram  lower(message)      TYPE ngrambf_v1(4, 65536, 3, 0)  GRANULARITY 1,
    INDEX idx_label_keys mapKeys(labels)     TYPE bloom_filter(0.01)          GRANULARITY 1,
    INDEX idx_label_vals mapValues(labels)   TYPE bloom_filter(0.01)          GRANULARITY 1
) ENGINE = MergeTree()
PARTITION BY toDate(timestamp)
ORDER BY (service, level, timestamp)
TTL toDateTime(timestamp) + INTERVAL 3 DAY   TO VOLUME 'warm',
    toDateTime(timestamp) + INTERVAL 30 DAY  TO VOLUME 'cold',
    toDateTime(timestamp) + INTERVAL 365 DAY DELETE
SETTINGS storage_policy = 'tiered',
         index_granularity = 8192,
         non_replicated_deduplication_window = 1000;   -- enables insert_deduplication_token

-- Metrics (all sources; tiered, deleted after 365d)
CREATE TABLE kitsune.metrics (
    timestamp  DateTime64(3) CODEC(DoubleDelta, ZSTD),
    name       LowCardinality(String),
    value      Float64 CODEC(Gorilla, ZSTD),
    type       LowCardinality(String),
    labels     Map(LowCardinality(String), String),
    source     LowCardinality(String),                    -- kube | prometheus | otlp
    INDEX idx_label_vals mapValues(labels) TYPE bloom_filter(0.01) GRANULARITY 1
) ENGINE = MergeTree()
PARTITION BY toDate(timestamp)
ORDER BY (source, name, timestamp)
TTL toDateTime(timestamp) + INTERVAL 7 DAY   TO VOLUME 'warm',
    toDateTime(timestamp) + INTERVAL 30 DAY  TO VOLUME 'cold',
    toDateTime(timestamp) + INTERVAL 365 DAY DELETE
SETTINGS storage_policy = 'tiered',
         index_granularity = 8192,
         non_replicated_deduplication_window = 1000;

-- Alert history (append-only; resolving an alert = inserting a new version of the same row)
CREATE TABLE kitsune.alert_history (
    id           UUID,
    rule_id      UUID,
    rule_name    LowCardinality(String),
    metric_name  LowCardinality(String),
    source       LowCardinality(String),
    condition    LowCardinality(String),
    threshold    Float64,
    value        Float64,
    fired_at     DateTime64(3),
    resolved_at  Nullable(DateTime64(3)),
    version      UInt64                      -- unix ms of the write; highest wins
) ENGINE = ReplacingMergeTree(version)
PARTITION BY toYYYYMM(fired_at)
ORDER BY (rule_id, fired_at, id)
TTL toDateTime(fired_at) + INTERVAL 365 DAY DELETE;
```

Rule details (`rule_id`, `rule_name`, …) are **denormalized** into `alert_history` at fire time. ClickHouse has no FKs, and history should survive rule edits and deletes.

**Schema decisions that are expensive to change later:**
- `logs ORDER BY (service, level, timestamp)` assumes most searches filter by service. If namespace-first browsing turns out to dominate, add a **projection** ordered by `(labels['k8s.namespace.name'], timestamp)` rather than rebuilding the table.
- Labels live in a `Map`. Labels that are queried often (namespace, pod) can later be promoted to real `LowCardinality` columns via `MATERIALIZED labels['…']` for faster filtering.

---

## PostgreSQL Schema

Config only — small, mutable, relational. No telemetry lives here.

```sql
-- Service registry (ingestor auth + rate limiting)
CREATE TABLE services (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT UNIQUE NOT NULL,
    api_token   TEXT UNIQUE NOT NULL,
    created_at  TIMESTAMPTZ DEFAULT NOW()
);

-- Alert rules (CRUD via kitsune-query; evaluated by kitsune-alerter)
CREATE TABLE alert_rules (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name           TEXT NOT NULL,
    metric_name    TEXT NOT NULL,
    source         TEXT NOT NULL DEFAULT '',          -- kube/prometheus/otlp, '' = any
    label_filters  JSONB NOT NULL DEFAULT '{}',       -- e.g. {"k8s.namespace.name": "production"}
    aggregation    TEXT NOT NULL DEFAULT 'avg',       -- avg/max/min/sum/count/p95
    condition      TEXT NOT NULL,                     -- gt/lt/eq
    threshold      FLOAT NOT NULL,
    window_secs    INT NOT NULL DEFAULT 60,           -- aggregation window
    for_secs       INT NOT NULL DEFAULT 0,            -- must stay true this long before firing
    enabled        BOOLEAN DEFAULT TRUE,
    created_at     TIMESTAMPTZ DEFAULT NOW(),
    updated_at     TIMESTAMPTZ DEFAULT NOW()
);
```

Removed vs v0.0.5: `logs_cold` (replaced by the ClickHouse cold tier), `alert_history` (moved to ClickHouse), `retention_config` (replaced by table TTLs).

---

## Internal Data Model

All services normalize data into these shared structs (defined in `internal/models`):

```go
// KitsuneLog — canonical log entry
type KitsuneLog struct {
    Timestamp time.Time         `json:"timestamp"`
    Service   string            `json:"service"`
    Level     string            `json:"level"`       // INFO, WARN, ERROR, DEBUG
    Message   string            `json:"message"`
    TraceID   string            `json:"trace_id"`
    Labels    map[string]string `json:"labels"`
    Source    string            `json:"source"`      // kitsune (ingestor), otel (collector)
}

// KitsuneMetric — canonical metric entry
type KitsuneMetric struct {
    Timestamp time.Time         `json:"timestamp"`
    Name      string            `json:"name"`        // e.g. "k8s.pod.cpu.usage"
    Value     float64           `json:"value"`
    Type      string            `json:"type"`        // gauge, counter, histogram
    Labels    map[string]string `json:"labels"`      // k8s.pod.name, k8s.namespace.name, etc.
    Source    string            `json:"source"`      // kube, prometheus, otlp
}
```

---

## Where the complexity is

OTel removes the work of *collecting* telemetry. Kitsune's engineering is everything after Kafka. Ranked by difficulty:

| # | Area | What makes it hard |
|---|---|---|
| 1 | **Consumer** | Commit offsets only after ClickHouse acks; dedup tokens; dead-letter without blocking partitions; pause/backoff when ClickHouse is down; OTLP → flat rows (histograms, attribute merging, service-name resolution); scaling with partitions |
| 2 | **Alerting** | Windowed evaluation, pending/firing/resolved state, `for` durations to avoid flapping, rebuilding state after restart, single active evaluator, later per-series alerts and notifications |
| 3 | **Query layer** | Translating filters into SQL that hits the sort key and skip indexes; keyset pagination; downsampling; query guards; live tail fan-out from Kafka with slow-client handling |
| 4 | **Schema design** | `ORDER BY` keys and label layout are expensive to change once data exists; they must match real query patterns |
| 5 | **ClickHouse operations** | S3 tiering and cache sizing, backups, monitoring part counts and merges, replication later |
| 6 | **Ingestor** | Comparatively simple: auth, rate limits, size limits, validation, Kafka produce |

**Build order follows from this**: get one log flowing end-to-end through the consumer into ClickHouse as early as possible. That surfaces the hard problems first.

---

## Repository Structure

```
kitsune/
├── cmd/
│   ├── ingestor/             # kitsune-ingestor entrypoint
│   ├── consumer/             # kitsune-consumer entrypoint
│   ├── alerter/              # kitsune-alerter entrypoint
│   └── query/                # kitsune-query entrypoint
│
├── internal/
│   ├── models/               # shared KitsuneLog, KitsuneMetric, alert structs
│   ├── kafka/                # shared producer/consumer helpers (franz-go)
│   ├── otlp/                 # OTLP logs/metrics → KitsuneLog/KitsuneMetric decoders
│   ├── clickhouse/           # ClickHouse client, batch writer, query builders
│   ├── postgres/             # PostgreSQL client + queries
│   ├── config/               # env var parsing (shared)
│   └── alert/                # rule evaluation + state machine
│
├── kitsune-ui/               # React + TypeScript frontend
│   ├── src/
│   │   ├── pages/
│   │   │   ├── LogExplorer.tsx
│   │   │   ├── MetricsDashboard.tsx
│   │   │   ├── KubeOverview.tsx
│   │   │   └── Alerting.tsx
│   │   ├── components/
│   │   │   ├── LogTable.tsx
│   │   │   ├── TimeSeriesChart.tsx
│   │   │   ├── PodGrid.tsx
│   │   │   └── AlertRuleEditor.tsx
│   │   ├── api/
│   │   │   └── kitsuneClient.ts
│   │   └── hooks/
│   │       ├── useLogs.ts
│   │       └── useMetrics.ts
│   └── package.json
│
├── infra/
│   ├── terraform/
│   │   └── local/            # minikube stack (see docs/local-dev.md)
│   └── helm/
│       ├── kitsune-ingestor/
│       ├── kitsune-consumer/
│       ├── kitsune-alerter/
│       ├── kitsune-query/
│       └── otel-collector/   # values for kitsune-otel-node (DaemonSet) + kitsune-otel-cluster (Deployment)
│
├── docs/
│   └── local-dev.md
├── go.mod
└── go.sum
```

---

## Local Dev Stack

Provisioned on minikube with Terraform + Helm — see [`docs/local-dev.md`](../docs/local-dev.md).

| Service | Port | Purpose |
|---|---|---|
| Kafka (KRaft mode) | 9092 | Message transport |
| ClickHouse | 8123, 9000 | All telemetry (hot/warm/cold tiers on local disks) |
| PostgreSQL | 5432 | Config: service tokens, alert rules |
| Grafana | 3000 | Dashboard UI |
| OTel Collector (node + cluster) | 4317, 4318 | Container logs, kube metrics, Prometheus scraping, OTLP → Kafka |

---

## UI Tech Stack (`kitsune-ui`)

| Concern | Library |
|---|---|
| Framework | React 18 + TypeScript (Vite) |
| Charts | uplot (fast, lightweight time-series) |
| Styling | Tailwind CSS |
| State management | Zustand |
| Data fetching | TanStack Query |
| Tables (log rows) | TanStack Table (virtualized) |
| Live tail | SSE via native EventSource API |

---

## Build Phases

### Phase 1 — Logs end-to-end (the hard path first)
- [ ] `kitsune-ingestor`: Kafka produce to `kitsune.logs.raw` (keyed by service), bearer-token auth, rate + size limits
- [ ] `kitsune-consumer`: `kitsune.logs.raw` → ClickHouse batch writer with commit-after-insert, dedup token, dead-letter topic, backoff/pause
- [ ] `kitsune-query`: `GET /query/logs` with filters, `q=` search, keyset pagination
- [ ] Wire Grafana → ClickHouse datasource
- [ ] Failure drills: kill ClickHouse mid-load, send malformed records, restart consumer — verify no loss, few duplicates

### Phase 2 — OpenTelemetry collection
- [ ] Deploy OTel Collector (node DaemonSet + cluster Deployment) → Kafka
- [ ] `kitsune-consumer`: OTLP logs decoder (`kitsune.logs.otlp`) — service/level resolution
- [ ] `kitsune-consumer`: OTLP metrics decoder (`kitsune.metrics.otlp`) — histogram expansion
- [ ] `kitsune-query`: `GET /query/metrics` with downsampling, `/query/kube/*`
- [ ] Annotate a sample app with `prometheus.io/scrape` and verify it lands in ClickHouse

### Phase 3 — Alerting
- [ ] Alert rules CRUD in PostgreSQL (`/alerts/rules`)
- [ ] `kitsune-alerter`: windowed evaluation, state machine, advisory-lock leadership
- [ ] Fires/resolves to `kitsune.alerts.events` + ClickHouse `alert_history`
- [ ] State rebuild on restart

### Phase 4 — Live tail + UI
- [ ] `GET /query/logs/stream` (SSE from Kafka, per-instance group, bounded channels)
- [ ] `kitsune-ui` scaffold (Vite + React + TS + Tailwind)
- [ ] Log Explorer, Metrics Dashboard, Kube Overview, Alerting pages

### Phase 5 — Helm + Terraform
- [ ] Helm chart per service
- [ ] Terraform for EKS cluster + networking
- [ ] ClickHouse via Altinity operator: hot/warm PVCs from separate StorageClasses, S3 cold disk via IRSA
- [ ] CI/CD pipeline (GitHub Actions)

### Future
- [ ] **Rightsizing recommendations** (Turbonomic-lite): compare each container's p95 usage over 7 days with its requests:
  ```sql
  SELECT labels['k8s.pod.name'] AS pod, labels['k8s.container.name'] AS container,
         quantileIf(0.95)(value, name = 'container.cpu.usage')  AS cpu_p95,
         maxIf(value, name = 'k8s.container.cpu_request')       AS cpu_request
  FROM kitsune.metrics
  WHERE source = 'kube' AND timestamp >= now() - INTERVAL 7 DAY
  GROUP BY pod, container
  HAVING cpu_request > 0 AND cpu_p95 < cpu_request * 0.5     -- over-provisioned
  ```
  Same pattern for memory. Surface as a UI page and optionally as alert rules.
- [ ] **Traces**: OTLP traces → `kitsune.traces.otlp` → ClickHouse `traces` table, linked from logs via `trace_id`
- [ ] **Alert notifications**: consumer of `kitsune.alerts.events` → Slack / webhook / email
- [ ] **Per-series alerting**: `GROUP BY` a label in rule evaluation
- [ ] **Optional integrations**: Instana / Turbonomic pollers publishing to the same topics, for environments that already run them

---

## Key Design Decisions

**Why Kafka?**
It's a durable buffer between collection and storage. It absorbs traffic spikes, gives a 48h replay window when ClickHouse is down, and lets multiple independent readers (storage consumer, live tail) see the same stream.

**Why the OpenTelemetry Collector?**
It already ships receivers for container logs, the kubelet, the Kubernetes API, Prometheus `/metrics`, and OTLP. They're maintained upstream, and the Helm chart presets handle RBAC and node-local discovery. A custom agent would be pure maintenance cost. It also gives apps a standard way in (OTel SDK, Prometheus endpoint, or plain stdout) instead of a Kitsune-specific API.

**Why keep the HTTP ingestor if OTel collects stdout?**
For apps outside Kubernetes, and for apps that want to send structured entries with explicit `service`, `level`, and labels without adopting an OTel SDK. It's small and already written.

**Why not a Prometheus server?**
Prometheus is used as a *format*: the collector scrapes `/metrics`. It is not used as a database. Running the Prometheus TSDB would split metrics across two stores, with weaker high-cardinality handling and short default retention. ClickHouse keeps metrics next to logs, with the same tiering and SQL.

**Why export OTLP to Kafka instead of using the collector's ClickHouse exporter?**
The `clickhouseexporter` writes directly. That bypasses Kafka (no spike buffer, no replay, no live tail source) and imposes OTel's own multi-table schema. Exporting to Kafka keeps one pipeline and one normalized schema.

**Why drop Instana / Turbonomic from the core?**
OTel already collects the metrics Instana provided. What Instana adds on top (auto-discovery, dashboards, incident detection) is exactly what Kitsune is building. Turbonomic's unique value is rightsizing and placement recommendations, and a useful subset of that can be computed from Kitsune's own request-vs-usage metrics (see Future). Both can come back as optional integrations.

**Why a separate alerter instead of evaluating in the consumer?**
A consumer batch holds seconds of data from one partition, so it can't evaluate "for 5 minutes" conditions or see data handled by other consumer replicas. Periodic queries over ClickHouse see the full window and all sources. They also keep the consumer focused on fast, reliable writes.

**Why live tail from Kafka instead of ClickHouse?**
ClickHouse has no change stream, so a tail would mean polling with `timestamp > last_seen` every second per client. That costs a lot and still has latency gaps. Kafka already carries every log in order, and a per-instance consumer from the latest offset gives sub-second tails at no extra storage cost.

**Why ClickHouse as the single telemetry store?**
Logs, metrics, and alert history are append-only, time-ordered, and queried by scanning and aggregating — the OLAP sweet spot. Columnar storage compresses logs 10–20×, and one storage policy handles hot → warm → cold (S3) with TTL moves while keeping old data directly queryable.

**Why keep PostgreSQL at all?**
Service tokens and alert rules are tiny, mutable, CRUD-shaped rows that need unique constraints and in-place updates. ClickHouse has no unique keys and treats updates as part rewrites. Postgres also provides the advisory lock for alerter leadership. It holds no telemetry.

**Why not OpenSearch / Elasticsearch?**
Its strength is relevance-ranked full-text search (fuzzy matching, stemming, synonyms). Kitsune's log search is filter + keyword + time range, which ClickHouse token/ngram skip indexes handle. OpenSearch would mean a second copy of every log, JVM and shard operations, and higher cost — the problems Didi, Trip.com, Uber, and Cloudflare moved off Elasticsearch to escape. Revisit only if ranked or fuzzy search becomes a requirement.

**Why batch inserts?**
Each ClickHouse `INSERT` creates a part, and tiny frequent inserts cause "too many parts". Kafka plus the consumer's 1000-row / 1s batching handles this.

**Why a single normalized schema?**
Every source (ingestor JSON, OTLP logs, OTLP metrics) is normalized into `KitsuneLog` / `KitsuneMetric` before storage. The query layer and alerter only deal with two tables, whatever the source.
