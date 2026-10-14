// Command limits-check verifies that the floors a running proxy publishes
// through /v1/models (the model_limits declared in config.yaml) match what the
// providers actually advertise on their /models endpoints. It probes with a
// safe GET and never sends a prompt, chat message, tools payload, or request
// body, so it is free to run as often as needed.
//
// Exit status is 0 when every published floor matches the live floors and no
// provider over-declares a dimension; 1 otherwise.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/glaicer/gonka-proxy/internal/config"
	"github.com/glaicer/gonka-proxy/internal/limitscheck"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to the YAML configuration file")
	timeout := flag.Duration("timeout", 20*time.Second, "per-provider probe timeout")
	flag.Parse()

	logger := log.New(os.Stdout, "", log.LstdFlags)
	if err := run(*configPath, *timeout, logger); err != nil {
		logger.Printf("error=%v", err)
		os.Exit(1)
	}
}

func run(configPath string, timeout time.Duration, logger *log.Logger) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	ctx, cancel := timedContext(timeout)
	defer cancel()
	report, err := limitscheck.Run(ctx, cfg, limitscheck.Options{Timeout: timeout})
	if err != nil {
		return fmt.Errorf("verify model limits: %w", err)
	}

	fmt.Printf("# model_limits check %s (%s)\n\n", report.GeneratedAt.Format(time.RFC3339), configPath)
	fmt.Println("## providers")
	for _, provider := range report.Providers {
		status := provider.Status
		if provider.Error != "" {
			status += " (" + provider.Error + ")"
		}
		fmt.Printf("  %-24s %-12s %d live model entries\n", provider.Name, status, len(provider.Live))
	}
	fmt.Println()
	fmt.Println("## routes (published floor vs live minimum)")
	for _, route := range report.Routes {
		fmt.Printf("  %-24s published %5d/%-6d live %5d/%-6d %s\n",
			route.Model,
			route.PublishedContext, route.PublishedOutput,
			route.LiveContext, route.LiveOutput,
			routeSummary(route))
	}

	failures := report.Failures()
	if len(failures) == 0 {
		fmt.Println("\nOK: every published floor matches the live minimum.")
		return nil
	}
	for _, failure := range failures {
		logger.Printf("FAIL: %s", failure)
	}
	return fmt.Errorf("%d model_limits floor(s) do not match live capacity", len(failures))
}

func timedContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}

func routeSummary(route limitscheck.RouteReport) string {
	var parts []string
	switch {
	case route.ContextVerifiable && route.ContextMatch:
		parts = append(parts, "context ok")
	case route.ContextVerifiable:
		parts = append(parts, "context MISMATCH")
	default:
		parts = append(parts, "context unknown")
	}
	switch {
	case route.OutputVerifiable && route.OutputMatch:
		parts = append(parts, "output ok")
	case route.OutputVerifiable:
		parts = append(parts, "output MISMATCH")
	default:
		parts = append(parts, "output unknown")
	}
	if len(route.ContextOver) > 0 || len(route.OutputOver) > 0 {
		parts = append(parts, fmt.Sprintf("over-declared: %s", joinProviders(route.ContextOver, route.OutputOver)))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func joinProviders(groups ...[]string) string {
	var names []string
	for _, group := range groups {
		names = append(names, group...)
	}
	return strings.Join(names, ", ")
}
