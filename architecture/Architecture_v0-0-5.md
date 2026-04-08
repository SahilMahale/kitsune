# Kitsune — Architecture Document

Kitsune is a self-hosted observability platform that aggregates logs, Kubernetes metrics, and external APM metrics (Instana/Turbonomic) into a unified storage and query layer.

---

## High-Level Architecture
![Kitsune Architecture](./kitsune_architecture.svg)

---

## Services

### 1. `kitsune-ingestor`
**Language**: Go  
**Framework**: Fiber  
**Role**: HTTP API that receives pushed data from user applications and the kube agent.

**Endpoints**:
```
POST /ingest/logs              ← accepts JSON array of log entries
POST /ingest/metrics/kube      ← accepts JSON array of kube metric entries
GET  /health                   ← liveness probe
```

**Behavior**:
- Validates and parses incoming payloads
- Publishes to the appropriate Kafka topic
- Returns 202 Accepted — fire and forget, no write confirmation
- Rate limiting per service token (stored in PostgreSQL)

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

**Kube Metric Payload Schema**:
```json
{
  "timestamp": "2026-03-27T10:00:00.000Z",
  "name": "pod_cpu_usage",
  "value": 0.45,
  "type": "gauge",
  "labels": {
    "pod": "payment-service-abc123",
    "namespace": "production",
    "node": "node-1",
    "source": "kube"
  }
}
```

---

### 2. `kitsune-kube-agent`
**Language**: Go  
**Deployment**: Kubernetes Deployment (single replica per cluster)  
**Role**: Scrapes the Kubernetes Metrics API and pushes to `kitsune-ingestor`.

**Collects**:
- Pod CPU and memory usage (`metrics.k8s.io/v1beta1`)
- Node resource usage and pressure conditions
- Container restart counts
- Deployment replica status (desired vs. ready)

**Behavior**:
- Scrapes every 15 seconds
- Normalizes into `KitsuneMetric` schema
- POSTs batches to `kitsune-ingestor /ingest/metrics/kube`
- Requires a `ServiceAccount` with `ClusterRole` read access to metrics API

**Config (env vars)**:
```
KITSUNE_INGESTOR_URL=http://kitsune-ingestor:8080
SCRAPE_INTERVAL_SECONDS=15
KUBE_NAMESPACE=production        # leave empty to scrape all namespaces
```

---

### 3. `kitsune-instana-collector`
**Language**: Go  
**Deployment**: Kubernetes Deployment (single replica)  
**Role**: Polls the Instana REST API periodically and publishes normalized metrics to Kafka.

**Polls**:
```
GET /api/metrics/applications     every 30s
GET /api/infrastructure/metrics   every 30s
GET /api/events                   every 30s
```

**Behavior**:
- Authenticates via `apiToken` header
- Normalizes response into `KitsuneMetric` schema with `labels.source = "instana"`
- Publishes to Kafka topic `kitsune.metrics.instana`

**Config (env vars)**:
```
INSTANA_BASE_URL=https://<tenant>.instana.io
INSTANA_API_TOKEN=<token>
POLL_INTERVAL_SECONDS=30
KAFKA_BROKERS=kafka:9092
```

---

### 4. `kitsune-turbo-collector`
**Language**: Go  
**Deployment**: Kubernetes Deployment (single replica)  
**Role**: Polls the Turbonomic REST API and publishes normalized metrics to Kafka.

**Polls**:
```
GET /api/v3/markets/{id}/stats    every 60s
GET /api/v3/entities              every 60s
GET /api/v3/actions               every 60s
```

**Behavior**:
- Authenticates via session token
- Normalizes into `KitsuneMetric` schema with `labels.source = "turbonomic"`
- Publishes to Kafka topic `kitsune.metrics.turbonomic`

**Config (env vars)**:
```
TURBO_BASE_URL=https://<turbonomic-host>
TURBO_USERNAME=<username>
TURBO_PASSWORD=<password>
POLL_INTERVAL_SECONDS=60
KAFKA_BROKERS=kafka:9092
```

---

### 5. `kitsune-consumer`
**Language**: Go  
**Deployment**: Kubernetes Deployment  
**Role**: Reads all Kafka topics, enriches data, and writes to ClickHouse (hot) or PostgreSQL (cold/metadata).

**Subscribes to**:
- `kitsune.logs.raw`
- `kitsune.metrics.kube`
- `kitsune.metrics.instana`
- `kitsune.metrics.turbonomic`

**Behavior**:
- Uses consumer groups (one group per topic)
- Batches writes: flush every 1000 rows OR every 1 second, whichever comes first
- Logs older than 30 days are written to PostgreSQL instead of ClickHouse
- Evaluates alert rules stored in PostgreSQL on every metric batch
- Fires matched alerts to `kitsune.alerts.events` Kafka topic

**Config (env vars)**:
```
KAFKA_BROKERS=kafka:9092
KAFKA_GROUP_ID=kitsune-consumer
CLICKHOUSE_DSN=clickhouse://user:pass@clickhouse:9000/kitsune
POSTGRES_DSN=postgres://user:pass@postgres:5432/kitsune
HOT_LOG_RETENTION_DAYS=30
```

---

### 6. `kitsune-query`
**Language**: Go  
**Framework**: Fiber  
**Role**: REST API that the UI and Grafana query for logs, metrics, and alert data.

**Endpoints**:
```
GET  /query/logs
     ?service=<name>
     &level=ERROR|WARN|INFO
     &q=<full-text search>
     &from=<unix ms>
     &to=<unix ms>
     &limit=100

GET  /query/logs/stream            ← SSE live tail
     ?service=<name>
     &level=<level>

GET  /query/metrics
     ?name=<metric name>
     &source=instana|turbonomic|kube
     &from=<unix ms>
     &to=<unix ms>
     &labels[namespace]=production

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
- Hot data (< 30 days logs, all metrics) → ClickHouse
- Cold data (> 30 days logs) → PostgreSQL
- Alert rules and config → PostgreSQL

---

## Kafka Topics

| Topic | Producer | Consumer | Retention |
|---|---|---|---|
| `kitsune.logs.raw` | kitsune-ingestor | kitsune-consumer | 48 hours |
| `kitsune.metrics.kube` | kitsune-ingestor | kitsune-consumer | 48 hours |
| `kitsune.metrics.instana` | kitsune-instana-collector | kitsune-consumer | 48 hours |
| `kitsune.metrics.turbonomic` | kitsune-turbo-collector | kitsune-consumer | 48 hours |
| `kitsune.alerts.events` | kitsune-consumer | (webhook handler, future) | 7 days |

All topics use **single partition** initially. Scale to multiple partitions when throughput requires it.

---

## ClickHouse Schema

```sql
CREATE DATABASE IF NOT EXISTS kitsune;

-- Logs (hot, last 30 days)
CREATE TABLE kitsune.logs (
    timestamp  DateTime64(3),
    service    LowCardinality(String),
    level      LowCardinality(String),
    message    String,
    trace_id   String,
    labels     Map(String, String),
    source     LowCardinality(String) DEFAULT 'kitsune',
    INDEX idx_service service TYPE bloom_filter GRANULARITY 1,
    INDEX idx_trace   trace_id TYPE bloom_filter GRANULARITY 1
) ENGINE = MergeTree()
PARTITION BY toDate(timestamp)
ORDER BY (service, level, timestamp)
TTL toDate(timestamp) + INTERVAL 30 DAY
SETTINGS index_granularity = 8192;

-- Metrics (all sources, 90 day TTL)
CREATE TABLE kitsune.metrics (
    timestamp  DateTime64(3),
    name       LowCardinality(String),
    value      Float64,
    type       LowCardinality(String),
    labels     Map(String, String),
    source     LowCardinality(String)
) ENGINE = MergeTree()
PARTITION BY toDate(timestamp)
ORDER BY (source, name, timestamp)
TTL toDate(timestamp) + INTERVAL 90 DAY
SETTINGS index_granularity = 8192;
```

---

## PostgreSQL Schema

```sql
-- Cold log archive (logs older than 30 days)
CREATE TABLE logs_cold (
    id          BIGSERIAL PRIMARY KEY,
    timestamp   TIMESTAMPTZ NOT NULL,
    service     TEXT NOT NULL,
    level       TEXT NOT NULL,
    message     TEXT NOT NULL,
    trace_id    TEXT,
    labels      JSONB,
    source      TEXT DEFAULT 'kitsune',
    archived_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX idx_logs_cold_service_ts ON logs_cold (service, timestamp DESC);

-- Service registry
CREATE TABLE services (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT UNIQUE NOT NULL,
    api_token   TEXT UNIQUE NOT NULL,
    created_at  TIMESTAMPTZ DEFAULT NOW()
);

-- Alert rules
CREATE TABLE alert_rules (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name         TEXT NOT NULL,
    metric_name  TEXT NOT NULL,
    source       TEXT,                      -- instana/turbonomic/kube/any
    condition    TEXT NOT NULL,             -- gt/lt/eq
    threshold    FLOAT NOT NULL,
    window_secs  INT NOT NULL DEFAULT 60,
    enabled      BOOLEAN DEFAULT TRUE,
    created_at   TIMESTAMPTZ DEFAULT NOW()
);

-- Alert history
CREATE TABLE alert_history (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    rule_id      UUID REFERENCES alert_rules(id),
    fired_at     TIMESTAMPTZ DEFAULT NOW(),
    value        FLOAT NOT NULL,
    resolved_at  TIMESTAMPTZ
);

-- Retention config
CREATE TABLE retention_config (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hot_log_days         INT DEFAULT 30,
    hot_metric_days      INT DEFAULT 90,
    updated_at           TIMESTAMPTZ DEFAULT NOW()
);
```

---

## Internal Data Model

All services normalize data into these shared structs (defined in a shared `internal/models` package):

```go
// KitsuneLog — canonical log entry
type KitsuneLog struct {
    Timestamp time.Time         `json:"timestamp"`
    Service   string            `json:"service"`
    Level     string            `json:"level"`       // INFO, WARN, ERROR, DEBUG
    Message   string            `json:"message"`
    TraceID   string            `json:"trace_id"`
    Labels    map[string]string `json:"labels"`
    Source    string            `json:"source"`      // always "kitsune"
}

// KitsuneMetric — canonical metric entry
type KitsuneMetric struct {
    Timestamp time.Time         `json:"timestamp"`
    Name      string            `json:"name"`        // e.g. "http_error_rate"
    Value     float64           `json:"value"`
    Type      string            `json:"type"`        // gauge, counter, histogram
    Labels    map[string]string `json:"labels"`      // service, pod, namespace, etc.
    Source    string            `json:"source"`      // instana, turbonomic, kube
}
```

---

## Repository Structure

```
kitsune/
├── cmd/
│   ├── ingestor/             # kitsune-ingestor entrypoint
│   ├── consumer/             # kitsune-consumer entrypoint
│   ├── query/                # kitsune-query entrypoint
│   ├── kube-agent/           # kitsune-kube-agent entrypoint
│   ├── instana-collector/    # kitsune-instana-collector entrypoint
│   └── turbo-collector/      # kitsune-turbo-collector entrypoint
│
├── internal/
│   ├── models/               # shared KitsuneLog, KitsuneMetric structs
│   ├── kafka/                # shared producer/consumer helpers
│   ├── clickhouse/           # ClickHouse client + batch writer
│   ├── postgres/             # PostgreSQL client + queries
│   ├── config/               # env var parsing (shared)
│   └── alert/                # alert rule evaluation logic
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
│   ├── terraform/            # cloud infrastructure (EKS, networking, etc.)
│   └── helm/
│       ├── kitsune-ingestor/
│       ├── kitsune-consumer/
│       ├── kitsune-query/
│       ├── kitsune-kube-agent/
│       ├── kitsune-instana-collector/
│       └── kitsune-turbo-collector/
│
├── docker-compose.yml        # local dev stack
├── go.mod
├── go.sum
└── ARCHITECTURE.md           # this file
```

---

## Local Dev Stack (`docker-compose.yml`)

The following services are required for local development:

| Service | Port | Purpose |
|---|---|---|
| Kafka (KRaft mode) | 9092 | Message transport |
| ClickHouse | 8123, 9000 | Hot storage |
| PostgreSQL | 5432 | Metadata + cold storage |
| Grafana | 3000 | Dashboard UI |

No Zookeeper needed — use Kafka in KRaft mode.

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

### Phase 1 — Core Logging Pipeline
- [ ] `kitsune-ingestor`: `POST /ingest/logs` → Kafka
- [ ] `kitsune-consumer`: Kafka → ClickHouse batch writer (logs)
- [ ] `kitsune-query`: `GET /query/logs` with filters
- [ ] Wire Grafana → ClickHouse datasource
- [ ] `docker-compose.yml` with Kafka (KRaft), ClickHouse, Postgres, Grafana

### Phase 2 — Metrics Pipeline
- [ ] `kitsune-ingestor`: `POST /ingest/metrics/kube` → Kafka
- [ ] `kitsune-consumer`: Kafka → ClickHouse batch writer (metrics)
- [ ] `kitsune-query`: `GET /query/metrics`
- [ ] `kitsune-kube-agent`: scrape k8s metrics API → push to ingestor

### Phase 3 — External Collectors
- [ ] `kitsune-instana-collector`: poll Instana → Kafka
- [ ] `kitsune-turbo-collector`: poll Turbonomic → Kafka
- [ ] Consumer handles `kitsune.metrics.instana` and `kitsune.metrics.turbonomic`

### Phase 4 — Alerting
- [ ] Alert rules CRUD in PostgreSQL (`/alerts/rules`)
- [ ] Consumer evaluates rules on every metric batch
- [ ] Fires to `kitsune.alerts.events`
- [ ] Alert history stored in PostgreSQL

### Phase 5 — Custom UI
- [ ] `kitsune-ui` scaffold (Vite + React + TS + Tailwind)
- [ ] Log Explorer page
- [ ] Metrics Dashboard page
- [ ] Kube Overview page
- [ ] Alerting panel

### Phase 6 — Helm + Terraform
- [ ] Helm chart per service
- [ ] Terraform for EKS cluster + networking
- [ ] CI/CD pipeline (GitHub Actions)

---

## Key Design Decisions

**Why Kafka?**
Acts as a durable buffer between ingest and storage. Handles traffic spikes without backpressure on the ingestor. Enables multiple consumers (e.g., alerting + storage simultaneously).

**Why ClickHouse for hot storage?**
Column-oriented storage with excellent compression and sub-second query performance on time-series log and metric data. Native TTL support for automatic hot/cold tiering.

**Why PostgreSQL for cold/metadata?**
Already in the stack. Reliable for low-volume structured data like alert rules, service registry, and cold log archive. No need for another database just for config.

**Why push for logs, pull for APM metrics?**
Logs are produced by the user's own services — push is simpler and lower latency. APM metrics already exist in Instana/Turbonomic — pulling avoids double instrumentation and respects that those systems are the source of truth for application performance.

**Why a single normalized schema?**
All sources (Instana, Turbonomic, kube agent) normalize into `KitsuneMetric` before hitting Kafka. The consumer and query layer only deal with one schema regardless of source.
