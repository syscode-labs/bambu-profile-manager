package service

import (
	"encoding/json"
	"fmt"

	"github.com/syscod3/bambu-profile-manager/internal/domain"
	"github.com/syscod3/bambu-profile-manager/internal/normalize"
)

// TargetProfileFromVersion reconstructs the RawProfile that was actually
// published from a stored ProfileVersion's ResolvedJSON. Its Name MUST come
// from the stored fields, not from any Profile row's Name: a rebind
// published under a targetName override (RebindAndPublish) publishes a
// DIFFERENT filename than the source profile it started from. Using a
// Profile row's name here once pointed check-recognition at the SOURCE
// profile's real, pre-existing .info file — a false-positive "recognized"
// on every check, exactly what decisions.md #6 forbids. Any caller deriving
// a published path/name from a stored version (CLI, web UI) must go
// through this, not read profile.Name directly.
func TargetProfileFromVersion(resolvedJSON []byte) (*domain.RawProfile, error) {
	var fields map[string]any
	if err := json.Unmarshal(resolvedJSON, &fields); err != nil {
		return nil, fmt.Errorf("service: decode stored version: %w", err)
	}
	name, _ := fields["name"].(string)
	if name == "" {
		return nil, fmt.Errorf("service: stored version has no name field")
	}
	return &domain.RawProfile{Name: name, Fields: fields}, nil
}

func jsonMarshal(fields map[string]any) ([]byte, error) {
	b, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("service: marshal: %w", err)
	}
	return b, nil
}

func hashOf(fields map[string]any) (string, error) {
	h, err := normalize.Hash(fields)
	if err != nil {
		return "", fmt.Errorf("service: hash: %w", err)
	}
	return h, nil
}
