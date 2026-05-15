// Command tm is the TrafficMorph CLI: drive runs, fetch history,
// import captures, and gate CI builds on regression verdicts from
// a single static binary.
//
// Quickstart:
//
//	export TM_API_KEY=tm_…
//	tm profiles list
//	tm runs start 42 --wait --fail-on-verdict FAIL,WARN
//
// See cli/README.md for full usage.
package main

import (
	"fmt"
	"os"

	"github.com/trafficmorph/tm-cli/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		// Subcommands that need a verdict-specific exit code wrap
		// it in cli.ExitCodeError; everything else exits 1.
		if ec, ok := err.(interface{ ExitCode() int }); ok {
			os.Exit(ec.ExitCode())
		}
		os.Exit(1)
	}
}
