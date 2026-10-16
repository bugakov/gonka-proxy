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
		got, dropped, err := FilterDisabledProviders(base, nil)
		if err != nil {
			t.Fatalf("filter: %v", err)
		}
		if dropped != nil {
			t.Fatalf("dropped routes = %v, want nil", dropped)
		}
		if len(got.Providers) != 3 {
			t.Fatalf("providers = %d, want 3", len(got.Providers))
		}
	})

	t.Run("unknown names are ignored", func(t *testing.T) {
		got, dropped, err := FilterDisabledProviders(base, []string{"does-not-exist"})
		if err != nil {
			t.Fatalf("filter: %v", err)
		}
		if dropped != nil {
			t.Fatalf("dropped routes = %v, want nil", dropped)
		}
		if len(got.Providers) != 3 {
			t.Fatalf("providers = %d, want 3", len(got.Providers))
		}
	})

	t.Run("disabled provider vanishes from pool and routes", func(t *testing.T) {
		got, dropped, err := FilterDisabledProviders(base, []string{"primary"})
		if err != nil {
			t.Fatalf("filter: %v", err)
		}
		if dropped != nil {
			t.Fatalf("dropped routes = %v, want nil", dropped)
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

	t.Run("route survives when a remaining provider serves the model", func(t *testing.T) {
		got, dropped, err := FilterDisabledProviders(base, []string{"bench"})
		if err != nil {
			t.Fatalf("filter: %v", err)
		}
		if dropped != nil {
			t.Fatalf("dropped routes = %v, want nil", dropped)
		}
		// primary also serves bench, so the route keeps running with it.
		if route := got.ModelRoutes["bench"]; len(route.Providers) != 1 || route.Providers[0] != "primary" {
			t.Fatalf("bench route = %v, want [primary]", route.Providers)
		}
		if len(got.ModelRouteOrder) != 2 {
			t.Fatalf("model route order = %v, want [gonka bench]", got.ModelRouteOrder)
		}
	})

	t.Run("route with every provider disabled is dropped and reported", func(t *testing.T) {
		got, dropped, err := FilterDisabledProviders(base, []string{"primary", "backup"})
		if err != nil {
			t.Fatalf("filter: %v", err)
		}
		if len(dropped) != 1 || dropped[0] != "gonka" {
			t.Fatalf("dropped routes = %v, want [gonka]", dropped)
		}
		if _, exists := got.ModelRoutes["gonka"]; exists {
			t.Fatal("gonka route still present after dropping")
		}
		if len(got.ModelRouteOrder) != 1 || got.ModelRouteOrder[0] != "bench" {
			t.Fatalf("model route order = %v, want [bench]", got.ModelRouteOrder)
		}
		if len(got.Providers) != 1 {
			t.Fatalf("providers = %d, want 1", len(got.Providers))
		}
		if route := got.ModelRoutes["bench"]; len(route.Providers) != 1 || route.Providers[0] != "bench" {
			t.Fatalf("bench route = %v, want [bench]", route.Providers)
		}
	})

	t.Run("disabling every provider fails validation", func(t *testing.T) {
		_, _, err := FilterDisabledProviders(base, []string{"primary", "backup", "bench"})
		if err == nil {
			t.Fatal("filter succeeded while disabling every provider")
		}
		if !strings.Contains(err.Error(), "providers must contain at least one Provider") {
			t.Fatalf("error = %q, want providers-must-not-be-empty", err)
		}
	})
}

// TestFilterDisabledProvidersGlobalRoutes covers global routes (an empty
// providers list), where validation resolves serving providers by scanning
// the whole pool. This is the production layout: model_routes entries like
// "qwen3.8-27b: {}" served by whichever provider lists the model alias.
func TestFilterDisabledProvidersGlobalRoutes(t *testing.T) {
	base := Config{
		ListenAddress:         "127.0.0.1:0",
		Cooldown:              1,
		RecoveryWait:          1,
		ResponseHeaderTimeout: 1,
		Providers: []Provider{
			{Name: "hyperfusion", BaseURL: "https://hyperfusion.example/v1", APIKey: "k",
				ModelAliases: map[string]string{"gonka": "m", "qwen3.8-27b": "Qwen/Qwen3.8-27B"}, Priority: 100},
			{Name: "gonka-router", BaseURL: "https://router.example/v1", APIKey: "k",
				ModelAliases: map[string]string{"gonka": "m"}, Priority: 80},
		},
		ModelRoutes: map[string]ModelRoute{
			"gonka":       {Providers: []string{"hyperfusion", "gonka-router"}},
			"qwen3.8-27b": {},
		},
		ModelRouteOrder: []string{"gonka", "qwen3.8-27b"},
	}

	t.Run("global route survives while a provider serves it", func(t *testing.T) {
		got, dropped, err := FilterDisabledProviders(base, []string{"gonka-router"})
		if err != nil {
			t.Fatalf("filter: %v", err)
		}
		if dropped != nil {
			t.Fatalf("dropped routes = %v, want nil", dropped)
		}
		if _, exists := got.ModelRoutes["qwen3.8-27b"]; !exists {
			t.Fatal("global route dropped although hyperfusion still serves it")
		}
	})

	t.Run("global route without serving providers is dropped and reported", func(t *testing.T) {
		got, dropped, err := FilterDisabledProviders(base, []string{"hyperfusion"})
		if err != nil {
			t.Fatalf("filter: %v", err)
		}
		if len(dropped) != 1 || dropped[0] != "qwen3.8-27b" {
			t.Fatalf("dropped routes = %v, want [qwen3.8-27b]", dropped)
		}
		if _, exists := got.ModelRoutes["qwen3.8-27b"]; exists {
			t.Fatal("global route still present after dropping")
		}
		if len(got.ModelRouteOrder) != 1 || got.ModelRouteOrder[0] != "gonka" {
			t.Fatalf("model route order = %v, want [gonka]", got.ModelRouteOrder)
		}
		// The surviving explicit route keeps its remaining provider.
		if route := got.ModelRoutes["gonka"]; len(route.Providers) != 1 || route.Providers[0] != "gonka-router" {
			t.Fatalf("gonka route = %v, want [gonka-router]", route.Providers)
		}
	})
}

// TestFilterDisabledProvidersAliasLoss covers an explicit route that mixes
// serving and non-serving providers: dropping the only serving provider must
// drop the route even though other providers remain listed in it, because
// validation rejects routes nobody can serve.
func TestFilterDisabledProvidersAliasLoss(t *testing.T) {
	cfg := Config{
		ListenAddress:         "127.0.0.1:0",
		Cooldown:              1,
		RecoveryWait:          1,
		ResponseHeaderTimeout: 1,
		Providers: []Provider{
			{Name: "wide", BaseURL: "https://wide.example/v1", APIKey: "k",
				ModelAliases: map[string]string{"gonka": "m", "bench": "m"}, Priority: 100},
			{Name: "narrow", BaseURL: "https://narrow.example/v1", APIKey: "k",
				ModelAliases: map[string]string{"gonka": "m"}, Priority: 50},
		},
		ModelRoutes: map[string]ModelRoute{
			"gonka": {Providers: []string{"wide", "narrow"}},
			"bench": {Providers: []string{"wide", "narrow"}},
		},
		ModelRouteOrder: []string{"gonka", "bench"},
	}
	got, dropped, err := FilterDisabledProviders(cfg, []string{"wide"})
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if len(dropped) != 1 || dropped[0] != "bench" {
		t.Fatalf("dropped routes = %v, want [bench]", dropped)
	}
	if _, exists := got.ModelRoutes["bench"]; exists {
		t.Fatal("bench route still present although nobody serves it")
	}
	if route := got.ModelRoutes["gonka"]; len(route.Providers) != 1 || route.Providers[0] != "narrow" {
		t.Fatalf("gonka route = %v, want [narrow]", route.Providers)
	}
}

// TestFilterDisabledProvidersFallbackCleared covers fallbacks pointing at a
// dropped route: they are cleared so the surviving route keeps working
// instead of failing validation with an unknown-fallback error.
func TestFilterDisabledProvidersFallbackCleared(t *testing.T) {
	cfg := Config{
		ListenAddress:         "127.0.0.1:0",
		Cooldown:              1,
		RecoveryWait:          1,
		ResponseHeaderTimeout: 1,
		Providers: []Provider{
			{Name: "main", BaseURL: "https://main.example/v1", APIKey: "k",
				ModelAliases: map[string]string{"gonka": "m", "bench": "m"}, Priority: 100},
			{Name: "only-bench", BaseURL: "https://bench.example/v1", APIKey: "k",
				ModelAliases: map[string]string{"bench": "m"}, Priority: 50},
		},
		ModelRoutes: map[string]ModelRoute{
			"gonka": {Providers: []string{"main"}, Fallback: "bench"},
			"bench": {Providers: []string{"only-bench"}},
		},
		ModelRouteOrder: []string{"gonka", "bench"},
	}
	got, dropped, err := FilterDisabledProviders(cfg, []string{"only-bench"})
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if len(dropped) != 1 || dropped[0] != "bench" {
		t.Fatalf("dropped routes = %v, want [bench]", dropped)
	}
	if route := got.ModelRoutes["gonka"]; route.Fallback != "" {
		t.Fatalf("gonka fallback = %q, want cleared", route.Fallback)
	}
}
