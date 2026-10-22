package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	"github.com/hermeshai/hermesh/internal/peer"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// place is a project on a machine: one entry of the list an agent picks
// from, addressed as machine/project.
type place struct {
	Project     string `json:"project"`
	Machine     string `json:"machine"`
	MachineID   string `json:"machine_id"`
	ProjectID   string `json:"project_id"`
	Description string `json:"description"`
	Path        string `json:"path"`
	// worktree (a worktree per session, from base) or in_place.
	Workspace string `json:"workspace"`
	Base      string `json:"base"`
	// Sessions with a turn in progress in this project.
	ActiveSessions uint32       `json:"active_sessions"`
	Platform       string       `json:"platform"`
	Agents         []placeAgent `json:"agents"`
	// ready, paused, full, no_agent or unknown.
	State string `json:"state"`

	running, max uint32 // the machine's sessions
}

// placeAgent is a coding agent this agent may run there.
type placeAgent struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// offlineMachine is a machine this client cannot reach now: offline (not
// connected to the relay), or online but its connection failed (Error).
type offlineMachine struct {
	Machine   string  `json:"machine"`
	MachineID string  `json:"machine_id"`
	LastSeen  *string `json:"last_seen"`
	Error     string  `json:"error"`
}

func (a *app) projectCmd() *cobra.Command {
	cmd := group(&cobra.Command{Use: "project", Short: "The projects this client can work in, addressed as machine/project"})
	cmd.AddCommand(a.projectListCmd())
	return cmd
}

func (a *app) projectListCmd() *cobra.Command {
	var machineFlag string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the projects this client can work in, as machine/project, with the coding agents it may run there",
		Long: `List the projects this client can work in. Each is addressed as
machine/project (e.g. vps-1/demo); pass that to session start --project.
A project name that only one machine has can be given alone.

Each project shows what it is (the owner's description), how sessions work
there (a worktree per session from a base branch, or in place in the
directory), the sessions with a turn in progress there, the machine's
platform, the coding agents this agent may run, and whether a session can
start now: ready, paused (by the owner), full (the machine runs as many
sessions as it takes), or no_agent.

A machine that is offline is listed after the projects as machine/* with
when it was last seen: its projects cannot be listed until it is back.
Being offline is not an error: the exit code is 0 even when every machine
is offline.`,
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
			places, errs := a.listPlaces(ctx, c, machines)
			offline := offlineOf(machines, errs)
			w := cmd.OutOrStdout()
			if a.output == "json" {
				if places == nil {
					places = []place{}
				}
				return a.report(w, nil, field{"projects", places}, field{"offline", offline})
			}
			if len(places) == 0 && len(offline) == 0 {
				fmt.Fprintln(w, "no projects this client can work in: ask the owner to add one, or to grant this client one, in Hermesh.app")
				return nil
			}
			tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
			fmt.Fprintln(tw, "PROJECT\tWORKSPACE\tOS\tAGENTS\tSTATE\tDESCRIPTION")
			for _, p := range places {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", p.Project, workspaceText(p), osOf(p.Platform), agentsText(p.Agents), stateText(p), p.Description)
			}
			for _, m := range offline {
				fmt.Fprintf(tw, "%s/*\t-\t-\t-\toffline\t%s\n", m.Machine, offlineText(m))
			}
			tw.Flush()
			return nil
		},
	}
	cmd.Flags().StringVar(&machineFlag, "machine", "", "only this machine (name or id)")
	return cmd
}

// listPlaces lists the projects on the online machines, with what this
// agent may run there; errs holds the machines that could not be asked.
func (a *app) listPlaces(ctx context.Context, c *peer.Client, machines []machineRef) ([]place, map[string]error) {
	res := map[string][]*machinev1.Project{}
	names := map[string]string{}
	status := map[string]*machinev1.MachineStatus{}
	var mu sync.Mutex
	errs := c.FanOut(ctx, ids(machines), func(ctx context.Context, n *peer.Machine) error {
		// Both at once: one round trip, not two.
		var info *machinev1.GetInfoResponse
		var infoErr error
		got := make(chan struct{})
		go func() {
			info, infoErr = n.Info.GetInfo(ctx, &machinev1.GetInfoRequest{})
			close(got)
		}()
		r, err := n.Info.ListProjects(ctx, &machinev1.ListProjectsRequest{})
		<-got
		if err != nil {
			return err
		}
		if infoErr != nil {
			return infoErr
		}
		mu.Lock()
		res[n.MachineID], names[n.MachineID], status[n.MachineID] = r.Projects, n.Name, info.Status
		mu.Unlock()
		return nil
	})
	var out []place
	for _, m := range machines {
		if name := names[m.ID]; name != "" {
			m.Name = name // as the machine itself says, not the relay
		}
		st := status[m.ID]
		for _, p := range res[m.ID] {
			agents := a.usableAgents(st, p)
			pl := place{Project: m.Name + "/" + p.ProjectId, Machine: m.Name, MachineID: m.ID, ProjectID: p.ProjectId,
				Description: p.Description, Path: p.Path, Workspace: workspaceName(p.Workspace), Base: p.Base,
				ActiveSessions: p.ActiveSessions, Platform: st.GetPlatform(), Agents: agents,
				running: st.GetRunningSessions(), max: st.GetMaxSessions()}
			switch {
			case st == nil:
				pl.State = "unknown"
			case st.Paused:
				pl.State = "paused"
			case st.MaxSessions > 0 && st.RunningSessions >= st.MaxSessions:
				pl.State = "full"
			case len(agents) == 0:
				pl.State = "no_agent"
			default:
				pl.State = "ready"
			}
			out = append(out, pl)
		}
	}
	return out, errs
}

// usableAgents are the coding agents this agent may run in a project:
// installed and logged in on its machine, in the grant, and set up for the
// project (a project with no agents is for all).
func (a *app) usableAgents(st *machinev1.MachineStatus, p *machinev1.Project) []placeAgent {
	out := []placeAgent{}
	for _, ag := range st.GetAgents() {
		if ag.Available && ag.LoggedIn && identity.Allows(a.g.Agents, ag.Name) && (len(p.Agents) == 0 || slices.Contains(p.Agents, ag.Name)) {
			out = append(out, placeAgent{Name: ag.Name, Version: ag.Version})
		}
	}
	return out
}

// warmProject warms the machine an address names, or every machine when
// resolveProject has to ask them which has the project.
func warmProject(address string) func([]machineRef) []machineRef {
	return func(seen []machineRef) []machineRef {
		if name, _, ok := strings.Cut(address, "/"); ok {
			m, _ := findMachine(seen, name)
			return []machineRef{m}
		}
		return seen
	}
}

// resolveProject finds the machine and project an address names:
// machine/project, or a project name that only one machine has.
func (a *app) resolveProject(ctx context.Context, c *peer.Client, machines []machineRef, address string) (machineRef, string, error) {
	if name, project, ok := strings.Cut(address, "/"); ok {
		m, found := findMachine(machines, name)
		if !found {
			return machineRef{}, "", &exitError{code: exitUsage, kind: "not_found", err: fmt.Errorf("no machine %q", name), hint: "see `hermesh project list`"}
		}
		return m, project, nil
	}
	places, errs := a.listPlaces(ctx, c, machines)
	var matches []place
	for _, p := range places {
		if p.ProjectID == address {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 1:
		m, _ := findMachine(machines, matches[0].MachineID)
		return m, address, nil
	case 0:
		if offline := offlineOf(machines, errs); len(offline) > 0 {
			// It may be on a machine that is offline: no answer yet.
			var names []string
			for _, m := range offline {
				names = append(names, m.Machine)
			}
			return machineRef{}, "", &exitError{code: exitUnavailable, kind: "unreachable", retryable: true,
				err:  fmt.Errorf("no project %q on the machines online now; offline: %s", address, strings.Join(names, ", ")),
				hint: "it may be on a machine that is offline: try again once it is back, or ask the owner to check it"}
		}
		return machineRef{}, "", &exitError{code: exitUsage, kind: "not_found", err: fmt.Errorf("no project %q", address), hint: "see `hermesh project list`"}
	}
	var addrs []string
	for _, p := range matches {
		addrs = append(addrs, p.Project)
	}
	return machineRef{}, "", fail(exitUsage, "several machines have a project %q: pass one of %s as --project", address, strings.Join(addrs, ", "))
}

func workspaceText(p place) string {
	s := "worktree from " + p.Base
	if p.Workspace == "in_place" {
		s = "in place"
	}
	if p.ActiveSessions > 0 {
		s += fmt.Sprintf(" · %d active", p.ActiveSessions)
	}
	return s
}

// osOf is the OS part of a platform ("linux/amd64" → "linux").
func osOf(platform string) string {
	os, _, _ := strings.Cut(platform, "/")
	return os
}

func agentsText(agents []placeAgent) string {
	if len(agents) == 0 {
		return "-"
	}
	var out []string
	for _, ag := range agents {
		out = append(out, ag.Name)
	}
	return strings.Join(out, ", ")
}

func stateText(p place) string {
	switch p.State {
	case "paused":
		return "paused by the owner"
	case "full":
		return fmt.Sprintf("full (%d/%d sessions)", p.running, p.max)
	case "no_agent":
		return "no coding agent this client may run"
	}
	return p.State
}

// offlineOf lists the machines that could not be asked: offline, or
// online with errs naming why the connection failed.
func offlineOf(machines []machineRef, errs map[string]error) []offlineMachine {
	out := []offlineMachine{}
	for _, m := range machines {
		o := offlineMachine{Machine: m.Name, MachineID: m.ID, LastSeen: tsJSON(m.Entry.GetLastSeen())}
		switch {
		case !m.Online:
		case errs[m.ID] != nil:
			o.Error = errs[m.ID].Error()
		default:
			continue
		}
		out = append(out, o)
	}
	return out
}

// offlineText says since when m is offline, or why it cannot be reached.
func offlineText(m offlineMachine) string {
	if m.Error != "" {
		return "cannot be reached: " + m.Error
	}
	if m.LastSeen != nil {
		if t, err := time.Parse(time.RFC3339, *m.LastSeen); err == nil {
			return "last seen " + approx(time.Since(t)) + " ago"
		}
	}
	return "never seen"
}
