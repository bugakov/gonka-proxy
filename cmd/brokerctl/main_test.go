package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, dir, contents string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

const listConfig = `reasoning_effort: null
providers:
  - name: primary
    base_url: https://primary.example/v1
    api_key: k
    model_alias: m
    priority: 100
  - name: backup
    base_url: https://backup.example/v1
    api_key: k
    model_alias: m
    priority: 50
`

func TestRunCommandListWithoutStateFile(t *testing.T) {
	dir := t.TempDir()
	configPath := writeConfig(t, dir, listConfig)
	var out bytes.Buffer
	if err := runCommand("list", nil, configPath, filepath.Join(dir, "provider-state.json"), &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "primary") || !strings.Contains(text, "backup") {
		t.Fatalf("list output missing providers: %q", text)
	}
	if strings.Contains(text, "DISABLED") {
		t.Fatalf("list output marks providers disabled without a state file: %q", text)
	}
}

func TestRunCommandDisableAndEnableRoundTrip(t *testing.T) {
	dir := t.TempDir()
	configPath := writeConfig(t, dir, listConfig)
	statePath := filepath.Join(dir, "provider-state.json")
	var out bytes.Buffer

	if err := runCommand("disable", []string{"primary"}, configPath, statePath, &out); err != nil {
		t.Fatalf("disable: %v", err)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if !strings.Contains(string(data), "primary") {
		t.Fatalf("state file does not mention primary: %q", string(data))
	}

	out.Reset()
	if err := runCommand("list", nil, configPath, statePath, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("list lines = %v, want 2", lines)
	}
	// configuredProviderNames sorts alphabetically, so backup comes first.
	if !strings.Contains(lines[0], "backup") || strings.Contains(lines[0], "DISABLED") {
		t.Fatalf("backup wrongly marked: %q", lines[0])
	}
	if !strings.Contains(lines[1], "primary") || !strings.Contains(lines[1], "DISABLED") {
		t.Fatalf("primary not marked disabled: %q", lines[1])
	}

	out.Reset()
	if err := runCommand("enable", []string{"primary"}, configPath, statePath, &out); err != nil {
		t.Fatalf("enable: %v", err)
	}
	out.Reset()
	if err := runCommand("list", nil, configPath, statePath, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.Contains(out.String(), "DISABLED") {
		t.Fatalf("providers still disabled after enable: %q", out.String())
	}
}

func TestRunCommandDisableUnknownProviderFails(t *testing.T) {
	dir := t.TempDir()
	configPath := writeConfig(t, dir, listConfig)
	var out bytes.Buffer
	err := runCommand("disable", []string{"nope"}, configPath, filepath.Join(dir, "provider-state.json"), &out)
	if err == nil || !strings.Contains(err.Error(), `unknown provider "nope"`) {
		t.Fatalf("error = %v, want unknown provider", err)
	}
}

func TestRunCommandDisableKeepsStaleStateEntriesListed(t *testing.T) {
	dir := t.TempDir()
	configPath := writeConfig(t, dir, listConfig)
	statePath := filepath.Join(dir, "provider-state.json")
	// A provider that was removed from config.yaml but lingers in the state
	// file must not break anything; list should point it out.
	if err := os.WriteFile(statePath, []byte(`{"disabled":["gone"]}`), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	var out bytes.Buffer
	if err := runCommand("list", nil, configPath, statePath, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), "gone") {
		t.Fatalf("list did not mention stale entry: %q", out.String())
	}
}

func TestRunCommandUnknownCommand(t *testing.T) {
	var out bytes.Buffer
	err := runCommand("frobnicate", nil, "config.yaml", "provider-state.json", &out)
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("error = %v, want unknown command", err)
	}
}

func TestRunCommandDisableRequiresNames(t *testing.T) {
	dir := t.TempDir()
	configPath := writeConfig(t, dir, listConfig)
	err := runCommand("disable", nil, configPath, filepath.Join(dir, "provider-state.json"), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "requires at least one provider name") {
		t.Fatalf("error = %v, want missing-name error", err)
	}
}
