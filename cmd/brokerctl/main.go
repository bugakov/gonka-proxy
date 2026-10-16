// Package brokerctl manages the provider on/off state file that gonka-proxy
// merges on top of its YAML configuration at startup.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/glaicer/gonka-proxy/internal/config"
)

const defaultStatePath = "provider-state.json"

const usage = `Usage: brokerctl <command> [flags]

Commands:
  list                  show every configured provider and its state
  disable <name>...     disable one or more providers
  enable  <name>...     enable (re-admit) one or more providers

Flags:
  -config string        path to config.yaml (default "config.yaml")
  -state string         path to the provider state file (default "provider-state.json")

After changing the state file, restart the proxy to apply it:
  oxmgr restart gonka-proxy
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	command := os.Args[1]
	args := os.Args[2:]

	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("config", "config.yaml", "path to config.yaml")
	statePath := flags.String("state", defaultStatePath, "path to the provider state file")
	if err := flags.Parse(args); err != nil {
		os.Exit(2)
	}
	rest := flags.Args()

	if err := runCommand(command, rest, *configPath, *statePath, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runCommand(command string, args []string, configPath, statePath string, out io.Writer) error {
	switch command {
	case "list":
		if len(args) != 0 {
			return fmt.Errorf("list takes no arguments")
		}
		return runList(configPath, statePath, out)
	case "disable", "enable":
		if len(args) == 0 {
			return fmt.Errorf("%s requires at least one provider name", command)
		}
		return runToggle(command, args, configPath, statePath, out)
	case "-h", "--help", "help":
		fmt.Fprint(out, usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n%s", command, usage)
	}
}

func runList(configPath, statePath string, out io.Writer) error {
	names, err := configuredProviderNames(configPath)
	if err != nil {
		return err
	}
	state, err := config.LoadProviderState(statePath)
	if err != nil {
		return err
	}
	disabled := make(map[string]struct{}, len(state.Disabled))
	for _, name := range state.Disabled {
		disabled[name] = struct{}{}
	}
	for _, name := range names {
		status := "enabled"
		if _, ok := disabled[name]; ok {
			status = "DISABLED"
		}
		fmt.Fprintf(out, "%-20s %s\n", name, status)
	}
	if len(state.Disabled) > 0 {
		stale := make([]string, 0, len(state.Disabled))
		configured := make(map[string]struct{}, len(names))
		for _, name := range names {
			configured[name] = struct{}{}
		}
		for _, name := range state.Disabled {
			if _, ok := configured[name]; !ok {
				stale = append(stale, name)
			}
		}
		if len(stale) > 0 {
			sort.Strings(stale)
			fmt.Fprintf(out, "\nnote: state file disables unknown providers: %s\n", strings.Join(stale, ", "))
		}
	}
	return nil
}

func runToggle(command string, names []string, configPath, statePath string, out io.Writer) error {
	configured, err := configuredProviderNames(configPath)
	if err != nil {
		return err
	}
	known := make(map[string]struct{}, len(configured))
	for _, name := range configured {
		known[name] = struct{}{}
	}
	for _, name := range names {
		if _, ok := known[name]; !ok {
			return fmt.Errorf("unknown provider %q (configured: %s)", name, strings.Join(configured, ", "))
		}
	}

	state, err := config.LoadProviderState(statePath)
	if err != nil {
		return err
	}
	set := make(map[string]struct{}, len(state.Disabled))
	for _, name := range state.Disabled {
		set[name] = struct{}{}
	}
	for _, name := range names {
		if command == "disable" {
			set[name] = struct{}{}
		} else {
			delete(set, name)
		}
	}
	state.Disabled = make([]string, 0, len(set))
	for name := range set {
		state.Disabled = append(state.Disabled, name)
	}
	sort.Strings(state.Disabled)

	if err := config.SaveProviderState(statePath, state); err != nil {
		return err
	}
	for _, name := range names {
		fmt.Fprintf(out, "%s %sd\n", name, command)
	}
	fmt.Fprintln(out, "restart the proxy to apply: oxmgr restart gonka-proxy")
	return nil
}

func configuredProviderNames(configPath string) ([]string, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config %q: %w", configPath, err)
	}
	names := make([]string, 0, len(cfg.Providers))
	for _, provider := range cfg.Providers {
		names = append(names, provider.Name)
	}
	sort.Strings(names)
	return names, nil
}
