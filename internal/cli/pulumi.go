package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Warewave-Technology/gaia/internal/engine/pulumi"
)

func newPulumiCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "pulumi", Short: "Gaia's Pulumi engine"}
	cmd.AddCommand(&cobra.Command{
		Use:   "prepare",
		Short: "Install the pinned Pulumi CLI and provider plugins, for servers that should not download them during runs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := pulumi.Prepare(cmd.Context()); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "pulumi CLI and plugins installed")
			return nil
		},
	})
	return cmd
}
