package cli

import (
	"context"
	"net/http"

	"github.com/trafficmorph-gif/tm-cli/internal/api"
)

// apiKeyRequestEditor returns a RequestEditor that injects the
// X-Api-Key header on every outbound request.
//
// We use the API-key header (over `Authorization: Bearer`) because
// the header name is explicit in logs and trace tooling — easier to
// debug "why did CI fail" tickets when the auth surface shows up
// distinctly from JWTs / OAuth bearer tokens in load-balancer logs.
// Both schemes accept the same key value per the server-side
// OpenAPI configuration.
func apiKeyRequestEditor(apiKey string) api.RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		req.Header.Set("X-Api-Key", apiKey)
		// Sane User-Agent so requests are distinguishable from
		// raw curl probes in server access logs.
		req.Header.Set("User-Agent", "tm-cli/"+CLIVersion)
		return nil
	}
}
