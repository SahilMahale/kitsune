package models

import "time"

// LogQueryParams maps to query params for GET /query/logs.
type LogQueryParams struct {
	Service string `query:"service"`
	Level   string `query:"level"`
	Q       string `query:"q"`    // full-text search
	From    int64  `query:"from"` // unix ms
	To      int64  `query:"to"`   // unix ms
	Limit   int    `query:"limit"`
}

// MetricQueryParams maps to query params for GET /query/metrics.
type MetricQueryParams struct {
	Name   string `query:"name"`
	Source string `query:"source"` // kube, prometheus, otlp
	From   int64  `query:"from"`   // unix ms
	To     int64  `query:"to"`     // unix ms
}

// LogQueryResponse is returned by GET /query/logs.
type LogQueryResponse struct {
	Logs  []KitsuneLog `json:"logs"`
	Total int          `json:"total"`
}

// MetricQueryResponse is returned by GET /query/metrics.
type MetricQueryResponse struct {
	Metrics []KitsuneMetric `json:"metrics"`
	Total   int             `json:"total"`
}

// KubePodSnapshot is a single pod's health snapshot for GET /query/kube/pods.
type KubePodSnapshot struct {
	Pod       string    `json:"pod"`
	Namespace string    `json:"namespace"`
	Node      string    `json:"node"`
	CPUUsage  float64   `json:"cpu_usage"`
	MemUsage  float64   `json:"mem_usage"`
	Restarts  int64     `json:"restarts"`
	Timestamp time.Time `json:"timestamp"`
}

// KubeNodeSummary is a single node's resource summary for GET /query/kube/nodes.
type KubeNodeSummary struct {
	Node      string    `json:"node"`
	CPUUsage  float64   `json:"cpu_usage"`
	MemUsage  float64   `json:"mem_usage"`
	Pressure  bool      `json:"pressure"`
	Timestamp time.Time `json:"timestamp"`
}
