package models

import "time"

// AlertRule is stored in PostgreSQL and evaluated periodically by kitsune-alerter
// against ClickHouse.
type AlertRule struct {
	ID           string            `json:"id"            db:"id"`
	Name         string            `json:"name"          db:"name"`
	MetricName   string            `json:"metric_name"   db:"metric_name"`
	Source       string            `json:"source"        db:"source"`        // kube, prometheus, otlp, or empty for any
	LabelFilters map[string]string `json:"label_filters" db:"label_filters"` // exact-match label filters
	Aggregation  string            `json:"aggregation"   db:"aggregation"`   // avg, max, min, sum, count, p95
	Condition    string            `json:"condition"     db:"condition"`     // gt, lt, eq
	Threshold    float64           `json:"threshold"     db:"threshold"`
	WindowSecs   int               `json:"window_secs"   db:"window_secs"` // aggregation window
	ForSecs      int               `json:"for_secs"      db:"for_secs"`    // must stay true this long before firing
	Enabled      bool              `json:"enabled"       db:"enabled"`
	CreatedAt    time.Time         `json:"created_at"    db:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"    db:"updated_at"`
}

// CreateAlertRuleRequest is the body for POST /alerts/rules.
type CreateAlertRuleRequest struct {
	Name         string            `json:"name"`
	MetricName   string            `json:"metric_name"`
	Source       string            `json:"source"`
	LabelFilters map[string]string `json:"label_filters"`
	Aggregation  string            `json:"aggregation"`
	Condition    string            `json:"condition"`
	Threshold    float64           `json:"threshold"`
	WindowSecs   int               `json:"window_secs"`
	ForSecs      int               `json:"for_secs"`
}

// UpdateAlertRuleRequest is the body for PUT /alerts/rules/:id.
type UpdateAlertRuleRequest struct {
	Name         string            `json:"name"`
	LabelFilters map[string]string `json:"label_filters"`
	Aggregation  string            `json:"aggregation"`
	Condition    string            `json:"condition"`
	Threshold    float64           `json:"threshold"`
	WindowSecs   int               `json:"window_secs"`
	ForSecs      int               `json:"for_secs"`
	Enabled      bool              `json:"enabled"`
}

// AlertHistory is a record of a fired alert, stored in ClickHouse (ReplacingMergeTree).
// Rule fields are denormalized at fire time. Resolving an alert inserts the same
// row again with ResolvedAt set and a higher Version.
type AlertHistory struct {
	ID         string     `json:"id"          ch:"id"`
	RuleID     string     `json:"rule_id"     ch:"rule_id"`
	RuleName   string     `json:"rule_name"   ch:"rule_name"`
	MetricName string     `json:"metric_name" ch:"metric_name"`
	Source     string     `json:"source"      ch:"source"`
	Condition  string     `json:"condition"   ch:"condition"`
	Threshold  float64    `json:"threshold"   ch:"threshold"`
	Value      float64    `json:"value"       ch:"value"`
	FiredAt    time.Time  `json:"fired_at"    ch:"fired_at"`
	ResolvedAt *time.Time `json:"resolved_at" ch:"resolved_at"`
	Version    uint64     `json:"-"           ch:"version"`
}

// AlertEvent is published to the kitsune.alerts.events Kafka topic when an alert fires or resolves.
type AlertEvent struct {
	AlertID    string     `json:"alert_id"`
	Status     string     `json:"status"` // firing, resolved
	RuleID     string     `json:"rule_id"`
	RuleName   string     `json:"rule_name"`
	MetricName string     `json:"metric_name"`
	Source     string     `json:"source"`
	Value      float64    `json:"value"`
	Threshold  float64    `json:"threshold"`
	Condition  string     `json:"condition"`
	FiredAt    time.Time  `json:"fired_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}
