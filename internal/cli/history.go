package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/trafficmorph-gif/tm-cli/internal/api"
)

func newHistoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Fetch past runs and their auto-comparison verdicts",
	}
	cmd.AddCommand(newHistoryGetCmd())
	return cmd
}

func newHistoryGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "get <run-id>",
		Short:       "Fetch a single run's full detail (the CI post-run inspection endpoint)",
		Args:        cobra.ExactArgs(1),
		Annotations: authRequired(),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseInt64(args[0])
			if err != nil {
				return fmt.Errorf("invalid run id %q: %w", args[0], err)
			}

			c, err := api.NewClientWithResponses(config.BaseURL,
				api.WithRequestEditorFn(apiKeyRequestEditor(config.APIKey)))
			if err != nil {
				return err
			}
			ctx, cancel := shortCtx()
			defer cancel()

			resp, err := c.GetHistoryItemWithResponse(ctx, id)
			if err != nil {
				return err
			}
			if resp.StatusCode() >= 400 {
				return errorFromResponse(resp.StatusCode(), resp.Body)
			}

			// Run detail is structurally Map<String,Object> on the
			// server (the controller assembles a LinkedHashMap with
			// ~25 fields). Decoding into a Go map preserves field
			// order from the server while letting us pretty-print
			// without binding to a brittle struct.
			var detail map[string]interface{}
			if err := json.Unmarshal(resp.Body, &detail); err != nil {
				return fmt.Errorf("decode run detail: %w", err)
			}
			// Always emit JSON for history detail — the response is
			// ~20 fields including arrays / nested objects, no
			// reasonable table form.
			return writeJSON(cmd.OutOrStdout(), detail)
		},
	}
}
