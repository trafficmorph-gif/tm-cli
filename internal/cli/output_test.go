package cli

import (
	"strings"
	"testing"
)

func TestErrorFromResponse(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		body        string
		wantContain []string
	}{
		{
			name:        "standard error body",
			status:      400,
			body:        `{"error":"Profile not found"}`,
			wantContain: []string{"Profile not found", "400"},
		},
		{
			name:        "error with code",
			status:      409,
			body:        `{"error":"Monthly quota","code":"USAGE_LIMIT_REACHED"}`,
			wantContain: []string{"Monthly quota", "409", "USAGE_LIMIT_REACHED"},
		},
		{
			name:        "auth-entry-point shape",
			status:      401,
			body:        `{"error":"Authentication required","code":"AUTH_REQUIRED"}`,
			wantContain: []string{"Authentication required", "401", "AUTH_REQUIRED"},
		},
		{
			name:        "non-JSON body",
			status:      500,
			body:        `<html>oops</html>`,
			wantContain: []string{"500"},
		},
		{
			name:        "empty body",
			status:      503,
			body:        ``,
			wantContain: []string{"503"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := errorFromResponse(c.status, []byte(c.body)).Error()
			for _, want := range c.wantContain {
				if !strings.Contains(got, want) {
					t.Errorf("errorFromResponse(%d, %q) = %q; missing %q",
						c.status, c.body, got, want)
				}
			}
		})
	}
}
