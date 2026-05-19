package cli

import (
	"strings"
	"testing"
)

// TestValidateBaseURL pins the rejection rules independent of the
// cobra wire-up — direct unit-level checks on every error path.
func TestValidateBaseURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // substring the error must contain (empty → no error expected)
	}{
		{"valid http with port", "http://localhost:8080", ""},
		{"valid https hosted", "https://app.example.com", ""},
		{"valid with path prefix", "https://host/proxy-prefix", ""},
		{"valid with %2F path", "https://host/a%2Fb", ""},
		{"valid with trailing slash", "https://example.com/", ""},
		{"valid with whitespace padding", "  https://example.com  ", ""},

		{"empty", "", "must not be empty"},
		{"whitespace only", "   ", "must not be empty"},
		{"missing scheme", "localhost:8080", "scheme"},
		{"wrong scheme", "ftp://example.com", "http or https"},
		{"no host", "https://", "host"},
		{"query string", "https://example.com/?x=1", "query"},
		{"query on prefix", "https://example.com/team/app?x=1", "query"},
		{"fragment", "https://example.com/#frag", "fragment"},
		{"trailing-? ForceQuery", "https://example.com/?", "query"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateBaseURL(c.in)
			if c.want == "" {
				if err != nil {
					t.Errorf("expected nil error for %q, got: %v", c.in, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error for %q", c.in)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q should mention %q", err.Error(), c.want)
			}
		})
	}
}

// TestNormalizeBaseURL pins the structural slash placement —
// crucially, %2F preservation. A naive string-append normalize
// would land the slash on a query/fragment for inputs validate
// rejects, OR decode %2F into a literal `/`. Both are wrong.
func TestNormalizeBaseURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://example.com", "https://example.com/"},
		{"https://example.com/", "https://example.com/"},
		{"https://example.com/team/app", "https://example.com/team/app/"},
		{"https://example.com/team/app/", "https://example.com/team/app/"},
		{"http://localhost:8080", "http://localhost:8080/"},
		// %2F preservation: one segment containing a literal slash,
		// NOT two segments. RFC 3986 distinguishes these.
		{"https://example.com/a%2Fb", "https://example.com/a%2Fb/"},
		// Whitespace tolerated (copy-paste artifact).
		{"  https://example.com  ", "https://example.com/"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := normalizeBaseURL(c.in)
			if got != c.want {
				t.Errorf("normalizeBaseURL(%q) = %q; want %q", c.in, got, c.want)
			}
		})
	}
}

// TestValidateHeaderValue pins the byte-rejection set. Mirrors
// what net/http would reject deep inside its transport — catching
// at config-parse time gives a clear error rather than an opaque
// "invalid header field value" mid-call.
func TestValidateHeaderValue(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain ASCII", "tm_valid_key_123", ""},
		{"with HTAB", "tm_x\tafter", ""},
		{"with obs-text byte", "tm_x\xc3\xa9", ""}, // UTF-8 é

		{"carriage return", "tm_x\rmore", "carriage return"},
		{"newline", "tm_x\nInjected: yes", "newline"},
		{"NUL byte", "tm_x\x00", "NUL byte"},
		{"DEL byte", "tm_x\x7f", "DEL"},
		{"SOH (0x01)", "tm_x\x01", "0x01"},
		{"ESC (0x1B)", "tm_x\x1bmore", "0x1B"},
		{"CRLF injection", "tm_x\r\nX-Injected: yes", "carriage return"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateHeaderValue(c.in)
			if c.want == "" {
				if err != nil {
					t.Errorf("expected nil error for %q, got: %v", c.in, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error for %q", c.in)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q should mention %q", err.Error(), c.want)
			}
		})
	}
}
