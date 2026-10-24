package cli

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Warewave-Technology/gaia/internal/client"
)

// apiClient is a client for addr, signed in with the saved session when
// there is one.
func apiClient(addr string) *client.Client {
	c := client.New(addr)
	if cr, err := client.Load(c.Base); err == nil {
		c.WithCredentials(cr, client.Save)
	}
	return c
}

// hint adds what to do to an error that means "not signed in".
func hint(err error) error {
	var e *client.Error
	if errors.Is(err, client.ErrNoCredentials) || errors.As(err, &e) && e.Status == 401 {
		return fmt.Errorf("%w (run gaia login)", err)
	}
	return err
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start() //nolint:gosec // the server's own sign-in page
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start() //nolint:gosec // same
	default:
		return exec.Command("xdg-open", url).Start() //nolint:gosec // same
	}
}

func newLoginCommand() *cobra.Command {
	var addr string
	var device bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign the CLI in to gaia server",
		Long: "login opens the browser, where you approve the sign-in, and keeps Gaia's tokens\n" +
			"in the OS keychain. --device prints a code to approve from another device, for\n" +
			"machines without a browser.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := client.New(addr)
			out := cmd.OutOrStdout()
			var cr *client.Credentials
			var err error
			if device {
				cr, err = c.DeviceLogin(cmd.Context(), out)
			} else {
				cr, err = c.Login(cmd.Context(), openBrowser, out)
			}
			if err != nil {
				return err
			}
			me, err := client.New(addr).WithCredentials(*cr, nil).Me(cmd.Context())
			if err != nil {
				return err
			}
			cr.Username, _ = me["username"].(string)
			if err := client.Save(*cr); err != nil {
				return err
			}
			_, err = fmt.Fprintf(out, "signed in to %s as %s until %s\n", c.Base, cr.Username, cr.RefreshExpiresAt.Local().Format("2006-01-02 15:04"))
			return err
		},
	}
	serverFlag(cmd, &addr)
	cmd.Flags().BoolVar(&device, "device", false, "sign in with a code approved on another device")
	return cmd
}

func newLogoutCommand() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use: "logout", Short: "End the CLI's session and forget its tokens", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := apiClient(addr)
			err := c.Logout(cmd.Context())
			client.Forget(c.Base)
			if err != nil {
				return fmt.Errorf("the local tokens are gone, but the server did not confirm: %w", err)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "signed out")
			return err
		},
	}
	serverFlag(cmd, &addr)
	return cmd
}

func newWhoamiCommand() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use: "whoami", Short: "Show who the CLI is signed in as", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			me, err := apiClient(addr).Me(cmd.Context())
			if err != nil {
				return hint(err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s (%s), teams %s\n", me["username"], joinAny(me["roles"]), joinAny(me["teams"]))
			return err
		},
	}
	serverFlag(cmd, &addr)
	return cmd
}

func joinAny(v any) string {
	var out []string
	if xs, ok := v.([]any); ok {
		for _, x := range xs {
			out = append(out, fmt.Sprint(x))
		}
	}
	if len(out) == 0 {
		return "-"
	}
	return strings.Join(out, ",")
}
