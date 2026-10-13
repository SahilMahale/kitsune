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
