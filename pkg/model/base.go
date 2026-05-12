// Package model defines shared persistence types for database-backed entities.
package model

import (
	"time"

	"github.com/google/uuid"
)

// BaseModel is embedded by persisted entities. Integer IDs are used for internal
// relationships; UUIDs are stable external identifiers.
type BaseModel struct {
	ID        int64      `json:"id"`
	UUID      uuid.UUID  `json:"uuid"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}
