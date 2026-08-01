// Package domain holds the core types shared across the profile manager.
package domain

// RawProfile is a Bambu Studio profile loaded from JSON with every field
// preserved verbatim, including ones this tool doesn't understand yet.
type RawProfile struct {
	Name     string
	Inherits string
	Fields   map[string]any // full decoded JSON object, keyed by original field name
}
