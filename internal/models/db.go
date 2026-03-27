package models

import "time"

// Service is a registered producer stored in PostgreSQL.
// The APIToken is used for rate limiting on the ingestor.
type Service struct {
	ID        string    `json:"id"         db:"id"`
	Name      string    `json:"name"       db:"name"`
	APIToken  string    `json:"api_token"  db:"api_token"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

// RetentionConfig controls hot/cold tiering thresholds.
type RetentionConfig struct {
	ID            string    `json:"id"              db:"id"`
	HotLogDays    int       `json:"hot_log_days"    db:"hot_log_days"`
	HotMetricDays int       `json:"hot_metric_days" db:"hot_metric_days"`
	UpdatedAt     time.Time `json:"updated_at"      db:"updated_at"`
}
