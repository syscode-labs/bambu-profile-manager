// Package parser loads Bambu Studio profile JSON files without discarding
// unknown fields.
package parser

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/syscod3/bambu-profile-manager/internal/domain"
)

// Load reads a Bambu Studio profile JSON file at path.
func Load(path string) (*domain.RawProfile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("parser: read %s: %w", path, err)
	}
	return Parse(b)
}

// Parse decodes raw profile JSON bytes.
func Parse(b []byte) (*domain.RawProfile, error) {
	var fields map[string]any
	if err := json.Unmarshal(b, &fields); err != nil {
		return nil, fmt.Errorf("parser: decode: %w", err)
	}
	name, _ := fields["name"].(string)
	inherits, _ := fields["inherits"].(string)
	return &domain.RawProfile{Name: name, Inherits: inherits, Fields: fields}, nil
}
