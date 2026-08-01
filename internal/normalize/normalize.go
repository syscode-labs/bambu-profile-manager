// Package normalize turns an effective profile into a canonical form and a
// stable semantic hash, ignoring volatile metadata that doesn't affect
// slicing (see openspec/changes/init-profile-manager/design.md §14).
package normalize

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// volatile lists field names excluded from semantic equality: they vary
// between otherwise-identical profiles without changing print behaviour.
var volatile = map[string]bool{
	"name":                 true,
	"inherits":             true,
	"from":                 true,
	"instantiation":        true,
	"filament_settings_id": true,
	"setting_id":           true,
	"base_id":              true,
	"is_custom_defined":    true,
	"update_time":          true,
}

// Canonical returns a deterministic JSON encoding of fields with volatile
// keys removed. encoding/json sorts map keys, so equal field sets always
// produce byte-identical output.
func Canonical(fields map[string]any) ([]byte, error) {
	clean := make(map[string]any, len(fields))
	for k, v := range fields {
		if volatile[k] {
			continue
		}
		clean[k] = v
	}
	b, err := json.Marshal(clean)
	if err != nil {
		return nil, fmt.Errorf("normalize: marshal: %w", err)
	}
	return b, nil
}

// Hash returns the semantic hash of fields: sha256 of its canonical form.
func Hash(fields map[string]any) (string, error) {
	b, err := Canonical(fields)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
