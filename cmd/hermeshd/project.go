package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	"github.com/hermeshai/hermesh/internal/machine"
)

var unsafeID = regexp.MustCompile(`[^a-z0-9._-]+`)

// projectName makes a project id of a folder name, as the apps do.
func projectName(folder string) string {
	s := strings.Trim(unsafeID.ReplaceAllString(strings.ToLower(folder), "-"), "-._")
	if s == "" {
		return "project"
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// projectCommand changes this machine's projects from its own shell
// (docs/onboarding.md §6.7): through the running service's control socket,
// or directly when it isn't running.
func projectCommand(dataDir *string, openMachine func() (*machine.Machine, error)) *cobra.Command {
	machineDir := func() string { return filepath.Join(*dataDir, "machine") }
	apply := func(ctx context.Context, cmd *identityv1.AdminCommand) (string, error) {
		body, _ := protojson.Marshal(cmd)
		if out, ok, err := machine.ControlRequest(ctx, machineDir(), "POST", "/settings", body); ok {
			return string(out), err
		}
		n, err := openMachine()
		if err != nil {
			return "", err
		}
		defer n.Close()
		if !n.Paired() {
			return "", fmt.Errorf("this machine is not paired yet; run `hermeshd pair`")
		}
		return n.ApplyLocal(ctx, cmd)
	}
	project := &cobra.Command{Use: "project", Short: "This machine's projects: the folders agents may work in"}

	var name, base, agents string
	var inPlace bool
	add := &cobra.Command{
		Use:   "add <folder>",
		Short: "Add a folder as a project (a separate worktree per session, or --in-place)",
		Example: `  hermeshd project add ~/code/app
  hermeshd project add ~/notes --in-place --agents claude`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := args[0]
			if strings.HasPrefix(dir, "~/") {
				home, _ := os.UserHomeDir()
				dir = filepath.Join(home, dir[2:])
			}
			dir, err := filepath.Abs(dir)
			if err != nil {
				return err
			}
			if st, err := os.Stat(dir); err != nil || !st.IsDir() {
				return fmt.Errorf("%s is not a folder", args[0])
			}
			if name == "" {
				name = projectName(filepath.Base(dir))
			}
			p := &identityv1.AddProject{ProjectId: name, Path: dir, DefaultBaseRef: base, Workspace: identityv1.Workspace_WORKSPACE_WORKTREE}
			if inPlace {
				p.Workspace, p.DefaultBaseRef = identityv1.Workspace_WORKSPACE_IN_PLACE, ""
			}
			for _, a := range strings.Split(agents, ",") {
				if a = strings.TrimSpace(a); a != "" {
					p.Agents = append(p.Agents, a)
				}
			}
			if _, err := apply(cmd.Context(), &identityv1.AdminCommand{Command: &identityv1.AdminCommand_AddProject{AddProject: p}}); err != nil {
				return err
			}
			how := "separate worktrees"
			if inPlace {
				how = "in place"
			}
			if base != "" {
				how += ", base " + base
			}
			who := "every coding agent"
			if len(p.Agents) > 0 {
				who = strings.Join(p.Agents, ", ")
			}
			fmt.Printf("✓ Added project “%s” (%s) for %s\n", name, how, who)
			return nil
		},
	}
	add.Flags().StringVar(&name, "name", "", "the project's name (default: from the folder)")
	add.Flags().BoolVar(&inPlace, "in-place", false, "agents change this folder directly, with no worktree")
	add.Flags().StringVar(&base, "base", "", "the branch new sessions start from (default: the current one)")
	add.Flags().StringVar(&agents, "agents", "", "the coding agents that may use it, comma-separated (default: all)")

	remove := &cobra.Command{
		Use:   "remove <name>",
		Short: "Stop using a project (the folder stays)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := apply(cmd.Context(), &identityv1.AdminCommand{Command: &identityv1.AdminCommand_RemoveProject{RemoveProject: &identityv1.RemoveProject{ProjectId: args[0]}}}); err != nil {
				return err
			}
			fmt.Printf("✓ Removed project “%s”; its folder stays\n", args[0])
			return nil
		},
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List this machine's projects",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var projects []*machinev1.Project
			if out, ok, err := machine.ControlRequest(cmd.Context(), machineDir(), "GET", "/projects", nil); ok {
				if err != nil {
					return err
				}
				res := &machinev1.ListProjectsResponse{}
				if err := protojson.Unmarshal(out, res); err != nil {
					return err
				}
				projects = res.Projects
			} else {
				n, err := openMachine()
				if err != nil {
					return err
				}
				ps, err := n.Projects()
				n.Close()
				if err != nil {
					return err
				}
				for _, p := range ps {
					projects = append(projects, &machinev1.Project{ProjectId: p.ID, Path: p.Path})
				}
			}
			if len(projects) == 0 {
				fmt.Println("No projects yet: hermeshd project add <folder>")
			}
			for _, p := range projects {
				fmt.Printf("%-20s %s\n", p.ProjectId, p.Path)
			}
			return nil
		},
	}
	project.AddCommand(add, remove, list)
	return project
}
