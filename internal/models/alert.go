package models

import "time"

// AlertRule is stored in PostgreSQL and evaluated by the consumer on every metric batch.
type AlertRule struct {
	ID         string    `json:"id"          db:"id"`
	Name       string    `json:"name"        db:"name"`
	MetricName string    `json:"metric_name" db:"metric_name"`
	Source     string    `json:"source"      db:"source"`     // instana, turbonomic, kube, or empty for any
	Condition  string    `json:"condition"   db:"condition"`  // gt, lt, eq
	Threshold  float64   `json:"threshold"   db:"threshold"`
	WindowSecs int       `json:"window_secs" db:"window_secs"`
	Enabled    bool      `json:"enabled"     db:"enabled"`
	CreatedAt  time.Time `json:"created_at"  db:"created_at"`
}

// CreateAlertRuleRequest is the body for POST /alerts/rules.
type CreateAlertRuleRequest struct {
	Name       string  `json:"name"`
	MetricName string  `json:"metric_name"`
	Source     string  `json:"source"`
	Condition  string  `json:"condition"`
	Threshold  float64 `json:"threshold"`
	WindowSecs int     `json:"window_secs"`
}

// UpdateAlertRuleRequest is the body for PUT /alerts/rules/:id.
type UpdateAlertRuleRequest struct {
	Name       string  `json:"name"`
	Condition  string  `json:"condition"`
	Threshold  float64 `json:"threshold"`
	WindowSecs int     `json:"window_secs"`
	Enabled    bool    `json:"enabled"`
}

// AlertHistory is a record of a fired alert, stored in PostgreSQL.
type AlertHistory struct {
	ID         string     `json:"id"          db:"id"`
	RuleID     string     `json:"rule_id"     db:"rule_id"`
	FiredAt    time.Time  `json:"fired_at"    db:"fired_at"`
	Value      float64    `json:"value"       db:"value"`
	ResolvedAt *time.Time `json:"resolved_at" db:"resolved_at"`
}

// AlertEvent is published to the kitsune.alerts.events Kafka topic when a rule fires.
type AlertEvent struct {
	RuleID     string    `json:"rule_id"`
	RuleName   string    `json:"rule_name"`
	MetricName string    `json:"metric_name"`
	Source     string    `json:"source"`
	Value      float64   `json:"value"`
	Threshold  float64   `json:"threshold"`
	Condition  string    `json:"condition"`
	FiredAt    time.Time `json:"fired_at"`
}
