package cli

import (
	"strconv"
	"time"
)

// parseInt64 wraps strconv with a tighter error type used by the
// cobra args validators in each subcommand.
func parseInt64(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

// formatInt64Ptr renders a nullable int64 from the generated client
// for human-readable table output. Empty string for null so the
// column doesn't read "<nil>" which is jarring in CI logs.
func formatInt64Ptr(p *int64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatInt(*p, 10)
}

// formatStrPtr renders a nullable string in table output.
func formatStrPtr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// formatTimePtr renders a nullable RFC3339 timestamp from the
// generated client. Returns an empty string for null so columns
// stay aligned.
func formatTimePtr(p *time.Time) string {
	if p == nil {
		return ""
	}
	return p.UTC().Format(time.RFC3339)
}
