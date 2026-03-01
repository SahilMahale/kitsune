package models

import (
	"encoding/json"
	"time"
)

type EventMetadata struct {
	IdempotencyKey string `json:"idempotency_key"`
	Source         string `json:"source"`
	Producer       string `json:"producer,omitempty"`
	Tenant         string `json:"tenant,omitempty"`
}

type CommandEvent struct {
	EventID        string          `json:"event_id"`
	Service        string          `json:"service"`
	Command        string          `json:"command"`
	CommandVersion  string          `json:"command_version"`
	SchemaVersion  string          `json:"schema_version"`
	Payload        json.RawMessage `json:"payload"`
	OccurredAt     time.Time       `json:"occurred_at"`
	TraceID        string          `json:"trace_id"`
	Metadata       EventMetadata   `json:"metadata"`
}

type LogEvent struct {
	EventID  string        `json:"event_id"`
	TraceID  string        `json:"trace_id"`
	Log      LogRequest    `json:"log"`
	Metadata EventMetadata `json:"metadata"`
}

type ResultEvent struct {
	EventID      string    `json:"event_id"`
	Service      string    `json:"service"`
	Command      string    `json:"command"`
	Status       string    `json:"status"`
	ErrorCode    int       `json:"error_code,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
	ProcessedAt  time.Time `json:"processed_at"`
	TraceID      string    `json:"trace_id"`
}
