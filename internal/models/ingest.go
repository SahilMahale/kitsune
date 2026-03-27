package models

// IngestLogsRequest is the payload for POST /ingest/logs.
type IngestLogsRequest struct {
	Logs []KitsuneLog `json:"logs"`
}

// IngestMetricsRequest is the payload for POST /ingest/metrics/kube.
type IngestMetricsRequest struct {
	Metrics []KitsuneMetric `json:"metrics"`
}

// IngestResponse is returned on successful ingest (202 Accepted).
type IngestResponse struct {
	Accepted int    `json:"accepted"`
	Status   string `json:"status"`
}
