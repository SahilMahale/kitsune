package models

import (
	"encoding/json"
	"time"
)

type LogRequest struct {
	Level   string `json:"level,omitempty"`
	Message string `json:"message,omitempty"`
}

type RequestMetadata struct {
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	Source         string `json:"source,omitempty"`
}

type ExecuteRequest struct {
	Command  string          `json:"command"`
	Version  string          `json:"version"`
	Payload  json.RawMessage `json:"payload"`
	Log      *LogRequest      `json:"log,omitempty"`
	Metadata *RequestMetadata  `json:"metadata,omitempty"`
}

type ExecuteResponse struct {
	EventID string `json:"event_id"`
	Status  string `json:"status"`
}

type EventStatusResp struct {
	EventID   string    `json:"event_id"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}
