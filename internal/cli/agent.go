package cli

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"sync"
	"text/tabwriter"

	"github.com/spf13/cobra"

	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	"github.com/hermeshai/hermesh/internal/peer"
	"github.com/hermeshai/hermesh/internal/provider/acp"
)

// machineAgent is a coding agent this client may run on a machine, with
// the models it offers there.
type machineAgent struct {
	Agent     string       `json:"agent"`
	Machine   string       `json:"machine"`
	MachineID string       `json:"machine_id"`
	Version   string       `json:"version"`
	Models    []agentModel `json:"models"`
	// What a session runs with when the client names none.
	DefaultModel  string `json:"default_model"`
	DefaultEffort string `json:"default_effort"`
}

type agentModel struct {
	ID      string   `json:"id"`
	Efforts []string `json:"efforts"`
}

func (a *app) agentCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "agent", Short: "The coding agents this client may run, and setting one up on this machine"}
	cmd.AddCommand(a.agentListCmd())
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

func (a *app) agentListCmd() *cobra.Command {
	var machineFlag, agentFlag string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the coding agents this client may run on each machine, with the models and reasoning efforts they offer",
		Long: `List the coding agents this client may run: installed and logged in on
the machine, and in the grant. Each shows the models it offers there, with
the reasoning efforts each model takes, and the model and effort a session
runs with when session start names none (the owner's default for the agent
on that machine, else the agent's own; empty when Hermesh can't tell).
Pass a model's id to session start --model and one of its efforts to
--effort.

A machine that is offline is listed after the agents as machine/*: its
agents cannot be listed until it is back. Being offline is not an error.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, machines, err := a.connect(ctx, warmTargets(machineFlag))
			if err != nil {
				return err
			}
			defer c.Close()
			machines, err = targetMachines(machines, machineFlag)
			if err != nil {
				return err
			}
			agents, errs := a.listAgents(ctx, c, machines, agentFlag)
			offline := offlineOf(machines, errs)
			w := cmd.OutOrStdout()
			if a.output == "json" {
				return a.report(w, nil, field{"agents", agents}, field{"offline", offline})
			}
			if len(agents) == 0 && len(offline) == 0 {
				fmt.Fprintln(w, "no coding agent this client may run")
				return nil
			}
			tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
			fmt.Fprintln(tw, "AGENT\tMACHINE\tDEFAULT\tMODELS")
			for _, ag := range agents {
				var ids []string
				for _, m := range ag.Models {
					ids = append(ids, m.ID)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", ag.Agent, ag.Machine, cmp.Or(strings.TrimSpace(ag.DefaultModel+" "+ag.DefaultEffort), "-"), cmp.Or(strings.Join(ids, ", "), "-"))
			}
			for _, m := range offline {
				fmt.Fprintf(tw, "%s/*\t-\t-\toffline: %s\n", m.Machine, offlineText(m))
			}
			tw.Flush()
			return nil
		},
	}
	cmd.Flags().StringVar(&machineFlag, "machine", "", "only this machine (name or id)")
	cmd.Flags().StringVar(&agentFlag, "agent", "", "only this agent (e.g. codex)")
	return cmd
}

// listAgents lists the coding agents this client may run on the online
// machines (only agent, when set); errs holds the machines that could not
// be asked.
func (a *app) listAgents(ctx context.Context, c *peer.Client, machines []machineRef, agent string) ([]machineAgent, map[string]error) {
	names := map[string]string{}
	status := map[string]*machinev1.MachineStatus{}
	var mu sync.Mutex
	errs := c.FanOut(ctx, ids(machines), func(ctx context.Context, n *peer.Machine) error {
		info, err := n.Info.GetInfo(ctx, &machinev1.GetInfoRequest{})
		if err != nil {
			return err
		}
		mu.Lock()
		names[n.MachineID], status[n.MachineID] = n.Name, info.Status
		mu.Unlock()
		return nil
	})
	out := []machineAgent{}
	for _, m := range machines {
		for _, ag := range status[m.ID].GetAgents() {
			if !a.mayRun(ag) || (agent != "" && ag.Name != agent) {
				continue
			}
			e := machineAgent{Agent: ag.Name, Machine: cmp.Or(names[m.ID], m.Name), MachineID: m.ID, Version: ag.Version, Models: []agentModel{},
				DefaultModel: cmp.Or(ag.Defaults.GetModel(), ag.Own.GetModel()), DefaultEffort: cmp.Or(ag.Defaults.GetEffort(), ag.Own.GetEffort())}
			for _, x := range ag.Models {
				e.Models = append(e.Models, agentModel{ID: x.Id, Efforts: append([]string{}, x.Efforts...)})
			}
			out = append(out, e)
		}
	}
	return out, errs
}
