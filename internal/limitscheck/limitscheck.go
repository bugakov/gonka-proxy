// Package limitscheck verifies that the model_limits floors a proxy publishes
// through /v1/models match what its providers actually advertise, by probing
// each provider's /models endpoint with a safe GET. It never sends chat
// messages, prompts, tools, or request bodies.
package limitscheck

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/glaicer/gonka-proxy/internal/config"
)

// Capacity is one upstream model's advertised capacity from its /models
// response. A zero field means the provider published nothing for that
// dimension.
type Capacity struct {
	Context int
	Output  int
}

// ProviderCheck records one provider's /models probe and the capacities it
// advertised, keyed by upstream model ID.
type ProviderCheck struct {
	Name   string
	Status string
	Error  string
	Live   map[string]Capacity
}

// RouteReport compares the floor the proxy publishes for one Virtual Model
// (the minimum declared context/output among the providers that serve it) with
// the floor derived from the capacities live providers actually advertise.
//
// ContextMatch/OutputMatch are true when no provider that served the route and
// returned a live capacity can be contradicted. A route is not fully verified
// when some serving provider (or the route itself) is absent from the probe —
// that combination means the published floor is at best the declared minimum,
// and the live floor is only the minimum of the providers data was obtained
// from. A provider named in ContextOver/OutputOver declares more for a dimension
// than its live /models advertises: stale configuration that cannot lower the
// published floor itself but should not be copied forward. A provider named in
// ContextUnknown/OutputUnknown serves the route but its live capacity could not
// be obtained, so it stays out of the live floor.
type RouteReport struct {
	Model             string
	ServingProviders  []string
	PublishedContext  int
	PublishedOutput   int
	LiveContext       int
	LiveOutput        int
	ContextMatch      bool
	OutputMatch       bool
	ContextVerifiable bool
	OutputVerifiable  bool
	ContextOver       []string
	OutputOver        []string
	ContextUnknown    []string
	OutputUnknown     []string
	ContextVerified   int
	OutputVerified    int
}

// Report is the full result of one verification run.
type Report struct {
	GeneratedAt time.Time
	Providers   []ProviderCheck
	Routes      []RouteReport
}

// Options controls a verification run.
type Options struct {
	Timeout time.Duration
	Client  *http.Client
}

// Run probes every provider's /models endpoint and verifies the configured
// model_limits floors against what those providers actually advertise.
func Run(ctx context.Context, cfg config.Config, options Options) (Report, error) {
	options = options.withDefaults()
	report := Report{GeneratedAt: time.Now().UTC()}
	for _, provider := range cfg.Providers {
		report.Providers = append(report.Providers, probeProvider(ctx, options.Client, provider))
	}
	for _, model := range routeModels(cfg) {
		report.Routes = append(report.Routes, checkRoute(cfg, routeProviders(cfg, model), model, report.Providers))
	}
	return report, nil
}

func (o Options) withDefaults() Options {
	if o.Timeout <= 0 {
		o.Timeout = 20 * time.Second
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: o.Timeout}
	}
	return o
}

func probeProvider(ctx context.Context, client *http.Client, provider config.Provider) ProviderCheck {
	check := ProviderCheck{Name: provider.Name, Status: "error", Live: map[string]Capacity{}}
	endpoint := strings.TrimRight(provider.BaseURL, "/") + "/models"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		check.Error = "request-create"
		return check
	}
	request.Header.Set("Authorization", "Bearer "+provider.APIKey)
	response, err := client.Do(request)
	if err != nil {
		check.Error = classifyProbeError(err)
		return check
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024))
	if err != nil {
		check.Error = "response-read"
		return check
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		check.Status = "unavailable"
		check.Error = fmt.Sprintf("http-%d", response.StatusCode)
		return check
	}
	var payload struct {
		Data []struct {
			ID               string `json:"id"`
			ContextLength    *int   `json:"context_length"`
			ContextWindow    *int   `json:"context_window"`
			MaxContextLength *int   `json:"max_context_length"`
			ModelMaxLength   *int   `json:"model_max_length"`
			MaxTotalTokens   *int   `json:"max_total_tokens"`
			MaxOutputTokens  *int   `json:"max_output_tokens"`
			MaxTokens        *int   `json:"max_tokens"`
			MaxOutputLength  *int   `json:"max_output_length"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		check.Error = "decode-models"
		return check
	}
	check.Status = "healthy"
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		capacity := Capacity{
			Context: firstPositive(item.ContextLength, item.ContextWindow, item.MaxContextLength, item.ModelMaxLength, item.MaxTotalTokens),
			Output:  firstPositive(item.MaxOutputTokens, item.MaxTokens, item.MaxOutputLength),
		}
		if capacity.Context > 0 || capacity.Output > 0 {
			check.Live[id] = capacity
		}
	}
	return check
}

func firstPositive(values ...*int) int {
	for _, value := range values {
		if value != nil && *value > 0 {
			return *value
		}
	}
	return 0
}

func classifyProbeError(err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(strings.ToLower(err.Error()), "timeout") {
		return "timeout"
	}
	return "transport-error"
}

// routeModels returns the ordered Virtual Models a Config exposes: the
// model_routes order when configured, the sorted route names otherwise, and
// the implicit legacy gonka route only when some Provider actually serves it.
func routeModels(cfg config.Config) []string {
	if len(cfg.ModelRouteOrder) > 0 {
		return cfg.ModelRouteOrder
	}
	if len(cfg.ModelRoutes) > 0 {
		names := make([]string, 0, len(cfg.ModelRoutes))
		for name := range cfg.ModelRoutes {
			names = append(names, name)
		}
		sort.Strings(names)
		return names
	}
	for _, provider := range cfg.Providers {
		if _, ok := provider.ModelAliasFor(config.LegacyVirtualModel); ok {
			return []string{config.LegacyVirtualModel}
		}
	}
	return nil
}

// routeProviders lists the Providers that serve one Virtual Model, using the
// same alias rule as the proxy: the route's explicit provider list when set,
// otherwise every Provider with an alias for the route.
func routeProviders(cfg config.Config, model string) []config.Provider {
	if len(cfg.ModelRoutes) > 0 {
		if route, ok := cfg.ModelRoutes[model]; ok && len(route.Providers) > 0 {
			byName := make(map[string]config.Provider, len(cfg.Providers))
			for _, provider := range cfg.Providers {
				byName[provider.Name] = provider
			}
			var out []config.Provider
			for _, name := range route.Providers {
				if provider, ok := byName[name]; ok {
					if _, ok := provider.ModelAliasFor(model); ok {
						out = append(out, provider)
					}
				}
			}
			return out
		}
	}
	var out []config.Provider
	for _, provider := range cfg.Providers {
		if _, ok := provider.ModelAliasFor(model); ok {
			out = append(out, provider)
		}
	}
	return out
}

func checkRoute(cfg config.Config, serving []config.Provider, model string, probes []ProviderCheck) RouteReport {
	report := RouteReport{
		Model:            model,
		ServingProviders: providerNames(serving),
	}
	probeByProvider := make(map[string]ProviderCheck, len(probes))
	for _, probe := range probes {
		probeByProvider[probe.Name] = probe
	}

	var liveContexts, liveOutputs []int
	served := len(serving)
	for _, provider := range serving {
		probe := probeByProvider[provider.Name]
		declared := provider.ModelLimits[model]
		upstream, _ := provider.ModelAliasFor(model)
		live := probe.Live[upstream]

		if declared.Context > 0 && (report.PublishedContext == 0 || declared.Context < report.PublishedContext) {
			report.PublishedContext = declared.Context
		}
		if declared.Output > 0 && (report.PublishedOutput == 0 || declared.Output < report.PublishedOutput) {
			report.PublishedOutput = declared.Output
		}

		if live.Context > 0 {
			report.ContextVerified++
			liveContexts = append(liveContexts, live.Context)
			if declared.Context > 0 && declared.Context > live.Context {
				report.ContextOver = append(report.ContextOver, provider.Name)
			}
		} else {
			report.ContextUnknown = append(report.ContextUnknown, provider.Name)
		}

		if live.Output > 0 {
			report.OutputVerified++
			liveOutputs = append(liveOutputs, live.Output)
			if declared.Output > 0 && declared.Output > live.Output {
				report.OutputOver = append(report.OutputOver, provider.Name)
			}
		} else {
			report.OutputUnknown = append(report.OutputUnknown, provider.Name)
		}
	}
	sort.Strings(report.ContextOver)
	sort.Strings(report.OutputOver)
	sort.Strings(report.ContextUnknown)
	sort.Strings(report.OutputUnknown)

	report.LiveContext = minValue(liveContexts)
	report.LiveOutput = minValue(liveOutputs)
	// A dimension matches only when every serving provider that returned a
	// live capacity can actually serve the declared floor, and (once the full
	// pool is verified) the declared minimum equals the live minimum. A route
	// that is only partly verified cannot prove the floor matches the whole
	// live pool, so it fails loudly but is still reported as matching when no
	// verifiable provider contradicts the declared floor.
	report.ContextMatch = noOver(report.ContextOver) &&
		(len(report.ContextUnknown) > 0 || report.PublishedContext == report.LiveContext || report.PublishedContext == 0)
	report.OutputMatch = noOver(report.OutputOver) &&
		(len(report.OutputUnknown) > 0 || report.PublishedOutput == report.LiveOutput || report.PublishedOutput == 0)
	// A route is fully verified only when every serving provider reported a
	// capacity for the dimension.
	report.ContextVerifiable = report.ContextVerified == served && len(liveContexts) > 0
	report.OutputVerifiable = report.OutputVerified == served && len(liveOutputs) > 0
	return report
}

func noOver(over []string) bool {
	return len(over) == 0
}

func providerNames(providers []config.Provider) []string {
	names := make([]string, 0, len(providers))
	for _, provider := range providers {
		names = append(names, provider.Name)
	}
	sort.Strings(names)
	return names
}

func minValue(values []int) int {
	if len(values) == 0 {
		return 0
	}
	minimum := values[0]
	for _, value := range values[1:] {
		if value < minimum {
			minimum = value
		}
	}
	return minimum
}

// Failures lists the routes whose published floor does not match the live
// floor, or where a provider over-declares a dimension, i.e. everything that
// should make a verification run exit non-zero.
func (r Report) Failures() []string {
	var failures []string
	for _, route := range r.Routes {
		if route.ContextVerifiable && !route.ContextMatch {
			failures = append(failures, fmt.Sprintf("%s: published context %d != live minimum %d", route.Model, route.PublishedContext, route.LiveContext))
		}
		if route.OutputVerifiable && !route.OutputMatch {
			failures = append(failures, fmt.Sprintf("%s: published max_output_tokens %d != live minimum %d", route.Model, route.PublishedOutput, route.LiveOutput))
		}
		for _, name := range route.ContextOver {
			failures = append(failures, fmt.Sprintf("%s: %s declares more context than /models advertises", route.Model, name))
		}
		for _, name := range route.OutputOver {
			failures = append(failures, fmt.Sprintf("%s: %s declares more output than /models advertises", route.Model, name))
		}
	}
	return failures
}
