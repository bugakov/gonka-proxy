package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadProviderState(t *testing.T) {
	tests := []struct {
		name     string
		contents string // empty means the file must not exist
		want     []string
		wantErr  string
	}{
		{
			name:     "missing file means all enabled",
			contents: "",
			want:     nil,
		},
		{
			name:     "empty file means all enabled",
			contents: "",
			want:     nil,
		},
		{
			name:     "empty disabled list",
			contents: `{"disabled": []}` + "\n",
			want:     []string{},
		},
		{
			name:     "single disabled provider",
			contents: `{"disabled": ["hyperfusion"]}` + "\n",
			want:     []string{"hyperfusion"},
		},
		{
			name:     "duplicates and blanks are dropped",
			contents: `{"disabled": ["a", "", "  b  ", "a"]}` + "\n",
			want:     []string{"a", "b"},
		},
		{
			name:     "malformed json is an error",
			contents: `{"disabled": [` + "\n",
			wantErr:  "decode provider state",
		},
		{
			name:     "wrong shape is an error",
			contents: `{"disabled": "hyperfusion"}` + "\n",
			wantErr:  "decode provider state",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "provider-state.json")
			if test.contents != "" {
				if err := os.WriteFile(path, []byte(test.contents), 0o600); err != nil {
					t.Fatalf("write state: %v", err)
				}
			} else if strings.Contains(test.name, "empty file") {
				if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
					t.Fatalf("write state: %v", err)
				}
			}

			state, err := LoadProviderState(path)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("load state: %v", err)
			}
			if len(state.Disabled) != len(test.want) {
				t.Fatalf("disabled = %v, want %v", state.Disabled, test.want)
			}
			for index, name := range test.want {
				if state.Disabled[index] != name {
					t.Fatalf("disabled[%d] = %q, want %q", index, state.Disabled[index], name)
				}
			}
		})
	}
}

func TestSaveProviderStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provider-state.json")
	want := []string{"hyperfusion", "gonkagate"}
	if err := SaveProviderState(path, ProviderState{Disabled: want}); err != nil {
		t.Fatalf("save state: %v", err)
	}
	loaded, err := LoadProviderState(path)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if len(loaded.Disabled) != len(want) {
		t.Fatalf("round trip lost data: %v", loaded.Disabled)
	}
	for index, name := range want {
		if loaded.Disabled[index] != name {
			t.Fatalf("round trip changed order: disabled[%d] = %q, want %q", index, loaded.Disabled[index], name)
		}
	}
}

func TestSaveProviderStateWritesEmptyListForNil(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provider-state.json")
	if err := SaveProviderState(path, ProviderState{}); err != nil {
		t.Fatalf("save state: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if string(data) != "{\n  \"disabled\": []\n}\n" {
		t.Fatalf("unexpected file contents: %q", string(data))
	}
}

func TestSaveProviderStateLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "provider-state.json")
	if err := SaveProviderState(path, ProviderState{Disabled: []string{"a"}}); err != nil {
		t.Fatalf("save state: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "provider-state.json" {
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("unexpected directory contents: %v", names)
	}
}

func TestDefaultStatePath(t *testing.T) {
	if got := DefaultStatePath("/etc/gonka-proxy/config.yaml"); got != "/etc/gonka-proxy/provider-state.json" {
		t.Fatalf("DefaultStatePath = %q", got)
	}
	if got := DefaultStatePath("config.yaml"); got != "provider-state.json" {
		t.Fatalf("DefaultStatePath relative = %q", got)
	}
}

func TestFilterDisabledProviders(t *testing.T) {
	aliases := func() map[string]string {
		return map[string]string{"gonka": "m", "bench": "m"}
	}
	base := Config{
		ListenAddress:         "127.0.0.1:0",
		Cooldown:              1,
		RecoveryWait:          1,
		ResponseHeaderTimeout: 1,
		ReasoningEffort:       nil,
		Providers: []Provider{
			{Name: "primary", BaseURL: "https://primary.example/v1", APIKey: "k", ModelAliases: aliases(), Priority: 100},
			{Name: "backup", BaseURL: "https://backup.example/v1", APIKey: "k", ModelAliases: aliases(), Priority: 50},
			{Name: "bench", BaseURL: "https://bench.example/v1", APIKey: "k", ModelAliases: aliases(), Priority: 10},
		},
		ModelRoutes: map[string]ModelRoute{
			"gonka": {Providers: []string{"primary", "backup"}},
			"bench": {Providers: []string{"bench", "primary"}},
		},
		ModelRouteOrder: []string{"gonka", "bench"},
	}

	t.Run("empty list returns config unchanged", func(t *testing.T) {
		got, err := FilterDisabledProviders(base, nil)
		if err != nil {
			t.Fatalf("filter: %v", err)
		}
		if len(got.Providers) != 3 {
			t.Fatalf("providers = %d, want 3", len(got.Providers))
		}
	})

	t.Run("unknown names are ignored", func(t *testing.T) {
		got, err := FilterDisabledProviders(base, []string{"does-not-exist"})
		if err != nil {
			t.Fatalf("filter: %v", err)
		}
		if len(got.Providers) != 3 {
			t.Fatalf("providers = %d, want 3", len(got.Providers))
		}
	})

	t.Run("disabled provider vanishes from pool and routes", func(t *testing.T) {
		got, err := FilterDisabledProviders(base, []string{"primary"})
		if err != nil {
			t.Fatalf("filter: %v", err)
		}
		if len(got.Providers) != 2 {
			t.Fatalf("providers = %d, want 2", len(got.Providers))
		}
		for _, provider := range got.Providers {
			if provider.Name == "primary" {
				t.Fatal("primary still present after filtering")
			}
		}
		if route := got.ModelRoutes["gonka"]; len(route.Providers) != 1 || route.Providers[0] != "backup" {
			t.Fatalf("gonka route = %v, want [backup]", route.Providers)
		}
		if route := got.ModelRoutes["bench"]; len(route.Providers) != 1 || route.Providers[0] != "bench" {
			t.Fatalf("bench route = %v, want [bench]", route.Providers)
		}
	})

	t.Run("disabling every provider fails validation", func(t *testing.T) {
		_, err := FilterDisabledProviders(base, []string{"primary", "backup", "bench"})
		if err == nil {
			t.Fatal("filter succeeded while disabling every provider")
		}
		if !strings.Contains(err.Error(), "providers must contain at least one Provider") {
			t.Fatalf("error = %q, want providers-must-not-be-empty", err)
		}
	})
}
