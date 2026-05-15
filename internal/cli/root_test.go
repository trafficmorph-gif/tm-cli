package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// TestOfflineCommandsWorkWithoutApiKey locks in that the standard
// first-run shell-setup flow works on a fresh install:
//
//	tm completion bash >> ~/.bashrc
//	tm version
//	tm help
//
// All of these must succeed BEFORE the user has set TM_API_KEY,
// otherwise the install instructions in the README contradict the
// CLI's behavior. The bug this guards against: a previous version
// of finalizeConfig hardcoded `if cmd.Name() == "version"` to skip
// auth validation, which incorrectly caught cobra-auto-generated
// `completion` / `help` subcommands in the auth gate.
func TestOfflineCommandsWorkWithoutApiKey(t *testing.T) {
	// Clear any ambient env that would mask the bug.
	t.Setenv(EnvAPIKey, "")
	t.Setenv(EnvBaseURL, "")

	cases := [][]string{
		{"version"},
		{"completion", "bash"},
		{"completion", "zsh"},
		{"completion", "fish"},
		{"completion", "powershell"},
		{"help"},
		{"--help"},
		{"profiles", "--help"},      // parent group: no leaf invoked
		{"runs", "--help"},          // parent group: no leaf invoked
		{"history", "--help"},       // parent group: no leaf invoked
		{"runs", "start", "--help"}, // leaf needs auth but --help should print without checking
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			// Reset the package-global config so a previous test
			// case can't contaminate this one (cobra's bind ties
			// flag parsing to module-level state).
			config = Config{}

			root := newRootCmd()
			root.SetArgs(args)
			var stdout, stderr bytes.Buffer
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			if err := root.Execute(); err != nil {
				t.Fatalf("expected %v to succeed without API key, got: %v\nstderr: %s",
					args, err, stderr.String())
			}
		})
	}
}

// TestApiCommandStillRequiresApiKey is the dual check —
// finalizeConfig must continue to REJECT the API-bound leaves when
// no key is set. The most common regression for the offline fix
// above is over-relaxing the gate and accidentally letting
// network commands through; this test makes sure that doesn't
// happen silently.
func TestApiCommandStillRequiresApiKey(t *testing.T) {
	t.Setenv(EnvAPIKey, "")
	t.Setenv(EnvBaseURL, "")

	cases := [][]string{
		{"profiles", "list"},
		{"runs", "start", "1"},
		{"runs", "stop", "1"},
		{"history", "get", "1"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			config = Config{}

			root := newRootCmd()
			root.SetArgs(args)
			// Swallow output — we only care about the error.
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})
			err := root.Execute()
			if err == nil {
				t.Fatalf("expected %v to fail without API key, got success", args)
			}
			if !strings.Contains(err.Error(), "API key is required") {
				t.Fatalf("expected API-key error for %v, got: %v", args, err)
			}
		})
	}
}

func TestMain(m *testing.M) {
	// Ensure tests run with a clean env regardless of the caller's
	// shell — Setenv per-test restores its specific keys but
	// doesn't catch unrelated TM_* vars that may have been set.
	os.Unsetenv(EnvAPIKey)
	os.Unsetenv(EnvBaseURL)
	os.Exit(m.Run())
}
