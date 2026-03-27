# Kitsune Architecture

**Kitsune (狐)** - Event-driven log aggregator and database worker system

Named after the clever messenger fox of Japanese folklore, Kitsune provides a unified interface for async log aggregation and database command execution through Kafka-based event streaming.

---

## Overview

Kitsune is a microservices-based system that accepts HTTP requests and produces events to a single Kafka topic. Multiple independent consumer groups subscribe to this topic for parallel, asynchronous processing. It demonstrates production-grade event-driven architecture patterns commonly used in cloud-native applications.

### Core Components

1. **API Service** - HTTP interface (Fiber framework)
2. **Log Worker** - Consumer group that forwards events to Loki
3. **DB Worker** - Consumer group that executes database commands

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
           │ Single produce
           ▼
     ┌──────────────────────┐
     │   Apache Kafka        │
     │ ┌──────────────────┐ │
     │ │ kitsune-events   │ │  ◄── Single Topic
     │ │ Partitions: 6    │ │
     │ │ Replication: 1   │ │
     │ └──────────────────┘ │
     └───────┬──────────────┘
             │
             │ Two independent consumer groups
             │
      ┌──────┴───────┐
      ▼              ▼
┌──────────────┐  ┌──────────────┐
│ Log Worker   │  │  DB Worker   │
│ Group:       │  │ Group:       │
│ log-         │  │ db-          │
│ processors   │  │ processors   │
└──────┬───────┘  └──────┬───────┘
       │                 │
       ▼                 ▼
   [Loki]          [PostgreSQL]
       │                 │
       ▼                 ▼
   [Grafana]       [Query Results]
```

---

## Project Structure

```
kitsune/
├── cmd/
│   ├── api/
│   │   └── main.go          # API server entrypoint
│   ├── log-worker/
│   │   └── main.go          # Log consumer entrypoint
│   └── db-worker/
│       └── main.go          # DB consumer entrypoint
├── internal/
│   ├── api/                 # API service logic
│   │   ├── handlers.go      # HTTP handlers
│   │   ├── routes.go        # Route definitions
│   │   └── server.go        # Fiber app setup
│   ├── dbworker/            # DB worker logic
│   │   ├── consumer.go      # Kafka consumer setup
│   │   ├── processor.go     # Command processing
│   │   ├── registry.go      # Command → SQL mapping
│   │   └── idempotency.go   # Idempotency checks
│   ├── logworker/           # Log worker logic
│   │   ├── consumer.go      # Kafka consumer setup
│   │   ├── processor.go     # Log formatting
│   │   └── loki.go          # Loki HTTP client
│   ├── kafka/               # Shared Kafka utilities
│   │   ├── producer.go      # Producer wrapper
│   │   └── consumer.go      # Consumer wrapper
│   ├── models/              # Shared data structures
│   │   └── events.go        # Event structs
│   └── config/              # Configuration
│       └── config.go        # Env loading
├── deployments/
│   ├── docker-compose.yml   # Local development
│   ├── helm/                # Kubernetes deployment
│   │   └── kitsune/
│   │       ├── Chart.yaml
│   │       ├── values.yaml
│   │       ├── values-local.yaml
│   │       ├── values-prod.yaml
│   │       └── templates/
│   │           ├── api-deployment.yaml
│   │           ├── api-service.yaml
│   │           ├── db-worker-deployment.yaml
│   │           ├── log-worker-deployment.yaml
│   │           ├── configmap.yaml
│   │           └── secrets.yaml
│   └── terraform/           # Infrastructure as Code
│       ├── local/           # Local k8s (kind/minikube)
│       │   ├── main.tf
│       │   ├── kafka.tf
│       │   ├── postgres.tf
│       │   ├── loki.tf
│       │   └── variables.tf
│       └── aws/             # AWS (future)
│           ├── main.tf
│           ├── eks.tf
│           ├── msk.tf       # Managed Kafka
│           └── rds.tf       # Managed Postgres
├── Dockerfile
├── go.mod
├── go.sum
├── ARCHITECTURE.md
└── README.md
```

---

## Design Decisions

### 1. **Single Topic + Consumer Groups (Core Pattern)**

**Decision**: Use one Kafka topic (`kitsune-events`) with multiple consumer groups instead of multiple topics.

**Architecture**:
```
API → kitsune-events topic (6 partitions)
      └─▶ Consumer Group: log-processors  (Log Worker instances)
      └─▶ Consumer Group: db-processors   (DB Worker instances)
```

**Rationale**:
- **True Pub/Sub**: One event published, multiple independent subscribers
- **Kafka Idiomatic**: This is how Kafka is designed to be used in production
- **Event Reusability**: Tomorrow's analytics worker just adds a new consumer group
- **Simpler API**: Single `produce()` call, no partial failure scenarios
- **Consumer Groups**: Each group independently tracks offset, scales separately
- **Extensibility**: New use cases don't require API changes

**How Consumer Groups Work**:
- Each consumer group gets **its own copy** of every message
- Within a group, Kafka distributes partitions across instances (load balancing)
- Groups track offsets independently (log workers and DB workers don't interfere)

**Example Scaling**:
```
Topic: kitsune-events (6 partitions)

Scenario 1: Light load
- Log Worker group: 1 instance → handles all 6 partitions
- DB Worker group: 2 instances → each handles 3 partitions

Scenario 2: Heavy DB load
- Log Worker group: 1 instance → handles all 6 partitions
- DB Worker group: 6 instances → each handles 1 partition

Both groups process ALL messages independently!
```

**Partition Distribution**:
- Max **6 active consumers** per consumer group (one per partition)
- Running more than 6 instances in a group = some will be idle
- Different groups can have different instance counts

**Alternative Considered**: Dual topics (`app-logs` + `db-events`)
- **Rejected because**:
  - Couples API to specific consumers
  - Requires two produce calls (partial failure risk)
  - Adding new consumers requires API changes
  - Not the idiomatic Kafka pattern
  - Less flexibility for future extensions

---

### 2. **Event-Driven Architecture**

**Decision**: Use Kafka as the central message broker between services.

**Rationale**:
- **Decoupling**: Services don't know about each other, only the topic
- **Scalability**: Each consumer group scales independently
- **Resilience**: If one consumer group fails, others continue processing
- **Async Processing**: API responds immediately without waiting for log/DB operations
- **Replay Capability**: Can reprocess events from any offset
- **At-least-once delivery**: Kafka guarantees message delivery
- **Partition Ordering**: Messages with same key go to same partition (ordered processing)

**Trade-offs**:
- Added complexity of managing Kafka
- Eventual consistency model (not immediate)
- Requires monitoring consumer lag
- Need idempotency to handle duplicate messages

---

### 3. **Command Pattern for Database Operations**

**Decision**: Use structured command objects instead of raw SQL strings.

**Event Schema (Unified)**:
```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "type": "command",
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
    "idempotency_key": "550e8400-e29b-41d4-a716-446655440000",
    "timestamp": "2026-03-15T10:00:00Z",
    "source": "user-service"
  }
}
```

**Both workers receive the same event**:
- **Log Worker** reads `log` field, ignores `command`/`payload`
- **DB Worker** reads `command`/`payload`, ignores `log` field

**Rationale**:
- **Security**: No SQL injection risks from raw query strings
- **Maintainability**: SQL logic centralized in DB worker
- **Versioning**: Commands can evolve with version field
- **Type Safety**: Structured payloads enforce contracts
- **Auditability**: Clear intent of each operation
- **Unified Event**: Single event carries both log context and DB command

**Implementation**:
```go
var commandRegistry = map[string]string{
    "create_user": "INSERT INTO users (name, email) VALUES (:name, :email)",
    "update_user": "UPDATE users SET email = :email WHERE id = :id",
    "delete_user": "DELETE FROM users WHERE id = :id",
}
```

---

### 4. **Idempotency**

**Decision**: Include idempotency keys in all events.

**Rationale**:
- Kafka guarantees at-least-once delivery
- Same event might be consumed multiple times
- Idempotency keys prevent duplicate operations
- Use event ID as idempotency key by default

**Implementation**:
```sql
CREATE TABLE idempotency_log (
    key VARCHAR(255) PRIMARY KEY,
    processed_at TIMESTAMP DEFAULT NOW(),
    command VARCHAR(100),
    created_at TIMESTAMP DEFAULT NOW()
);

-- Before executing command, check:
SELECT 1 FROM idempotency_log WHERE key = $1;
-- If exists, skip. If not, execute and insert.
```

**DB Worker Flow**:
1. Receive message from Kafka
2. Parse event, extract idempotency_key
3. Check if key exists in idempotency_log
4. If exists → skip (already processed)
5. If not exists → execute command → insert key
6. Commit Kafka offset

---

### 5. **Terraform + Helm Deployment**

**Decision**: Use Terraform for infrastructure, Helm for application deployment.

**Rationale**:
- **Infrastructure as Code**: Version-controlled infrastructure
- **Reproducibility**: Anyone can spin up identical environment
- **Production Pattern**: Mirrors AWS/IBM deployment practices
- **Separation of Concerns**: Infra (Terraform) vs Apps (Helm)
- **Multi-Environment**: Dev/staging/prod with different values files

**Terraform Manages**:
- Kafka cluster (via Strimzi operator)
- Kafka topics with partition configuration
- PostgreSQL database
- Loki + Grafana stack
- Kubernetes namespaces

**Helm Manages**:
- Kitsune API deployment
- DB Worker deployment
- Log Worker deployment
- ConfigMaps and Secrets
- Services and Ingress

**Why this split?**:
- Infra changes infrequently (Terraform state management)
- App deployments are frequent (Helm upgrade/rollback)
- Different lifecycle management needs

---

### 6. **Partition Count: 6**

**Decision**: Configure topic with 6 partitions for local development.

**Rationale**:
- **Parallelism**: Can run up to 6 consumers per group
- **Testing**: Enough to see partition distribution behavior
- **Not Overkill**: Won't overwhelm local machine
- **Scalability Demo**: Shows how Kafka distributes load

**Configuration** (in Terraform):
```hcl
resource "kubernetes_manifest" "kitsune_topic" {
  manifest = {
    apiVersion = "kafka.strimzi.io/v1beta2"
    kind       = "KafkaTopic"
    metadata = {
      name = "kitsune-events"
    }
    spec = {
      partitions = 6
      replicas   = 1
    }
  }
}
```

**Scaling Rules**:
- 1 consumer in group → handles all 6 partitions
- 2 consumers → each handles 3 partitions
- 6 consumers → each handles 1 partition
- 10 consumers → 6 active, 4 idle (can't exceed partition count)

---

### 7. **Three Separate Services (cmd/ structure)**

**Decision**: Build as 3 independent services, not a monolith.

**Rationale**:
- **Independent Scaling**: Scale DB worker separately from API
- **Deployment Flexibility**: Can deploy/restart workers without affecting API
- **Resource Optimization**: Workers can run on different machine types
- **Failure Isolation**: API stays up even if worker crashes
- **Container-friendly**: Each service runs in its own container/pod

**Service Entrypoints**:
```
cmd/api/main.go         → Starts Fiber HTTP server
cmd/log-worker/main.go  → Starts Kafka consumer (log-processors group)
cmd/db-worker/main.go   → Starts Kafka consumer (db-processors group)
```

**Shared Code**:
```
internal/kafka/      → Common Kafka producer/consumer setup
internal/models/     → Shared event structs
internal/commands/   → Command registry
internal/config/     → Configuration loading
```

---

### 8. **Strimzi Kafka Operator**

**Decision**: Use Strimzi instead of raw Kafka deployment.

**Rationale**:
- **Kubernetes-native**: Manages Kafka as k8s custom resources
- **Production-ready**: IBM and others use this in production
- **Declarative**: Define Kafka clusters in YAML
- **Topic Management**: Create topics via KafkaTopic CRDs
- **Monitoring**: Built-in metrics and health checks
- **Upgrades**: Handles rolling upgrades safely

**Alternative Considered**: Bitnami Kafka Helm chart
- **Why Strimzi won**: Better for learning production k8s patterns

---

### 9. **Loki for Log Aggregation**

**Decision**: Use Grafana Loki instead of ELK stack.

**Rationale**:
- **Lightweight**: Lower resource footprint than Elasticsearch (~300MB vs 2-3GB)
- **Modern**: Designed for cloud-native environments
- **Integration**: Natural fit with Grafana for visualization
- **Cost**: Free and open source
- **Simplicity**: Easier to run locally on laptop

**Trade-offs**:
- Less feature-rich than Elasticsearch for complex queries
- Smaller community compared to ELK

---

### 10. **Go + Fiber for API**

**Decision**: Use Fiber web framework over Gin or standard library.

**Rationale**:
- **Performance**: Built on fasthttp, ~3x faster than Gin
- **Express-like API**: Familiar patterns for quick development
- **Minimal**: Lightweight for a worker/utility service
- **Good Documentation**: Easy to learn

**Alternative Considered**: Gin
- **Why Fiber won**: Better performance for async/background service use case

---

### 11. **segmentio/kafka-go over Confluent**

**Decision**: Use pure Go Kafka library.

**Rationale**:
- **No CGo**: Simpler build process, no librdkafka dependency
- **Cross-platform**: Compiles cleanly on macOS, Linux, Windows
- **Lightweight**: Easier to containerize
- **Sufficient Features**: Has everything needed for this use case
- **Better for Learning**: Simpler internals to understand

**Trade-offs**:
- Confluent client has more advanced features (transactions, exactly-once)
- For learning project, pure Go is better

---

## Technology Stack

| Component | Technology | Version | Rationale |
|-----------|-----------|---------|-----------|
| API Framework | Fiber | v2 | Fast, Express-like, minimal |
| Message Broker | Apache Kafka | 3.6.0 | Industry standard for event streaming |
| Kafka Operator | Strimzi | Latest | Kubernetes-native Kafka management |
| Kafka Client | segmentio/kafka-go | v0.4.47 | Pure Go, no CGo dependencies |
| Database | PostgreSQL | 16 | Mature, ACID compliant, supports JSONB |
| DB Driver | sqlx + lib/pq | Latest | Better ergonomics than database/sql |
| Log Aggregation | Grafana Loki | Latest | Lightweight, cloud-native |
| Visualization | Grafana | Latest | Rich dashboards, integrates with Loki |
| Logging | zap | v1.27.0 | Structured, high-performance |
| Config | godotenv | v1.5.1 | Simple .env file loading |
| IaC | Terraform | 1.7+ | Infrastructure as Code |
| Deployment | Helm | 3.14+ | Kubernetes package manager |
| Container Runtime | Docker | 24+ | Containerization |
| Local k8s | kind/minikube | Latest | Local Kubernetes cluster |

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

2. API generates event:
   - Creates unique ID (UUID)
   - Adds idempotency key
   - Adds metadata (timestamp, source)

3. API produces to **single topic**:
   ```go
   producer.WriteMessages(ctx, kafka.Message{
       Topic: "kitsune-events",
       Key:   []byte(userID),  // Optional: for ordering
       Value: eventJSON,
   })
   ```

4. API returns `202 Accepted` immediately

5. **Log Worker** (Consumer Group: `log-processors`):
   - Consumes from `kitsune-events`
   - Extracts `log` field
   - Formats for Loki
   - POSTs to Loki HTTP API
   - Visible in Grafana within seconds
   - Commits offset

6. **DB Worker** (Consumer Group: `db-processors`):
   - Consumes from `kitsune-events` (same message!)
   - Extracts `command` and `payload`
   - Checks idempotency key
   - Resolves `create_user` command to SQL
   - Executes: `INSERT INTO users...`
   - Records idempotency key
   - Commits offset

**Key Point**: Both workers receive the **exact same message** from the same topic, but process it independently using their own consumer groups.

---

### Error Handling

**If DB Worker fails**:
- Kafka retains event (configurable retention)
- Worker can retry on next poll
- Dead Letter Queue (DLQ) for persistent failures

**If Log Worker fails**:
- Does not affect DB operations
- Logs can be replayed from Kafka

**Idempotency prevents duplicates**:
- If worker processes same event twice
- Idempotency check prevents duplicate DB writes

---

## Deployment Workflows

### Local Development (Terraform + Helm)

**Step 1: Setup Local Kubernetes**
```bash
# Create kind cluster
kind create cluster --name kitsune

# Or use minikube
minikube start --cpus=4 --memory=8192
```

**Step 2: Deploy Infrastructure with Terraform**
```bash
cd deployments/terraform/local

# Initialize Terraform
terraform init

# Plan changes
terraform plan

# Apply infrastructure
terraform apply -auto-approve
```

This creates:
- ✅ Kafka cluster (Strimzi operator)
- ✅ Kafka topic `kitsune-events` with 6 partitions
- ✅ PostgreSQL database
- ✅ Loki + Grafana stack

**Step 3: Build Docker Images**
```bash
# Build multi-stage image
docker build -t kitsune:latest .

# Load into kind
kind load docker-image kitsune:latest --name kitsune
```

**Step 4: Deploy Kitsune with Helm**
```bash
cd deployments/helm

# Install with local values
helm install kitsune ./kitsune \
  --namespace kitsune \
  --create-namespace \
  --values ./kitsune/values-local.yaml

# Or upgrade existing
helm upgrade kitsune ./kitsune \
  --namespace kitsune \
  --values ./kitsune/values-local.yaml
```

**Step 5: Verify Deployment**
```bash
# Check pods
kubectl get pods -n kitsune

# Expected output:
# kitsune-api-xxx           1/1     Running
# kitsune-db-worker-xxx     1/1     Running
# kitsune-db-worker-yyy     1/1     Running
# kitsune-log-worker-xxx    1/1     Running

# Port forward API
kubectl port-forward -n kitsune svc/kitsune-api 8080:80

# Test endpoint
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "command": "create_user",
    "payload": {"name": "Sahil", "email": "sahil@example.com"},
    "log": {"level": "INFO", "message": "User registration"}
  }'
```

---

### Production AWS (Future)

**Terraform manages**:
- EKS cluster
- MSK (Managed Streaming for Kafka)
- RDS PostgreSQL
- CloudWatch for Loki alternative

**Helm deploys**:
- Same charts with `values-prod.yaml`
- Different resource limits
- Horizontal Pod Autoscaling (HPA)
- Ingress with TLS

---

## Scaling Strategies

### Horizontal Scaling

**API Service**:
```yaml
replicas: 3  # Load balanced via k8s Service
```

**DB Worker**:
```yaml
replicas: 5  # Kafka distributes 6 partitions across 5 instances
              # Distribution: [0,1], [2,3], [4,5], [idle], [idle]
              # Or Kafka can rebalance: [0,1], [2], [3], [4], [5]
```

**Log Worker**:
```yaml
replicas: 2  # Lower throughput, fewer replicas
             # Distribution: [0,1,2], [3,4,5]
```

**Autoscaling**:
```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: kitsune-db-worker
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: kitsune-db-worker
  minReplicas: 2
  maxReplicas: 6  # Match partition count
  metrics:
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 70
```

---

### Partition Strategy

**kitsune-events topic**:
- Partitions: 6
- Replication Factor: 1 (local), 3 (production)
- Retention: 7 days
- Cleanup Policy: delete

**Partition Key Strategy**:
```go
// Option 1: No key (round-robin)
producer.WriteMessages(ctx, kafka.Message{
    Topic: "kitsune-events",
    Value: eventJSON,
})

// Option 2: Key by entity ID (ensures ordering per entity)
producer.WriteMessages(ctx, kafka.Message{
    Topic: "kitsune-events",
    Key:   []byte(userID),  // Same user → same partition
    Value: eventJSON,
})
```

**When to use partition keys**:
- ✅ Need strict ordering for same entity (user, order, session)
- ❌ Don't need ordering (logs, metrics, independent events)

---

## Observability

### Metrics to Monitor

**API Service**:
- Request rate (requests/sec)
- Response time (p50, p95, p99)
- Error rate (4xx, 5xx)
- Kafka produce errors
- Kafka produce latency

**Workers**:
- **Consumer lag** (most critical!)
- Messages processed/sec
- Processing time per message
- Error rate
- Idempotency key collisions
- Partition assignment

**Kafka**:
- Topic throughput (messages/sec)
- Partition distribution
- Disk usage
- Under-replicated partitions

**Database**:
- Query execution time
- Connection pool usage
- Deadlocks
- Table sizes

### Logging Strategy

- All services log to stdout (JSON format via zap)
- Log Worker aggregates to Loki
- Grafana dashboards for visualization
- Structured logging with context:

```go
logger.Info("Command executed",
    zap.String("command", "create_user"),
    zap.String("idempotency_key", key),
    zap.Duration("execution_time", elapsed),
)
```

### Consumer Lag Monitoring

**Critical metric**: Consumer lag = messages produced - messages consumed

```bash
# Check lag
kafka-consumer-groups.sh \
  --bootstrap-server localhost:9092 \
  --group db-processors \
  --describe

# Output shows LAG column per partition
```

**Alert if**:
- Lag > 1000 messages
- Lag growing over time
- Rebalancing frequently

---

## Future Enhancements

### 1. Dead Letter Queue (DLQ)

**Problem**: Some events fail permanently (malformed data, business rule violations)

**Solution**:
```
Main Topic: kitsune-events
DLQ Topic: kitsune-events-dlq

After N retries → produce to DLQ
Manual inspection/reprocessing
```

---

### 2. Circuit Breaker

**Problem**: PostgreSQL down → DB Worker keeps retrying, wastes resources

**Solution**:
- Detect repeated failures
- Pause consumer for X minutes
- Prevents backpressure on Kafka

---

### 3. Command Versioning

**Problem**: Command schema evolves over time

**Solution**:
```json
{
  "command": "create_user",
  "version": "2",
  "payload": {
    "name": "Sahil",
    "email": "sahil@example.com",
    "phone": "+91-1234567890"  // New field in v2
  }
}
```

DB Worker handles both v1 and v2:
```go
switch event.Version {
case "1":
    return handleCreateUserV1(event)
case "2":
    return handleCreateUserV2(event)
}
```

---

### 4. Distributed Tracing

**Problem**: Hard to trace request through API → Kafka → Workers

**Solution**: Add OpenTelemetry
```
API → produces event with trace_id
Workers → extract trace_id, continue trace
View full request flow in Jaeger
```

---

### 5. Schema Registry

**Problem**: Event schema changes break consumers

**Solution**: Confluent Schema Registry
- Enforce backward compatibility
- Version schemas centrally
- Validate events before producing

---

### 6. Result Topic

**Problem**: API can't get DB operation result

**Solution**:
```
kitsune-events → DB Worker → kitsune-results
API subscribes to results topic
Async response back to caller
```

---

### 7. Analytics Consumer

**Problem**: Want to analyze all events

**Solution**: Just add a new consumer group!
```
Consumer Group: analytics-processors
Reads from: kitsune-events
Writes to: ClickHouse/BigQuery
```

**No changes needed to**:
- API code
- Existing workers
- Kafka configuration

**This is the power of the single topic + consumer groups pattern!**

---

## Resource Requirements

### Local Development (32GB MacBook Pro)

**Minimal Setup** (Docker Compose):
```
Kafka:              1 GB
PostgreSQL:         256 MB
Kitsune services:   500 MB
─────────────────────────
Total:             ~2 GB
```

**Full k8s Setup** (kind + Terraform + Helm):
```
Kubernetes (kind):  2 GB
Kafka (Strimzi):    2 GB
PostgreSQL:         512 MB
Loki + Grafana:     600 MB
Kitsune services:   1 GB
─────────────────────────
Total:             ~6 GB

With macOS, IDE, browser: ~15 GB total
Free RAM on 32GB: ~17 GB
```

**Heavy Testing** (scaled workers):
```
Infrastructure:     6 GB
Kitsune (scaled):   3 GB (6 DB workers, 4 log workers)
Dev environment:    10 GB
─────────────────────────
Total:             ~19 GB

Free RAM: ~13 GB
```

---

## Testing Strategy

### Unit Tests
```go
// internal/dbworker/processor_test.go
func TestCommandExecution(t *testing.T) {
    // Test command registry
    // Test idempotency checks
    // Test SQL execution
}
```

### Integration Tests
```go
// Test Kafka producer/consumer
// Test end-to-end flow
// Use testcontainers for Kafka/Postgres
```

### Load Testing
```bash
# Produce 10k events
for i in {1..10000}; do
  curl -X POST localhost:8080/execute -d '...'
done

# Monitor consumer lag
watch -n 1 'kafka-consumer-groups.sh --describe --group db-processors'
```

---

## References

### Kafka Patterns
- [CQRS Pattern](https://martinfowler.com/bliki/CQRS.html)
- [Event-Driven Architecture](https://martinfowler.com/articles/201701-event-driven.html)
- [Kafka Documentation](https://kafka.apache.org/documentation/)
- [Consumer Groups](https://kafka.apache.org/documentation/#consumergroups)

### Tools
- [Strimzi Kafka Operator](https://strimzi.io/)
- [Grafana Loki](https://grafana.com/oss/loki/)
- [Fiber Framework](https://docs.gofiber.io/)
- [segmentio/kafka-go](https://github.com/segmentio/kafka-go)

### Cloud-Native
- [Twelve-Factor App](https://12factor.net/)
- [Kubernetes Best Practices](https://kubernetes.io/docs/concepts/)
- [Helm Charts](https://helm.sh/docs/)
- [Terraform](https://www.terraform.io/docs)

---

## License

MIT

---

**Last Updated**: March 15, 2026  
**Author**: Sahil  
**Status**: In Development
