package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// CLIVersion is the published version string. Set via -ldflags at
// build time (see Makefile's `build` target); falls back to "dev"
// during local development.
var CLIVersion = "dev"

// SpecVersion records which OpenAPI v1 snapshot the binary was
// built against. Surfaced in `tm version` so a CI operator can
// confirm the binary matches the server's API surface.
//
// Bumped manually when the snapshot is regenerated via
// `make regen-spec` — kept in sync with the `version` field in the
// committed openapi/v1.json.
const SpecVersion = "v1"

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version and which OpenAPI spec snapshot it was built against",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "tm %s (spec %s)\n", CLIVersion, SpecVersion)
			return nil
		},
	}
}
