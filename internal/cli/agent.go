package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hermeshai/hermesh/internal/provider/acp"
)

func (a *app) agentCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "agent", Short: "Set up a coding agent on this machine"}
	var method string
	login := &cobra.Command{Use: "login antigravity", Short: "Sign in to the official Antigravity ACP server on this machine", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if args[0] != "antigravity" {
				return fail(exitUsage, "agent login supports antigravity; use other agents' own login commands")
			}
			for _, p := range acp.Providers("") {
				if p.Name() != args[0] {
					continue
				}
				fmt.Fprintln(c.ErrOrStderr(), "Follow the Antigravity sign-in link below. Press Ctrl+C to cancel.")
				if err := p.Login(c.Context(), method, c.ErrOrStderr()); err != nil {
					return err
				}
				if a.output == "json" {
					return a.report(c.OutOrStdout(), nil, field{"agent", p.Name()}, field{"authenticated", true})
				}
				fmt.Fprintln(c.OutOrStdout(), "Antigravity ACP signed in. Hermesh can now start its sessions.")
				return nil
			}
			return fail(exitUsage, "unknown agent %q", args[0])
		}}
	login.Flags().StringVar(&method, "method", "oauth-personal", "ACP authentication method (oauth-personal, oauth-business, gemini-api-key, agent-platform)")
	cmd.AddCommand(login)
	return cmd
}
