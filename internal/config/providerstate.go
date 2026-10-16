package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ProviderStateFileName is the default file name of the operator-controlled
// provider state overlay.
const ProviderStateFileName = "provider-state.json"

// ProviderState is the operator-controlled overlay on top of config.yaml.
// It holds no secrets and is safe to keep next to the config: Disabled lists
// provider names that must be excluded from the routing pool at startup.
type ProviderState struct {
	Disabled []string `json:"disabled"`
}

// DefaultStatePath returns the state file path that sits next to configPath.
func DefaultStatePath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), ProviderStateFileName)
}

// LoadProviderState reads the provider state file. A missing or empty file is
// not an error: every provider is enabled by default. Duplicate and blank
// names are dropped.
func LoadProviderState(path string) (ProviderState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ProviderState{}, nil
		}
		return ProviderState{}, fmt.Errorf("read provider state %q: %w", path, err)
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return ProviderState{}, nil
	}
	var state ProviderState
	if err := json.Unmarshal(trimmed, &state); err != nil {
		return ProviderState{}, fmt.Errorf("decode provider state %q: %w", path, err)
	}
	seen := make(map[string]struct{}, len(state.Disabled))
	names := make([]string, 0, len(state.Disabled))
	for _, name := range state.Disabled {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	state.Disabled = names
	return state, nil
}

// SaveProviderState writes the provider state file atomically: the JSON is
// written to a temporary file in the same directory and renamed into place,
// so a crash mid-write cannot leave a truncated state behind.
func SaveProviderState(path string, state ProviderState) error {
	if state.Disabled == nil {
		state.Disabled = []string{}
	}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode provider state: %w", err)
	}
	encoded = append(encoded, '\n')
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".provider-state-*")
	if err != nil {
		return fmt.Errorf("create temp state file: %w", err)
	}
	tempName := temp.Name()
	if _, err := temp.Write(encoded); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempName)
		return fmt.Errorf("write temp state file: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempName)
		return fmt.Errorf("close temp state file: %w", err)
	}
	if err := os.Chmod(tempName, 0o644); err != nil {
		_ = os.Remove(tempName)
		return fmt.Errorf("chmod temp state file: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		_ = os.Remove(tempName)
		return fmt.Errorf("replace provider state %q: %w", path, err)
	}
	return nil
}
