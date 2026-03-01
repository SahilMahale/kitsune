package models

import "time"


type IdempotencyRecord struct {
	Service        string    `db:"service"`
	IdempotencyKey string    `db:"idempotency_key"`
	State          string    `db:"state"`
	ProcessedAt    time.Time `db:"processed_at"`
}

type StatusRecord struct {
	EventID   string    `db:"event_id"`
	Status    string    `db:"status"`
	UpdatedAt time.Time `db:"updated_at"`
}
