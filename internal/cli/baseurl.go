package cli

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// validateBaseURL returns nil if raw parses as an absolute http/https
// URL with a non-empty host AND no query string or fragment, or a
// typed error otherwise. Catches the common malformed-input cases
// that would otherwise fail late on the first API call (or worse —
// silently misroute):
//
//   - `""` or whitespace-only        → "must not be empty"
//   - `"localhost:8092"` (no scheme) → "must include http:// or https:// scheme"
//   - `"ftp://x"` (wrong scheme)     → "scheme must be http or https"
//   - `"https://"` (no host)         → "must include a host"
//   - `"https://x/?q=1"`             → "must not contain a query string"
//   - `"https://x/#frag"`            → "must not contain a fragment"
//
// Query strings and fragments are rejected because they corrupt
// trailing-slash normalization and silently mangle generated
// request URLs. Per-request params belong at the endpoint call
// site, not the base URL.
//
// The string is trimmed before parsing; callers can pass values
// with surrounding whitespace (a common copy-paste artifact) and
// have them normalize cleanly.
func validateBaseURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("base URL must not be empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("base URL %q is not a valid URL: %w", raw, err)
	}
	if u.Scheme == "" {
		return fmt.Errorf("base URL %q must include http:// or https:// scheme (got no scheme — did you mean http://%s?)", raw, raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("base URL %q has scheme %q; must be http or https", raw, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("base URL %q must include a host", raw)
	}
	if u.RawQuery != "" || u.ForceQuery {
		have := u.RawQuery
		if have == "" {
			have = "(empty, but trailing `?` is present)"
		}
		return fmt.Errorf("base URL %q must not contain a query string (have %q); attach per-request params at the endpoint call site instead", raw, have)
	}
	if u.Fragment != "" {
		return fmt.Errorf("base URL %q must not contain a fragment (have %q); fragments are client-side only and have no meaning to the server", raw, u.Fragment)
	}
	return nil
}

// normalizeBaseURL parses raw and returns it with a trailing slash
// on the path component (NOT a string-level append). Call AFTER
// validateBaseURL so the URL is known well-formed; be defensive
// if parse fails anyway and fall back to the string append.
//
// Why structural: a naive string append lands the slash on a
// query or fragment for inputs like `https://x?q=1`, silently
// producing `https://x?q=1/`. Validation rejects those upstream,
// but doing the slash on the path component keeps the function
// correct even if a future code path bypasses validate.
//
// RawPath preservation: percent-encoded path segments (e.g.
// `%2F`) survive round-trip. Per RFC 3986, `/a%2Fb` (one segment)
// and `/a/b` (two segments) are semantically different paths —
// never collapse one into the other.
func normalizeBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return ensureTrailingSlash(raw)
	}

	// Inspect RawPath's suffix when it's set (the emitted form
	// uses RawPath, so its suffix is what matters); fall back to
	// Path otherwise.
	var hasTrailingSlash bool
	if u.RawPath != "" {
		hasTrailingSlash = strings.HasSuffix(u.RawPath, "/")
	} else {
		hasTrailingSlash = strings.HasSuffix(u.Path, "/")
	}

	if !hasTrailingSlash {
		u.Path += "/"
		if u.RawPath != "" {
			u.RawPath += "/"
		}
	}
	return u.String()
}

// ensureTrailingSlash is the string-only trailing-slash helper.
// Used as the defensive fallback inside normalizeBaseURL when
// url.Parse somehow rejects an input validate already accepted.
func ensureTrailingSlash(s string) string {
	if strings.HasSuffix(s, "/") {
		return s
	}
	return s + "/"
}

// validateHeaderValue rejects strings that would cause http.Request
// to fail at execution time with "invalid header field value".
// Mirrors the stdlib's rule (golang.org/x/net/http/httpguts):
// reject ASCII controls 0x00–0x1F (except HTAB) and DEL (0x7F).
// Per RFC 7230, header field-value is built from VCHAR (0x21–0x7E),
// SP (0x20), HTAB (0x09), and obs-text (0x80–0xFF).
//
// CR/LF specifically would enable header-injection (a key with
// embedded `\r\n` could splice in arbitrary extra headers); NUL
// would corrupt the wire encoding; DEL is rejected by the stdlib.
// Catching at config-parse time gives a clear error message the
// caller can act on, rather than an opaque transport-layer
// failure on the first API call.
func validateHeaderValue(v string) error {
	for i := 0; i < len(v); i++ {
		b := v[i]
		if b == '\t' || (b >= 0x20 && b != 0x7f) {
			continue
		}
		return fmt.Errorf("value contains %s at position %d; HTTP header values cannot contain ASCII control bytes", describeControlByte(b), i)
	}
	return nil
}

// describeControlByte names a rejected byte for inclusion in the
// validation error. Named cases for CR/LF/NUL/DEL (the bytes a
// caller is most likely to have stumbled into); hex fallback for
// everything else.
func describeControlByte(b byte) string {
	switch b {
	case '\r':
		return "a carriage return (CR, 0x0D)"
	case '\n':
		return "a newline (LF, 0x0A)"
	case '\x00':
		return "a NUL byte (0x00)"
	case 0x7f:
		return "a DEL byte (0x7F)"
	default:
		return fmt.Sprintf("a control byte (0x%02X)", b)
	}
}
