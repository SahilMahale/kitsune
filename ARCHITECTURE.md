# Kitsune Architecture

**Kitsune (狐)** - Event-driven log aggregator and database worker system

Named after the clever messenger fox of Japanese folklore, Kitsune provides a unified interface for async log aggregation and database command execution through Kafka-based event streaming.

---

## Overview

Kitsune is a microservices-based system that accepts HTTP requests and fans them out to multiple Kafka topics for parallel, asynchronous processing. It demonstrates event-driven architecture patterns commonly used in cloud-native applications.

### Core Components

1. **API Service** - HTTP interface (Fiber framework)
2. **Log Worker** - Consumes log events and forwards to Loki
3. **DB Worker** - Consumes database command events and executes queries

---

## Architecture Diagram

```
┌─────────────────────┐
│  External Services  │
│  (User apps)        │
└──────────┬──────────┘
           │ HTTP POST /execute
           ▼
┌──────────────────────┐
│   Kitsune API        │
│   (Fiber)            │
│   Port: 8080         │
└──────────┬───────────┘
           │ Produces events
           ▼
┌─────────────────────────┐
│      Apache Kafka        │
│  ┌─────────────────────┐│
│  │ Topic: app-logs     ││
│  │ Topic: db-events    ││
│  └─────────────────────┘│
└───────┬─────────────┬───┘
        │             │
        │             │ Independent consumers
        ▼             ▼
┌──────────────┐  ┌──────────────┐
│ Log Worker   │  │  DB Worker   │
│              │  │              │
└──────┬───────┘  └──────┬───────┘
       │                 │
       ▼                 ▼
   [Loki]          [PostgreSQL]
       │                 │
       ▼                 ▼
   [Grafana]       [Query Results]
```

---

## Design Decisions

### 1. **Event-Driven Architecture**

**Decision**: Use Kafka as the central message broker between services.

**Rationale**:
- **Decoupling**: Services don't know about each other, only Kafka topics
- **Scalability**: Each worker can be scaled independently
- **Resilience**: If one worker fails, others continue processing
- **Async Processing**: API responds immediately without waiting for log/DB operations
- **Replay Capability**: Can reprocess events if needed

**Trade-offs**:
- Added complexity of managing Kafka
- Eventual consistency model (not immediate)
- Requires monitoring Kafka consumer lag

---

### 2. **Command Pattern for Database Operations**

**Decision**: Use structured command objects instead of raw SQL strings.

**Event Schema**:
```json
{
  "id": "uuid",
  "command": "create_user",
  "version": "1",
  "payload": {
    "name": "Sahil",
    "email": "sahil@example.com"
  },
  "log": {
    "level": "INFO",
    "message": "User registration triggered"
  },
  "metadata": {
    "idempotency_key": "uuid",
    "timestamp": "2026-02-19T10:00:00Z",
    "source": "user-service"
  }
}
```

**Rationale**:
- **Security**: No SQL injection risks from raw query strings
- **Maintainability**: SQL logic centralized in DB worker
- **Versioning**: Commands can evolve with version field
- **Type Safety**: Structured payloads enforce contracts
- **Auditability**: Clear intent of each operation

**Implementation**:
```go
var commandRegistry = map[string]string{
    "create_user": "INSERT INTO users (name, email) VALUES (:name, :email)",
    "update_user": "UPDATE users SET email = :email WHERE id = :id",
    "delete_user": "DELETE FROM users WHERE id = :id",
}
```

---

### 3. **Idempotency**

**Decision**: Include idempotency keys in all events.

**Rationale**:
- Kafka guarantees at-least-once delivery
- Same event might be consumed multiple times
- Idempotency keys prevent duplicate operations

**Implementation**:
- Store processed idempotency keys in a separate table
- Check before executing command
- Use event ID as idempotency key by default

```sql
CREATE TABLE idempotency_log (
    key VARCHAR(255) PRIMARY KEY,
    processed_at TIMESTAMP DEFAULT NOW()
);
```

---

### 4. **Dual-Topic Fanout**

**Decision**: Single API call produces to multiple Kafka topics.

**Rationale**:
- One user action → multiple side effects (log + DB operation)
- Parallel processing improves throughput
- Each consumer handles one concern (separation of concerns)

**Alternative Considered**: Single topic with multiple consumer groups
- **Rejected because**: Couples log and DB concerns in event schema

---

### 5. **Loki for Log Aggregation**

**Decision**: Use Grafana Loki instead of ELK stack.

**Rationale**:
- **Lightweight**: Lower resource footprint than Elasticsearch
- **Modern**: Designed for cloud-native environments
- **Integration**: Natural fit with Grafana for visualization
- **Cost**: Free and open source

**Trade-offs**:
- Less feature-rich than Elasticsearch for complex queries
- Smaller community compared to ELK

---

### 6. **Three Separate Services (cmd/ structure)**

**Decision**: Build as 3 independent services, not a monolith.

**Structure**:
```
cmd/
├── api/main.go         # HTTP server
├── log-worker/main.go  # Log consumer
└── db-worker/main.go   # DB consumer
```

**Rationale**:
- **Independent Scaling**: Scale DB worker separately from API
- **Deployment Flexibility**: Can deploy/restart workers without affecting API
- **Resource Optimization**: Workers can run on different machine types
- **Failure Isolation**: API stays up even if worker crashes

**Shared Code**:
```
internal/
├── kafka/      # Common Kafka producer/consumer setup
├── models/     # Shared event structs
├── commands/   # Command registry
└── config/     # Configuration loading
```

---

### 7. **Go + Fiber for API**

**Decision**: Use Fiber web framework over Gin or standard library.

**Rationale**:
- **Performance**: Built on fasthttp, ~3x faster than Gin
- **Express-like API**: Familiar patterns for quick development
- **Minimal**: Lightweight for a worker/utility service

**Alternative Considered**: Gin
- **Why Fiber won**: Better performance for async/background service use case

---

### 8. **segmentio/kafka-go over Confluent**

**Decision**: Use pure Go Kafka library.

**Rationale**:
- **No CGo**: Simpler build process, no librdkafka dependency
- **Lightweight**: Easier to containerize
- **Sufficient Features**: Has everything needed for this use case

**Trade-offs**:
- Confluent client has more advanced features (transactions, exactly-once)
- For hobby/learning project, pure Go is better

---

## Technology Stack

| Component | Technology | Rationale |
|-----------|-----------|-----------|
| API Framework | Fiber v2 | Fast, Express-like, minimal |
| Message Broker | Apache Kafka | Industry standard for event streaming |
| Kafka Client | segmentio/kafka-go | Pure Go, no CGo dependencies |
| Database | PostgreSQL | Mature, ACID compliant, supports JSONB |
| DB Driver | sqlx + lib/pq | Better ergonomics than database/sql |
| Log Aggregation | Grafana Loki | Lightweight, cloud-native |
| Visualization | Grafana | Rich dashboards, integrates with Loki |
| Logging | zap | Structured, high-performance |
| Config | godotenv | Simple .env file loading |

---

## Data Flow

### Happy Path: User Creation

1. External service calls API:
```bash
POST /execute
{
  "command": "create_user",
  "payload": {"name": "Sahil", "email": "sahil@example.com"},
  "log": {"level": "INFO", "message": "User created"}
}
```

2. API generates event ID and idempotency key

3. API produces to **two topics simultaneously**:
   - `app-logs` topic
   - `db-events` topic

4. API returns `202 Accepted` immediately

5. **Log Worker** (independently):
   - Consumes from `app-logs`
   - Forwards to Loki via HTTP API
   - Visible in Grafana within seconds

6. **DB Worker** (independently):
   - Consumes from `db-events`
   - Checks idempotency key
   - Resolves `create_user` command to SQL
   - Executes: `INSERT INTO users...`
   - Records idempotency key

### Error Handling

**If DB Worker fails**:
- Kafka retains event (configurable retention)
- Worker can retry on next poll
- Dead Letter Queue (DLQ) for persistent failures

**If Log Worker fails**:
- Does not affect DB operations
- Logs can be replayed from Kafka

---

## Scaling Strategies

### Horizontal Scaling

**API Service**:
```yaml
replicas: 3  # Load balanced via k8s Service
```

**DB Worker**:
```yaml
replicas: 5  # Kafka automatically distributes partitions
```

**Log Worker**:
```yaml
replicas: 2  # Lower throughput, fewer replicas needed
```

### Partition Strategy

**db-events topic**: 
- Partitions: 6
- Key: `user_id` or `entity_id` (ensures ordering per entity)

**app-logs topic**:
- Partitions: 3
- Key: `null` (round-robin, order doesn't matter)

---

## Observability

### Metrics to Monitor

1. **API Service**:
   - Request rate
   - Response time (p50, p95, p99)
   - Kafka produce errors

2. **Workers**:
   - Consumer lag (critical!)
   - Processing rate
   - Error rate
   - Idempotency key collisions

3. **Kafka**:
   - Topic throughput
   - Partition distribution
   - Disk usage

### Logging Strategy

- All services log to stdout (JSON format via zap)
- Log Worker aggregates to Loki
- Grafana dashboards for visualization

---

## Future Enhancements

1. **Dead Letter Queue (DLQ)**
   - Topic for failed events after N retries
   - Separate consumer for manual inspection

2. **Circuit Breaker**
   - Pause DB Worker if PostgreSQL is down
   - Prevents backpressure on Kafka

3. **Command Versioning**
   - Support multiple versions of same command
   - Graceful migration (e.g., `create_user_v1` → `create_user_v2`)

4. **Distributed Tracing**
   - Add OpenTelemetry
   - Trace request through API → Kafka → Workers

5. **Schema Registry**
   - Use Confluent Schema Registry
   - Enforce event schema evolution rules

6. **Result Topic**
   - DB Worker publishes results back to `db-results` topic
   - API can subscribe for async responses

---

## Development Setup

### Prerequisites

- Go 1.22+
- Docker & Docker Compose
- (Optional) Kubernetes for deployment

### Local Environment

```bash
# Start infrastructure
docker-compose up -d kafka postgres loki grafana

# Run services
go run cmd/api/main.go
go run cmd/log-worker/main.go
go run cmd/db-worker/main.go
```

### Environment Variables

```env
# API
HTTP_PORT=8080
KAFKA_BROKERS=localhost:9092

# Workers
KAFKA_BROKERS=localhost:9092
KAFKA_GROUP_ID=kitsune-workers
POSTGRES_DSN=postgres://user:pass@localhost:5432/kitsune
LOKI_URL=http://localhost:3100
```

---

## References

- [CQRS Pattern](https://martinfowler.com/bliki/CQRS.html)
- [Event-Driven Architecture](https://martinfowler.com/articles/201701-event-driven.html)
- [Kafka Documentation](https://kafka.apache.org/documentation/)
- [Grafana Loki](https://grafana.com/oss/loki/)

---

## License

MIT

---

**Last Updated**: February 19, 2026
