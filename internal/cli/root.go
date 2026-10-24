// Package cli wires the gaia command lines. Commands stay thin: they parse
// flags, call internal packages and print results.
package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// NewRootCommand builds the gaia command tree.
func NewRootCommand(version string) *cobra.Command {
	root := &cobra.Command{
		Use:           "gaia",
		Short:         "Gaia: infrastructure from a single YAML contract",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newVersionCommand("gaia", version), newValidateCommand(), newPlanCommand(), newApplyCommand(), newRecordCommand(), newRunCommand(), newServerCommand(), newUserCommand(), newMigrateCommand())
	return root
}

// NewIPAMCommand builds the gaia-ipam command tree. IPAM is a separate
// service that Gaia reaches through a provider, like any other system.
func NewIPAMCommand(version string) *cobra.Command {
	root := &cobra.Command{
		Use:           "gaia-ipam",
		Short:         "Gaia IPAM: IP pools and allocations",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newVersionCommand("gaia-ipam", version), newIPAMServeCommand(), newIPAMMigrateCommand(), newIPAMPoolCommand())
	return root
}

func newVersionCommand(binary, version string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the " + binary + " version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), version)
			return err
		},
	}
}
