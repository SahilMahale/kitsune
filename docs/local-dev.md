# Kitsune — Local Dev Infrastructure

Reference guide for provisioning the Kitsune backing stack locally with Terraform + Helm on Kubernetes.

After `terraform apply` the entire stack is ready — topics created, schemas applied, Grafana datasource wired. No manual post-apply steps.

---

## Prerequisites

```bash
brew install minikube kubectl terraform helm
```

---

## 1. Start the Local Cluster

```bash
minikube start --cpus=4 --memory=6g --driver=docker
kubectl get nodes   # verify
```

---

## 2. Terraform Setup

### File Structure

Create these files under `infra/terraform/local/`:

```
infra/terraform/local/
├── main.tf          # providers + namespace
├── variables.tf     # input variables
├── outputs.tf       # connection string outputs
├── kafka.tf         # Kafka helm_release (KRaft + topic provisioning)
├── clickhouse.tf    # ClickHouse helm_release (schema via initdbScripts)
├── postgres.tf      # PostgreSQL helm_release (schema via initdb scripts)
├── grafana.tf       # Grafana helm_release (plugin + datasource pre-wired)
└── otel.tf          # OTel Collector helm_releases (node DaemonSet + cluster Deployment)
```

### main.tf — Providers + Namespace

```hcl
terraform {
  required_providers {
    helm = {
      source  = "hashicorp/helm"
      version = "~> 2.13"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.30"
    }
  }
}

provider "kubernetes" {
  config_path    = "~/.kube/config"
  config_context = "minikube"
}

provider "helm" {
  kubernetes {
    config_path    = "~/.kube/config"
    config_context = "minikube"
  }
}

resource "kubernetes_namespace" "kitsune_infra" {
  metadata {
    name = var.namespace
  }
}
```

### variables.tf

```hcl
variable "namespace" {
  default = "kitsune-infra"
}

variable "db_password" {
  default   = "kitsune"
  sensitive = true
}
```

### outputs.tf

```hcl
output "kafka_broker"    { value = "localhost:9092" }
output "clickhouse_http" { value = "http://localhost:8123" }
output "clickhouse_tcp"  { value = "localhost:9000" }
output "postgres_dsn"    { value = "postgres://kitsune:${var.db_password}@localhost:5432/kitsune"    sensitive = true }
output "clickhouse_dsn"  { value = "clickhouse://kitsune:${var.db_password}@localhost:9000/kitsune" sensitive = true }
```

---

## 3. kafka.tf

- **Chart**: `bitnami/kafka`
- **Repo**: `https://charts.bitnami.com/bitnami`

Configures: KRaft mode, single broker, and all 5 topics (1 partition each locally) with retention.

```hcl
resource "helm_release" "kafka" {
  name       = "kitsune-kafka"
  namespace  = var.namespace
  repository = "https://charts.bitnami.com/bitnami"
  chart      = "kafka"
  wait       = true

  # KRaft — no Zookeeper
  set { name = "kraft.enabled"              value = "true"      }
  set { name = "zookeeper.enabled"          value = "false"     }
  set { name = "replicaCount"               value = "1"         }
  set { name = "listeners.client.protocol"  value = "PLAINTEXT" }
  set { name = "service.ports.client"       value = "9092"      }

  # Topic provisioning
  set { name = "provisioning.enabled" value = "true" }

  # kitsune.logs.raw — 48h, JSON logs from the ingestor
  set { name = "provisioning.topics[0].name"                        value = "kitsune.logs.raw" }
  set { name = "provisioning.topics[0].partitions"                  value = "1" }
  set { name = "provisioning.topics[0].replicationFactor"           value = "1" }
  set { name = "provisioning.topics[0].config.retention\\.ms"       value = "172800000" }

  # kitsune.logs.otlp — 48h, OTLP logs from the OTel Collector
  set { name = "provisioning.topics[1].name"                        value = "kitsune.logs.otlp" }
  set { name = "provisioning.topics[1].partitions"                  value = "1" }
  set { name = "provisioning.topics[1].replicationFactor"           value = "1" }
  set { name = "provisioning.topics[1].config.retention\\.ms"       value = "172800000" }

  # kitsune.metrics.otlp — 48h, OTLP metrics from the OTel Collector
  set { name = "provisioning.topics[2].name"                        value = "kitsune.metrics.otlp" }
  set { name = "provisioning.topics[2].partitions"                  value = "1" }
  set { name = "provisioning.topics[2].replicationFactor"           value = "1" }
  set { name = "provisioning.topics[2].config.retention\\.ms"       value = "172800000" }

  # kitsune.alerts.events — 7d, firing/resolved events from the alerter
  set { name = "provisioning.topics[3].name"                        value = "kitsune.alerts.events" }
  set { name = "provisioning.topics[3].partitions"                  value = "1" }
  set { name = "provisioning.topics[3].replicationFactor"           value = "1" }
  set { name = "provisioning.topics[3].config.retention\\.ms"       value = "604800000" }

  # kitsune.deadletter — 7d, records the consumer could not decode/validate
  set { name = "provisioning.topics[4].name"                        value = "kitsune.deadletter" }
  set { name = "provisioning.topics[4].partitions"                  value = "1" }
  set { name = "provisioning.topics[4].replicationFactor"           value = "1" }
  set { name = "provisioning.topics[4].config.retention\\.ms"       value = "604800000" }

  depends_on = [kubernetes_namespace.kitsune_infra]
}
```

---

## 4. clickhouse.tf

- **Chart**: `bitnami/clickhouse`
- **Repo**: `https://charts.bitnami.com/bitnami`

Configures: single shard, credentials, a `tiered` storage policy (hot/warm/cold as three directories on the same PVC — prod uses separate disks + S3, see the architecture doc), and runs the full schema DDL on first boot via `initdbScripts`.

```hcl
resource "helm_release" "clickhouse" {
  name       = "kitsune-clickhouse"
  namespace  = var.namespace
  repository = "https://charts.bitnami.com/bitnami"
  chart      = "clickhouse"
  wait       = true

  set { name = "shards"             value = "1"              }
  set { name = "replicaCount"       value = "1"              }
  set { name = "auth.username"      value = "kitsune"        }
  set { name = "auth.password"      value = var.db_password  }
  set { name = "auth.database"      value = "kitsune"        }
  set { name = "service.ports.http" value = "8123"           }
  set { name = "service.ports.tcp"  value = "9000"           }

  values = [<<-EOT
    # Storage policy: hot → warm → cold. Locally all three are dirs on the same PVC
    # so TTL moves can be exercised; in prod, warm/cold are separate disks / S3.
    extraOverrides: |
      <clickhouse>
        <storage_configuration>
          <disks>
            <hot><path>/bitnami/clickhouse/hot/</path></hot>
            <warm><path>/bitnami/clickhouse/warm/</path></warm>
            <cold><path>/bitnami/clickhouse/cold/</path></cold>
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

    initdbScripts:
      schema.sql: |
        CREATE DATABASE IF NOT EXISTS kitsune;

        CREATE TABLE IF NOT EXISTS kitsune.logs (
            timestamp  DateTime64(3),
            service    LowCardinality(String),
            level      LowCardinality(String),
            message    String CODEC(ZSTD(3)),
            trace_id   String,
            labels     Map(LowCardinality(String), String),
            source     LowCardinality(String) DEFAULT 'kitsune',
            INDEX idx_trace      trace_id          TYPE bloom_filter(0.01)         GRANULARITY 1,
            INDEX idx_msg_token  lower(message)    TYPE tokenbf_v1(32768, 3, 0)    GRANULARITY 1,
            INDEX idx_msg_ngram  lower(message)    TYPE ngrambf_v1(4, 65536, 3, 0) GRANULARITY 1,
            INDEX idx_label_keys mapKeys(labels)   TYPE bloom_filter(0.01)         GRANULARITY 1,
            INDEX idx_label_vals mapValues(labels) TYPE bloom_filter(0.01)         GRANULARITY 1
        ) ENGINE = MergeTree()
        PARTITION BY toDate(timestamp)
        ORDER BY (service, level, timestamp)
        TTL toDateTime(timestamp) + INTERVAL 3 DAY   TO VOLUME 'warm',
            toDateTime(timestamp) + INTERVAL 30 DAY  TO VOLUME 'cold',
            toDateTime(timestamp) + INTERVAL 365 DAY DELETE
        SETTINGS storage_policy = 'tiered', index_granularity = 8192, non_replicated_deduplication_window = 1000;

        CREATE TABLE IF NOT EXISTS kitsune.metrics (
            timestamp  DateTime64(3) CODEC(DoubleDelta, ZSTD),
            name       LowCardinality(String),
            value      Float64 CODEC(Gorilla, ZSTD),
            type       LowCardinality(String),
            labels     Map(LowCardinality(String), String),
            source     LowCardinality(String),
            INDEX idx_label_vals mapValues(labels) TYPE bloom_filter(0.01) GRANULARITY 1
        ) ENGINE = MergeTree()
        PARTITION BY toDate(timestamp)
        ORDER BY (source, name, timestamp)
        TTL toDateTime(timestamp) + INTERVAL 7 DAY   TO VOLUME 'warm',
            toDateTime(timestamp) + INTERVAL 30 DAY  TO VOLUME 'cold',
            toDateTime(timestamp) + INTERVAL 365 DAY DELETE
        SETTINGS storage_policy = 'tiered', index_granularity = 8192, non_replicated_deduplication_window = 1000;

        CREATE TABLE IF NOT EXISTS kitsune.alert_history (
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
            version      UInt64
        ) ENGINE = ReplacingMergeTree(version)
        PARTITION BY toYYYYMM(fired_at)
        ORDER BY (rule_id, fired_at, id)
        TTL toDateTime(fired_at) + INTERVAL 365 DAY DELETE;
  EOT
  ]

  depends_on = [kubernetes_namespace.kitsune_infra]
}
```

---

## 5. postgres.tf

- **Chart**: `bitnami/postgresql`
- **Repo**: `https://charts.bitnami.com/bitnami`

Configures: credentials and runs the full schema DDL on first boot via `primary.initdb.scripts`.

```hcl
resource "helm_release" "postgres" {
  name       = "kitsune-postgres"
  namespace  = var.namespace
  repository = "https://charts.bitnami.com/bitnami"
  chart      = "postgresql"
  wait       = true

  set { name = "auth.username"                    value = "kitsune"       }
  set { name = "auth.password"                    value = var.db_password }
  set { name = "auth.database"                    value = "kitsune"       }
  set { name = "primary.service.ports.postgresql" value = "5432"          }

  values = [<<-EOT
    primary:
      initdb:
        scripts:
          schema.sql: |
            -- Config only. All telemetry (logs, metrics, alert history) lives in ClickHouse.
            CREATE TABLE IF NOT EXISTS services (
                id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                name       TEXT UNIQUE NOT NULL,
                api_token  TEXT UNIQUE NOT NULL,
                created_at TIMESTAMPTZ DEFAULT NOW()
            );

            CREATE TABLE IF NOT EXISTS alert_rules (
                id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                name           TEXT NOT NULL,
                metric_name    TEXT NOT NULL,
                source         TEXT NOT NULL DEFAULT '',
                label_filters  JSONB NOT NULL DEFAULT '{}',
                aggregation    TEXT NOT NULL DEFAULT 'avg',
                condition      TEXT NOT NULL,
                threshold      FLOAT NOT NULL,
                window_secs    INT NOT NULL DEFAULT 60,
                for_secs       INT NOT NULL DEFAULT 0,
                enabled        BOOLEAN DEFAULT TRUE,
                created_at     TIMESTAMPTZ DEFAULT NOW(),
                updated_at     TIMESTAMPTZ DEFAULT NOW()
            );
  EOT
  ]

  depends_on = [kubernetes_namespace.kitsune_infra]
}
```

---

## 6. grafana.tf

- **Chart**: `grafana/grafana`
- **Repo**: `https://grafana.github.io/helm-charts`

Configures: admin credentials, pre-installs the ClickHouse plugin, and wires the datasource pointing at the in-cluster ClickHouse service. No manual UI steps needed.

```hcl
resource "helm_release" "grafana" {
  name       = "kitsune-grafana"
  namespace  = var.namespace
  repository = "https://grafana.github.io/helm-charts"
  chart      = "grafana"
  wait       = true

  set { name = "adminUser"     value = "admin" }
  set { name = "adminPassword" value = "admin" }
  set { name = "service.port"  value = "3000"  }

  values = [<<-EOT
    plugins:
      - grafana-clickhouse-datasource

    datasources:
      datasources.yaml:
        apiVersion: 1
        datasources:
          - name: ClickHouse
            type: grafana-clickhouse-datasource
            access: proxy
            url: http://kitsune-clickhouse:8123
            isDefault: true
            jsonData:
              defaultDatabase: kitsune
              username: kitsune
            secureJsonData:
              password: kitsune
  EOT
  ]

  depends_on = [helm_release.clickhouse]
}
```

---

## 7. otel.tf

- **Chart**: `open-telemetry/opentelemetry-collector`
- **Repo**: `https://open-telemetry.github.io/opentelemetry-helm-charts`
- **Image**: `otel/opentelemetry-collector-contrib` (needed for the `filelog`, `kubeletstats`, `k8s_cluster`, `prometheus`, and `kafka` components)

Two releases, both exporting OTLP protobuf to Kafka (`kitsune.logs.otlp` for logs, `kitsune.metrics.otlp` for metrics):

- **`kitsune-otel-node`** (DaemonSet): container stdout/stderr (`filelog`), kubelet stats, Prometheus scraping of annotated pods **on the same node**, and an OTLP endpoint for app SDKs.
- **`kitsune-otel-cluster`** (Deployment, 1 replica): cluster-level state from the k8s API. Running this as a single replica avoids duplicate metrics.

> Terraform heredocs interpolate `${...}`, so collector env refs are written `$${env:K8S_NODE_NAME}`. `$$1` in the relabel config is the collector's own escape for a literal `$1`.

```hcl
locals {
  otel_kafka_exporter = <<-EOT
    kafka:
      brokers: ["kitsune-kafka:9092"]
      logs:
        topic: kitsune.logs.otlp
        encoding: otlp_proto
      metrics:
        topic: kitsune.metrics.otlp
        encoding: otlp_proto
  EOT
}

resource "helm_release" "otel_node" {
  name       = "kitsune-otel-node"
  namespace  = var.namespace
  repository = "https://open-telemetry.github.io/opentelemetry-helm-charts"
  chart      = "opentelemetry-collector"
  wait       = true

  values = [<<-EOT
    mode: daemonset
    image:
      repository: otel/opentelemetry-collector-contrib
    command:
      name: otelcol-contrib

    presets:
      logsCollection:
        enabled: true          # filelog receiver on /var/log/pods + host mounts
        includeCollectorLogs: false
      kubeletMetrics:
        enabled: true          # kubeletstats receiver + RBAC + K8S_NODE_NAME env
      kubernetesAttributes:
        enabled: true          # k8sattributes processor + RBAC (pods list/watch, also used by prometheus SD)

    config:
      receivers:
        kubeletstats:
          collection_interval: 15s
          insecure_skip_verify: true   # minikube kubelet serves a self-signed cert
        prometheus:
          config:
            scrape_configs:
              - job_name: k8s-pods
                scrape_interval: 15s
                kubernetes_sd_configs:
                  - role: pod
                    selectors:
                      - role: pod
                        field: spec.nodeName=$${env:K8S_NODE_NAME}
                relabel_configs:
                  - source_labels: [__meta_kubernetes_pod_annotation_prometheus_io_scrape]
                    action: keep
                    regex: "true"
                  - source_labels: [__meta_kubernetes_pod_annotation_prometheus_io_path]
                    action: replace
                    target_label: __metrics_path__
                    regex: (.+)
                  - source_labels: [__address__, __meta_kubernetes_pod_annotation_prometheus_io_port]
                    action: replace
                    regex: ([^:]+)(?::\d+)?;(\d+)
                    replacement: $$1:$$2
                    target_label: __address__
      exporters:
        ${indent(4, local.otel_kafka_exporter)}
      service:
        pipelines:
          logs:
            receivers:  [otlp, filelog]
            processors: [memory_limiter, k8sattributes, batch]
            exporters:  [kafka]
          metrics:
            receivers:  [otlp, kubeletstats, prometheus]
            processors: [memory_limiter, k8sattributes, batch]
            exporters:  [kafka]
  EOT
  ]

  depends_on = [helm_release.kafka]
}

resource "helm_release" "otel_cluster" {
  name       = "kitsune-otel-cluster"
  namespace  = var.namespace
  repository = "https://open-telemetry.github.io/opentelemetry-helm-charts"
  chart      = "opentelemetry-collector"
  wait       = true

  values = [<<-EOT
    mode: deployment
    replicaCount: 1
    image:
      repository: otel/opentelemetry-collector-contrib
    command:
      name: otelcol-contrib

    presets:
      clusterMetrics:
        enabled: true          # k8s_cluster receiver + RBAC

    config:
      receivers:
        k8s_cluster:
          collection_interval: 15s
          node_conditions_to_report: [Ready, MemoryPressure, DiskPressure]
      exporters:
        ${indent(4, local.otel_kafka_exporter)}
      service:
        pipelines:
          metrics:
            receivers:  [k8s_cluster]
            processors: [memory_limiter, batch]
            exporters:  [kafka]
  EOT
  ]

  depends_on = [helm_release.kafka]
}
```

**Getting an app's telemetry in:**
- Prometheus endpoint: annotate the pod with `prometheus.io/scrape: "true"`, `prometheus.io/port: "<port>"` and optionally `prometheus.io/path: "/metrics"`.
- Logs: nothing to do — anything a pod writes to stdout/stderr is collected. Structured JSON lines keep their fields as attributes.
- OTel SDK: set `OTEL_EXPORTER_OTLP_ENDPOINT=http://kitsune-otel-node-opentelemetry-collector.kitsune-infra:4318`.

---

## 8. Apply

```bash
cd infra/terraform/local
terraform init
terraform apply
```

---

## 9. Expose Services Locally

```bash
kubectl port-forward svc/kitsune-kafka               -n kitsune-infra 9092:9092 &
kubectl port-forward svc/kitsune-clickhouse          -n kitsune-infra 8123:8123 &
kubectl port-forward svc/kitsune-clickhouse          -n kitsune-infra 9000:9000 &
kubectl port-forward svc/kitsune-postgres-postgresql -n kitsune-infra 5432:5432 &
kubectl port-forward svc/kitsune-grafana             -n kitsune-infra 3000:3000 &
```

| Service | Address |
|---|---|
| Kafka broker | `localhost:9092` |
| ClickHouse HTTP | `http://localhost:8123` |
| ClickHouse TCP | `localhost:9000` |
| PostgreSQL (config only) | `localhost:5432` |
| Grafana | `http://localhost:3000` (admin / admin) |

---

## Environment Variables for Kitsune Services

```bash
# Ingestor
SERVER_BIND_ADDRESS=0.0.0.0:8001
KAFKA_BROKERS=localhost:9092
POSTGRES_DSN=postgres://kitsune:kitsune@localhost:5432/kitsune   # service tokens
RATE_LIMIT_RPS=100
MAX_BODY_BYTES=5242880

# Consumer
KAFKA_BROKERS=localhost:9092
KAFKA_GROUP_ID=kitsune-consumer
CLICKHOUSE_DSN=clickhouse://kitsune:kitsune@localhost:9000/kitsune
BATCH_MAX_ROWS=1000
BATCH_MAX_WAIT_MS=1000

# Alerter
CLICKHOUSE_DSN=clickhouse://kitsune:kitsune@localhost:9000/kitsune
POSTGRES_DSN=postgres://kitsune:kitsune@localhost:5432/kitsune
KAFKA_BROKERS=localhost:9092
EVAL_INTERVAL_SECONDS=15

# Query
SERVER_BIND_ADDRESS=0.0.0.0:8002
CLICKHOUSE_DSN=clickhouse://kitsune:kitsune@localhost:9000/kitsune
POSTGRES_DSN=postgres://kitsune:kitsune@localhost:5432/kitsune
KAFKA_BROKERS=localhost:9092                                       # live tail
```

---

## Teardown

```bash
pkill -f "kubectl port-forward"
terraform destroy
minikube stop
```
