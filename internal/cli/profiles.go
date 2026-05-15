package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/trafficmorph/tm-cli/internal/api"
)

func newProfilesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profiles",
		Short: "List and inspect traffic profiles",
	}
	cmd.AddCommand(newProfilesListCmd())
	cmd.AddCommand(newProfilesGetCmd())
	return cmd
}

func newProfilesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "list",
		Short:       "List all profiles owned by the authenticated user",
		Args:        cobra.NoArgs,
		Annotations: authRequired(),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := api.NewClientWithResponses(config.BaseURL,
				api.WithRequestEditorFn(apiKeyRequestEditor(config.APIKey)))
			if err != nil {
				return err
			}
			ctx, cancel := shortCtx()
			defer cancel()

			resp, err := c.ListProfilesWithResponse(ctx)
			if err != nil {
				return err
			}
			if resp.StatusCode() >= 400 {
				return errorFromResponse(resp.StatusCode(), resp.Body)
			}

			var summaries []api.TrafficProfileSummaryResponse
			if err := json.Unmarshal(resp.Body, &summaries); err != nil {
				return fmt.Errorf("decode profiles list: %w", err)
			}

			if config.JSON {
				return writeJSON(cmd.OutOrStdout(), summaries)
			}

			tw := newTabWriter(cmd.OutOrStdout())
			fmt.Fprintln(tw, "ID\tNAME\tCREATED")
			for _, p := range summaries {
				fmt.Fprintf(tw, "%s\t%s\t%s\n",
					formatInt64Ptr(p.Id),
					formatStrPtr(p.Name),
					formatTimePtr(p.CreatedAt))
			}
			return tw.Flush()
		},
	}
}

func newProfilesGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "get <profile-id>",
		Short:       "Show the full configuration + current run status of a profile",
		Args:        cobra.ExactArgs(1),
		Annotations: authRequired(),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseInt64(args[0])
			if err != nil {
				return fmt.Errorf("invalid profile id %q: %w", args[0], err)
			}

			c, err := api.NewClientWithResponses(config.BaseURL,
				api.WithRequestEditorFn(apiKeyRequestEditor(config.APIKey)))
			if err != nil {
				return err
			}
			ctx, cancel := shortCtx()
			defer cancel()

			resp, err := c.GetProfileWithResponse(ctx, id)
			if err != nil {
				return err
			}
			if resp.StatusCode() >= 400 {
				return errorFromResponse(resp.StatusCode(), resp.Body)
			}

			var profile api.ApiProfileResponse
			if err := json.Unmarshal(resp.Body, &profile); err != nil {
				return fmt.Errorf("decode profile: %w", err)
			}
			// Detail view always prints JSON — the profile has too
			// many fields for a useful table, and a CI step is the
			// most likely caller of `profiles get`.
			return writeJSON(cmd.OutOrStdout(), profile)
		},
	}
}
