package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Warewave-Technology/gaia/internal/auth"
)

func newNotificationsCommand() *cobra.Command {
	var addr, email string
	var triggers []string
	cmd := &cobra.Command{Use: "notifications", Short: "Your own email notifications (design 7.6)"}
	print := func(cmd *cobra.Command, n auth.Notifications) error {
		addr := n.Email
		if addr == "" {
			addr = "(none: nothing can be sent)"
		}
		if !n.EmailEditable {
			addr += "  (from your identity provider)"
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "email     %s\ntriggers  %s\navailable %s\n", addr, strings.Join(n.Triggers, ", "), strings.Join(n.Available, ", "))
		return err
	}
	show := &cobra.Command{
		Use: "show", Short: "Show your address and the triggers you get mail for", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var n auth.Notifications
			if err := apiClient(addr).Get(cmd.Context(), "/api/v1/me/notifications", &n); err != nil {
				return hint(err)
			}
			return print(cmd, n)
		},
	}
	set := &cobra.Command{
		Use:   "set",
		Short: "Choose the triggers you get mail for (and your address, for local accounts)",
		Long: "set replaces your subscriptions with --trigger (repeatable; none unsubscribes from\n" +
			"everything). You hear only of contracts you see in full: your teams', or all of\n" +
			"them as an operator or platform admin.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body := map[string]any{"triggers": triggers}
			if cmd.Flags().Changed("email") {
				body["email"] = email
			}
			if triggers == nil {
				body["triggers"] = []string{}
			}
			var n auth.Notifications
			if err := apiClient(addr).Put(cmd.Context(), "/api/v1/me/notifications", body, &n); err != nil {
				return hint(err)
			}
			return print(cmd, n)
		},
	}
	set.Flags().StringSliceVar(&triggers, "trigger", nil, "a trigger, such as change.pending; repeatable")
	set.Flags().StringVar(&email, "email", "", "your address (local accounts)")
	for _, c := range []*cobra.Command{show, set} {
		serverFlag(c, &addr)
		cmd.AddCommand(c)
	}
	return cmd
}
