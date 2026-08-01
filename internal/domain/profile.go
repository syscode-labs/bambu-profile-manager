// Package domain holds the core types shared across the profile manager.
package domain

import "time"

// RawProfile is a Bambu Studio profile loaded from JSON with every field
// preserved verbatim, including ones this tool doesn't understand yet.
type RawProfile struct {
	Name     string
	Inherits string
	Fields   map[string]any // full decoded JSON object, keyed by original field name
}

// Profile is a canonical local profile, independent of any specific
// deployment (design.md §7).
type Profile struct {
	ID   string // UUIDv7
	Name string
}

// ProfileVersion is an immutable revision of a Profile (design.md §7).
type ProfileVersion struct {
	ID           string // UUIDv7
	ProfileID    string
	Revision     int
	SourceJSON   []byte // original profile JSON as loaded
	ResolvedJSON []byte // canonical resolved (flattened) JSON, see internal/normalize
	SemanticHash string
	CreatedAt    time.Time
}

// DomainEvent is an immutable, sequenced record of something that happened,
// used for the event log and WebSocket replay (design.md §7, §10).
type DomainEvent struct {
	ID        string // UUIDv7
	Sequence  int64
	Type      string
	EntityID  string
	Revision  int
	Payload   []byte // JSON
	CreatedAt time.Time
}
