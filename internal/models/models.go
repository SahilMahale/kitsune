package models

import "time"

// KitsuneLog is the canonical log entry used across all services.
type KitsuneLog struct {
	Timestamp time.Time         `json:"timestamp"`
	Service   string            `json:"service"`
	Level     string            `json:"level"` // INFO, WARN, ERROR, DEBUG
	Message   string            `json:"message"`
	TraceID   string            `json:"trace_id"`
	Labels    map[string]string `json:"labels"`
	Source    string            `json:"source"` // kitsune (ingestor), otel (collector)
}

// KitsuneMetric is the canonical metric entry used across all services.
type KitsuneMetric struct {
	Timestamp time.Time         `json:"timestamp"`
	Name      string            `json:"name"` // e.g. "pod_cpu_usage"
	Value     float64           `json:"value"`
	Type      string            `json:"type"`   // gauge, counter, histogram
	Labels    map[string]string `json:"labels"` // k8s.pod.name, k8s.namespace.name, etc.
	Source    string            `json:"source"` // kube, prometheus, otlp
}
