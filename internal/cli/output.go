package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
)

// writeJSON pretty-prints `v` as 2-space-indented JSON. Used by all
// subcommands when --json is set, and as the underlying serializer
// for the `tm history get` command (whose default IS JSON because
// the response is large and structured).
func writeJSON(out io.Writer, v interface{}) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// newTabWriter returns a tabwriter with the same column settings
// every "human" table in the CLI uses. Centralizing the parameters
// means rolling out a table-style change (e.g. wider padding) is
// a one-place edit.
func newTabWriter(out io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
}

// errorFromResponse builds a human-readable error from a non-2xx
// HTTP response. The server's GlobalExceptionHandler emits
// {"error":"..."} on every 4xx path so we surface that message
// directly when present; falls back to the status line for
// non-JSON or unexpected bodies (e.g. 401 from the
// AuthenticationEntryPoint, which DOES emit JSON but the field
// names differ — we sniff for either).
//
// Takes a plain status code rather than a *http.Response so the
// caller doesn't have to construct one for the typed-response
// path, which only exposes the integer StatusCode().
func errorFromResponse(statusCode int, body []byte) error {
	var parsed struct {
		Error   string `json:"error"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		switch {
		case parsed.Error != "" && parsed.Code != "":
			return fmt.Errorf("%s (HTTP %d, code=%s)", parsed.Error, statusCode, parsed.Code)
		case parsed.Error != "":
			return fmt.Errorf("%s (HTTP %d)", parsed.Error, statusCode)
		case parsed.Message != "":
			return fmt.Errorf("%s (HTTP %d)", parsed.Message, statusCode)
		}
	}
	return fmt.Errorf("server returned HTTP %d", statusCode)
}
