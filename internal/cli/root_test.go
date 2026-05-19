package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
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

// TestApiCommandRequiresBaseURL — once the API key is supplied,
// the missing-base-URL case must also error cleanly (was previously
// papered over by a placeholder default that didn't resolve).
func TestApiCommandRequiresBaseURL(t *testing.T) {
	t.Setenv(EnvAPIKey, "tm_test")
	t.Setenv(EnvBaseURL, "")

	config = Config{}
	root := newRootCmd()
	root.SetArgs([]string{"profiles", "list"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected base-URL error, got success")
	}
	msg := err.Error()
	if !strings.Contains(msg, "base URL is required") {
		t.Fatalf("expected 'base URL is required' error, got: %v", err)
	}
	if !strings.Contains(msg, EnvBaseURL) {
		t.Fatalf("error should name the %s env var; got: %v", EnvBaseURL, err)
	}
}

// TestApiCommandRejectsMalformedBaseURL locks in fail-fast for the
// caller-side mistakes that used to fail late at request time with
// `unsupported protocol scheme` or similar opaque transport errors.
func TestApiCommandRejectsMalformedBaseURL(t *testing.T) {
	t.Setenv(EnvAPIKey, "tm_test")

	cases := []struct {
		name string
		base string
		want string
	}{
		{"missing scheme", "localhost:8080", "scheme"},
		{"wrong scheme", "ftp://example.com", "http or https"},
		{"no host", "https://", "host"},
		{"query string", "https://example.com/?x=1", "query"},
		{"fragment", "https://example.com/#frag", "fragment"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(EnvBaseURL, c.base)
			config = Config{}
			root := newRootCmd()
			root.SetArgs([]string{"profiles", "list"})
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})
			err := root.Execute()
			if err == nil {
				t.Fatalf("expected error for %q, got success", c.base)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q should mention %q", err.Error(), c.want)
			}
		})
	}
}

// TestApiCommandRejectsInvalidApiKeyChars — header-byte validation
// catches CR/LF/NUL/control bytes in the API key at config-parse
// time rather than letting them fail deep inside net/http with
// "invalid header field value".
func TestApiCommandRejectsInvalidApiKeyChars(t *testing.T) {
	// NUL byte is omitted here — Go's t.Setenv refuses to set
	// env values containing NUL, and the validator's unit tests
	// (baseurl_test.go) already cover that path.
	cases := []struct {
		name string
		key  string
		want string
	}{
		{"carriage return", "tm_x\rmore", "carriage return"},
		{"newline", "tm_x\nInjected: yes", "newline"},
		{"DEL byte", "tm_x\x7f", "DEL"},
		{"SOH (0x01)", "tm_x\x01", "0x01"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(EnvAPIKey, c.key)
			t.Setenv(EnvBaseURL, "https://example.com")
			config = Config{}
			root := newRootCmd()
			root.SetArgs([]string{"profiles", "list"})
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})
			err := root.Execute()
			if err == nil {
				t.Fatalf("expected error for key %q, got success", c.key)
			}
			if !strings.Contains(err.Error(), "api-key") {
				t.Errorf("error should mention --api-key; got: %v", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q should mention %q", err.Error(), c.want)
			}
		})
	}
}

// TestApiCommandNormalizesBaseURL — a base URL without a trailing
// slash gets normalized for path-prefix preservation; a base URL
// with %2F preserves the encoding (per RFC 3986, /a%2Fb ≠ /a/b).
func TestApiCommandNormalizesBaseURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://example.com", "https://example.com/"},
		{"https://example.com/", "https://example.com/"},
		{"https://example.com/proxy", "https://example.com/proxy/"},
		{"https://example.com/a%2Fb", "https://example.com/a%2Fb/"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			t.Setenv(EnvAPIKey, "tm_test")
			t.Setenv(EnvBaseURL, c.in)
			config = Config{}
			root := newRootCmd()
			// `runs stop` is auth-required but accepts arg parsing
			// without making a network call until later — finalizeConfig
			// fires first in PersistentPreRunE. Use it as a cheap
			// hook to exercise the validation pipeline.
			root.SetArgs([]string{"runs", "stop", "1", "--help"})
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})
			// --help skips finalizeConfig entirely; we need to
			// invoke a path that actually runs it. Run the real
			// finalizeConfig directly to verify normalization.
			config.APIKey = "tm_test"
			config.BaseURL = c.in
			cmd := &cobra.Command{}
			cmd.Annotations = authRequired()
			if err := finalizeConfig(cmd); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if config.BaseURL != c.want {
				t.Errorf("BaseURL: got %q, want %q", config.BaseURL, c.want)
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
