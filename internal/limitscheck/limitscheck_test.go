package limitscheck

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/glaicer/gonka-proxy/internal/config"
)

// modelsHandler serves a fixed /models payload on /v1/models.
func modelsHandler(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(server.Close)
	return server
}

// withLimits decorates a Provider with declared per-model limits.
func withLimits(limits map[string]config.ModelLimit, provider config.Provider) config.Provider {
	provider.ModelLimits = limits
	return provider
}

// routeConfig builds a Config whose single route-model is served by the given
// providers.
func routeConfig(providers []config.Provider) config.Config {
	return config.Config{
		Providers:       providers,
		ModelRoutes:     map[string]config.ModelRoute{"route-model": {}},
		ModelRouteOrder: []string{"route-model"},
	}
}

func TestRunVerifiesMatchingFloors(t *testing.T) {
	server := modelsHandler(t, `{"data":[
		{"id":"upstream-a","context_length":200000,"max_output_tokens":8192},
		{"id":"upstream-b","context_length":131072,"max_output_tokens":4096}
	]}`)
	cfg := routeConfig([]config.Provider{
		withLimits(map[string]config.ModelLimit{"route-model": {Context: 200000, Output: 8192}}, config.Provider{
			Name: "a", BaseURL: server.URL + "/v1", APIKey: "secret", ModelAliases: map[string]string{"route-model": "upstream-a"}, Priority: 1,
		}),
		withLimits(map[string]config.ModelLimit{"route-model": {Context: 131072, Output: 4096}}, config.Provider{
			Name: "b", BaseURL: server.URL + "/v1", APIKey: "secret", ModelAliases: map[string]string{"route-model": "upstream-b"}, Priority: 1,
		}),
	})
	report, err := Run(context.Background(), cfg, Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(report.Routes) != 1 {
		t.Fatalf("routes = %d, want 1", len(report.Routes))
	}
	route := report.Routes[0]
	if route.Model != "route-model" {
		t.Fatalf("model = %q, want route-model", route.Model)
	}
	// The published floor is the declared minimum across serving providers.
	if route.PublishedContext != 131072 || route.PublishedOutput != 4096 {
		t.Fatalf("published = %d/%d, want 131072/4096", route.PublishedContext, route.PublishedOutput)
	}
	if route.LiveContext != 131072 || route.LiveOutput != 4096 {
		t.Fatalf("live = %d/%d, want 131072/4096", route.LiveContext, route.LiveOutput)
	}
	if !route.ContextMatch || !route.OutputMatch {
		t.Fatalf("route = %#v, want both dimensions matching", route)
	}
	if !route.ContextVerifiable || !route.OutputVerifiable {
		t.Fatalf("route = %#v, want both dimensions fully verifiable", route)
	}
	if len(report.Failures()) != 0 {
		t.Fatalf("failures = %#v, want none", report.Failures())
	}
}

func TestRunFailsWhenTheNarrowestProviderOverDeclares(t *testing.T) {
	// Provider b declares 160000 but actually advertises 140000 on its live
	// /models. Because b is the narrowest declared provider, the published
	// floor (160000) exceeds what b can really serve — an unsafe config.
	server := modelsHandler(t, `{"data":[
		{"id":"upstream-a","context_length":200000,"max_output_tokens":8192},
		{"id":"upstream-b","context_length":140000,"max_output_tokens":4096}
	]}`)
	cfg := routeConfig([]config.Provider{
		withLimits(map[string]config.ModelLimit{"route-model": {Context: 200000, Output: 8192}}, config.Provider{
			Name: "a", BaseURL: server.URL + "/v1", APIKey: "secret", ModelAliases: map[string]string{"route-model": "upstream-a"}, Priority: 1,
		}),
		withLimits(map[string]config.ModelLimit{"route-model": {Context: 160000, Output: 16384}}, config.Provider{
			Name: "b", BaseURL: server.URL + "/v1", APIKey: "secret", ModelAliases: map[string]string{"route-model": "upstream-b"}, Priority: 1,
		}),
	})
	report, err := Run(context.Background(), cfg, Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	route := report.Routes[0]
	if route.PublishedContext != 160000 {
		t.Fatalf("published = %d, want 160000", route.PublishedContext)
	}
	if route.LiveContext != 140000 {
		t.Fatalf("live = %d, want 140000", route.LiveContext)
	}
	if route.ContextMatch || route.OutputMatch {
		t.Fatalf("route = %#v, want both dimensions failing", route)
	}
	if len(route.ContextOver) != 1 || route.ContextOver[0] != "b" {
		t.Fatalf("context_over = %#v, want [b]", route.ContextOver)
	}
	if len(route.OutputOver) != 1 || route.OutputOver[0] != "b" {
		t.Fatalf("output_over = %#v, want [b]", route.OutputOver)
	}
	if len(report.Failures()) != 4 {
		t.Fatalf("failures = %#v, want the dimension mismatches and both over-declarations", report.Failures())
	}
}

func TestRunTreatsUnknownLiveCapacityAsNotFullyVerifiable(t *testing.T) {
	// Provider b serves the route but its /models replies 404, so its live
	// capacity is unknown. The route cannot be fully verified, but nothing
	// contradicts the declared floor, so it should not hard-fail.
	ok := modelsHandler(t, `{"data":[{"id":"upstream-a","context_length":200000,"max_output_tokens":8192}]}`)
	missing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no models", http.StatusNotFound)
	}))
	t.Cleanup(missing.Close)

	cfg := routeConfig([]config.Provider{
		withLimits(map[string]config.ModelLimit{"route-model": {Context: 131072, Output: 4096}}, config.Provider{
			Name: "a", BaseURL: ok.URL + "/v1", APIKey: "secret", ModelAliases: map[string]string{"route-model": "upstream-a"}, Priority: 1,
		}),
		withLimits(map[string]config.ModelLimit{"route-model": {Context: 150000, Output: 4096}}, config.Provider{
			Name: "b", BaseURL: missing.URL + "/v1", APIKey: "secret", ModelAliases: map[string]string{"route-model": "upstream-b"}, Priority: 1,
		}),
	})
	report, err := Run(context.Background(), cfg, Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	route := report.Routes[0]
	if route.ContextVerifiable || route.OutputVerifiable {
		t.Fatalf("route = %#v, want both dimensions not fully verifiable", route)
	}
	if !route.ContextMatch || !route.OutputMatch {
		t.Fatalf("route = %#v, want unknown dimensions to match by default", route)
	}
	if len(route.ContextUnknown) != 1 || route.ContextUnknown[0] != "b" {
		t.Fatalf("context_unknown = %#v, want [b]", route.ContextUnknown)
	}
	if len(report.Failures()) != 0 {
		t.Fatalf("failures = %#v, want none", report.Failures())
	}
}

func TestRunRespectsExplicitRouteProviderList(t *testing.T) {
	// Provider "excluded" serves route-model but is not in the route's provider
	// list, so it must be ignored even though it advertises less than the floor.
	small := modelsHandler(t, `{"data":[{"id":"small","context_length":8000,"max_output_tokens":1000}]}`)
	large := modelsHandler(t, `{"data":[{"id":"large","context_length":200000,"max_output_tokens":8192}]}`)
	cfg := config.Config{
		Providers: []config.Provider{
			withLimits(map[string]config.ModelLimit{"route-model": {Context: 131072, Output: 4096}}, config.Provider{
				Name: "excluded", BaseURL: small.URL + "/v1", APIKey: "secret", ModelAliases: map[string]string{"route-model": "small"}, Priority: 1,
			}),
			withLimits(map[string]config.ModelLimit{"route-model": {Context: 200000, Output: 8192}}, config.Provider{
				Name: "included", BaseURL: large.URL + "/v1", APIKey: "secret", ModelAliases: map[string]string{"route-model": "large"}, Priority: 1,
			}),
		},
		ModelRoutes: map[string]config.ModelRoute{
			"route-model": {Providers: []string{"included"}},
		},
		ModelRouteOrder: []string{"route-model"},
	}
	report, err := Run(context.Background(), cfg, Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	route := report.Routes[0]
	if len(route.ServingProviders) != 1 || route.ServingProviders[0] != "included" {
		t.Fatalf("serving = %#v, want [included]", route.ServingProviders)
	}
	if route.LiveContext != 200000 || route.LiveOutput != 8192 {
		t.Fatalf("live = %d/%d, want 200000/8192", route.LiveContext, route.LiveOutput)
	}
	if !route.ContextMatch || !route.OutputMatch {
		t.Fatalf("route = %#v, want matching when the excluded provider is ignored", route)
	}
}

func TestRunHandlesLegacyGonkaRoute(t *testing.T) {
	server := modelsHandler(t, `{"data":[{"id":"legacy-upstream","context_length":200000,"max_output_tokens":8192}]}`)
	cfg := config.Config{
		Providers: []config.Provider{
			withLimits(map[string]config.ModelLimit{"gonka": {Context: 200000, Output: 8192}}, config.Provider{
				Name: "legacy", BaseURL: server.URL + "/v1", APIKey: "secret", ModelAlias: "legacy-upstream", Priority: 1,
			}),
		},
	}
	report, err := Run(context.Background(), cfg, Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(report.Routes) != 1 || report.Routes[0].Model != "gonka" {
		t.Fatalf("routes = %#v, want the implicit legacy gonka route", report.Routes)
	}
	route := report.Routes[0]
	if route.LiveContext != 200000 || !route.ContextMatch || !route.OutputMatch {
		t.Fatalf("route = %#v, want matching legacy floor", route)
	}
}

func TestProbeProviderReadsCapacityAcrossFieldVariants(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload any
		switch r.URL.Path {
		case "/ctx/models":
			payload = []map[string]any{
				{"id": "a", "context_window": 160000},
				{"id": "b", "max_context_length": 4096},
				{"id": "c", "model_max_length": 32768, "max_total_tokens": 100000},
			}
		case "/out/models":
			payload = []map[string]any{
				{"id": "a", "max_tokens": 128000},
				{"id": "b", "max_output_length": 1024},
			}
		default:
			http.NotFound(w, r)
			return
		}
		writeModels(t, w, payload)
	}))
	t.Cleanup(server.Close)

	check := probeProvider(context.Background(), &http.Client{Timeout: time.Second}, config.Provider{
		Name: "variants", BaseURL: server.URL + "/ctx", APIKey: "secret", ModelAlias: "ignored", Priority: 1,
	})
	if check.Status != "healthy" {
		t.Fatalf("status = %q (%s), want healthy", check.Status, check.Error)
	}
	if got := check.Live["a"].Context; got != 160000 {
		t.Fatalf("context_window = %d, want 160000", got)
	}
	if got := check.Live["b"].Context; got != 4096 {
		t.Fatalf("max_context_length = %d, want 4096", got)
	}
	if got := check.Live["c"].Context; got != 32768 {
		t.Fatalf("model_max_length precedence = %d, want 32768 over max_total_tokens", got)
	}

	check = probeProvider(context.Background(), &http.Client{Timeout: time.Second}, config.Provider{
		Name: "variants", BaseURL: server.URL + "/out", APIKey: "secret", ModelAlias: "ignored", Priority: 1,
	})
	if got := check.Live["a"].Output; got != 128000 {
		t.Fatalf("max_tokens = %d, want 128000", got)
	}
	if got := check.Live["b"].Output; got != 1024 {
		t.Fatalf("max_output_length = %d, want 1024", got)
	}
}

func writeModels(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"data": payload}); err != nil {
		t.Fatalf("encode models: %v", err)
	}
}

func TestRunReportsProviderStatuses(t *testing.T) {
	ok := modelsHandler(t, `{"data":[{"id":"upstream-a","context_length":1000}]}`)
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(down.Close)

	cfg := routeConfig([]config.Provider{
		withLimits(map[string]config.ModelLimit{"route-model": {Context: 1000}}, config.Provider{
			Name: "healthy", BaseURL: ok.URL + "/v1", APIKey: "secret", ModelAliases: map[string]string{"route-model": "upstream-a"}, Priority: 1,
		}),
		withLimits(map[string]config.ModelLimit{"route-model": {Context: 1000}}, config.Provider{
			Name: "down", BaseURL: down.URL + "/v1", APIKey: "secret", ModelAliases: map[string]string{"route-model": "upstream-b"}, Priority: 1,
		}),
	})
	report, err := Run(context.Background(), cfg, Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	byName := map[string]ProviderCheck{}
	for _, provider := range report.Providers {
		byName[provider.Name] = provider
	}
	if byName["healthy"].Status != "healthy" {
		t.Fatalf("healthy = %#v, want status healthy", byName["healthy"])
	}
	if byName["down"].Status != "unavailable" || !strings.Contains(byName["down"].Error, "http-") {
		t.Fatalf("down = %#v, want unavailable with an http error", byName["down"])
	}
}

func TestReportFailuresListOverDeclaredProvidersSorted(t *testing.T) {
	server := modelsHandler(t, `{"data":[
		{"id":"za","context_length":100000,"max_output_tokens":8192},
		{"id":"zb","context_length":90000,"max_output_tokens":4000}
	]}`)
	cfg := routeConfig([]config.Provider{
		withLimits(map[string]config.ModelLimit{"route-model": {Context: 200000, Output: 16000}}, config.Provider{
			Name: "bz", BaseURL: server.URL + "/v1", APIKey: "secret", ModelAliases: map[string]string{"route-model": "zb"}, Priority: 1,
		}),
		withLimits(map[string]config.ModelLimit{"route-model": {Context: 250000, Output: 20000}}, config.Provider{
			Name: "ax", BaseURL: server.URL + "/v1", APIKey: "secret", ModelAliases: map[string]string{"route-model": "za"}, Priority: 1,
		}),
	})
	report, err := Run(context.Background(), cfg, Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	route := report.Routes[0]
	if len(route.ContextOver) != 2 {
		t.Fatalf("context_over = %#v, want both providers", route.ContextOver)
	}
	if route.ContextOver[0] != "ax" || route.ContextOver[1] != "bz" {
		t.Fatalf("context_over = %#v, want sorted [ax bz]", route.ContextOver)
	}
	if !sort.StringsAreSorted(route.OutputOver) {
		t.Fatalf("output_over = %#v, want sorted", route.OutputOver)
	}
	if len(report.Failures()) != 6 {
		t.Fatalf("failures = %#v, want 2 mismatches + 4 over-declarations", report.Failures())
	}
}

func TestRunNoProvidersDoesNotPanic(t *testing.T) {
	report, err := Run(context.Background(), config.Config{}, Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(report.Routes) != 0 {
		t.Fatalf("routes = %#v, want none", report.Routes)
	}
	if len(report.Failures()) != 0 {
		t.Fatalf("failures = %#v, want none", report.Failures())
	}
}
