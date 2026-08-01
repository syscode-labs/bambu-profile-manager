package service

import (
	"encoding/json"
	"fmt"

	"github.com/syscod3/bambu-profile-manager/internal/normalize"
)

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
