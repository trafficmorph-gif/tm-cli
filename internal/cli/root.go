// Package cli wires the cobra command tree, configuration, and
// authenticated API client for the `tm` binary.
//
// Layout:
//
//	root.go        — cobra root, global flags, config loading
//	auth.go        — RequestEditor that injects X-Api-Key
//	version.go     — `tm version`
//	profiles.go    — `tm profiles list|get`
//	runs.go        — `tm runs start [--wait]|stop|pause|resume`
//	history.go     — `tm history get`
//	output.go      — JSON / table output helpers
package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/trafficmorph/tm-cli/internal/api"
)

// Config carries the resolved configuration for one CLI invocation —
// base URL of the TrafficMorph instance, API key, and output format
// preference. Populated from environment variables first, then
// overridden by command-line flags so a CI step can ship a baseline
// config via env and tune individual calls via flags.
type Config struct {
	BaseURL string
	APIKey  string
	JSON    bool
	Timeout time.Duration
}

const (
	// EnvBaseURL is the environment variable for the API base URL.
	// Mirrors the CI-friendly convention used by gh, kubectl, etc.
	EnvBaseURL = "TM_BASE_URL"
	// EnvAPIKey carries the API key (full `tm_…` value).
	EnvAPIKey = "TM_API_KEY"

	defaultBaseURL = "https://app.trafficmorph.example.com"
	defaultTimeout = 30 * time.Second

	// annotationNeedsAuth is set on every leaf command that
	// actually performs an API call. finalizeConfig keys off it
	// to decide whether to require --api-key — that lets purely
	// local commands (`version`, the cobra-auto-generated
	// `completion <shell>` and `help <command>`, every parent
	// command that just prints its child list) run without any
	// configuration at all. Critical for first-run setup like
	// `tm completion bash >> ~/.bashrc` before the user has
	// provisioned a key.
	annotationNeedsAuth = "tm/needs_auth"
)

// authRequired returns an annotations map opting a command into
// API-key validation in PersistentPreRunE. Attach to every leaf
// command that makes an HTTP call; omit for offline-only commands.
func authRequired() map[string]string {
	return map[string]string{annotationNeedsAuth: "true"}
}

// global config singleton, populated by PersistentPreRun on the
// root command. Subcommands read from this directly rather than
// passing a *Config through every cobra.Command — cobra's design
// makes that threading awkward.
var config Config

// Execute is the entrypoint called by cmd/tm/main.go. Builds the
// command tree, parses args, runs the matched subcommand.
func Execute() error {
	root := newRootCmd()
	return root.Execute()
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "tm",
		Short:         "TrafficMorph CLI — drive runs, fetch history, import captures, gate CI builds",
		Long:          "TrafficMorph CLI talks to the public /api/v1 surface to start traffic runs, fetch their regression verdicts, import JSONL captures, and gate CI builds on pass/fail/warn outcomes.",
		SilenceUsage:  true, // Don't dump usage on every command-execution error.
		SilenceErrors: true, // main() prints the error; cobra would duplicate it.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return finalizeConfig(cmd)
		},
	}

	root.PersistentFlags().StringVar(&config.BaseURL, "base-url", os.Getenv(EnvBaseURL),
		fmt.Sprintf("TrafficMorph base URL (default $%s or %s)", EnvBaseURL, defaultBaseURL))
	root.PersistentFlags().StringVar(&config.APIKey, "api-key", os.Getenv(EnvAPIKey),
		fmt.Sprintf("API key prefixed with tm_ (default $%s)", EnvAPIKey))
	root.PersistentFlags().BoolVar(&config.JSON, "json", false, "Emit machine-readable JSON instead of human tables")
	root.PersistentFlags().DurationVar(&config.Timeout, "timeout", defaultTimeout, "HTTP request timeout for a single call")

	root.AddCommand(newVersionCmd())
	root.AddCommand(newProfilesCmd())
	root.AddCommand(newRunsCmd())
	root.AddCommand(newHistoryCmd())
	return root
}

// finalizeConfig fills in defaults and validates the auth/URL config.
// Called from PersistentPreRunE so every subcommand can assume the
// config is usable.
//
// Validation is opt-in via the {@code annotationNeedsAuth} marker:
// commands that don't perform network calls (the cobra-auto-generated
// `completion <shell>` and `help <command>`, every parent group like
// `tm profiles` that just prints child help, plus the explicit
// `tm version`) skip the API-key check entirely. Without this,
// first-run flows like `tm completion bash >> ~/.bashrc` would fail
// before the user has even read the README's auth section.
func finalizeConfig(cmd *cobra.Command) error {
	if config.BaseURL == "" {
		config.BaseURL = defaultBaseURL
	}
	if cmd.Annotations[annotationNeedsAuth] != "true" {
		return nil
	}
	if config.APIKey == "" {
		return fmt.Errorf("API key is required; set $%s or pass --api-key", EnvAPIKey)
	}
	return nil
}

// newClient builds a typed API client with the X-Api-Key header
// auto-injected on every request. Reads from the global config
// populated by PersistentPreRunE.
func newClient() (*api.Client, error) {
	return api.NewClient(config.BaseURL,
		api.WithRequestEditorFn(apiKeyRequestEditor(config.APIKey)))
}

// shortCtx returns a Context with the per-call timeout from config.
// Subcommand handlers should use it for every API call so a hung
// server doesn't leave the CLI spinning forever — particularly
// important inside the `runs start --wait` polling loop.
func shortCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), config.Timeout)
}
