package machine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/session/v1/sessionv1connect"
	"github.com/hermeshai/hermesh/internal/kernel"
	"github.com/hermeshai/hermesh/pkg/identity"
)

type machineSessionService struct {
	sessionv1connect.SessionServiceHandler
	n *Machine
}

func newMachineSessionService(n *Machine) sessionv1connect.SessionServiceHandler {
	return &machineSessionService{
		SessionServiceHandler: kernel.NewService(n.sessions, n.sessionPrincipal),
		n:                     n,
	}
}

func (s *machineSessionService) ListSessions(ctx context.Context, req *sessionv1.ListSessionsRequest) (*sessionv1.ListSessionsResponse, error) {
	list, next, err := s.n.ListSessionsPage(ctx, principalFrom(ctx), req)
	if err != nil {
		return nil, err
	}
	return &sessionv1.ListSessionsResponse{Sessions: list, Next: next}, nil
}

func (s *machineSessionService) WatchSessions(ctx context.Context, req *sessionv1.WatchSessionsRequest, stream *connect.ServerStream[sessionv1.WatchSessionsResponse]) error {
	send := stream.Send
	if req.View == sessionv1.SessionView_SESSION_VIEW_BASIC {
		send = func(m *sessionv1.WatchSessionsResponse) error {
			return stream.Send(&sessionv1.WatchSessionsResponse{
				Snapshot:          m.Snapshot,
				Sessions:          BasicSessions(m.Sessions),
				DeletedSessionIds: m.DeletedSessionIds,
			})
		}
	}
	return s.n.sessions.WatchSessions(ctx, s.n.sessionPrincipal(ctx), req.IncludeArchived, send)
}

type machineService struct{ n *Machine }

func (s *machineService) GetInfo(ctx context.Context, req *machinev1.GetInfoRequest) (*machinev1.GetInfoResponse, error) {
	// An agent about to pick where to work waits for a fresh check of the
	// coding agents; the owner's devices get what is known (and start the
	// first check), or wait for a new one when the person asked (Check
	// Again).
	switch {
	case req.Recheck && principalFrom(ctx).Kind == PrincipalOwner:
		select {
		case <-s.n.CheckAgents(true, 0):
		case <-ctx.Done():
		}
	case principalFrom(ctx).Kind == PrincipalClient:
		select {
		case <-s.n.CheckAgents(false, AgentsFresh):
		case <-ctx.Done():
		}
	default:
		s.n.CheckAgents(false, AgentsOnce)
	}
	return &machinev1.GetInfoResponse{MachineId: s.n.MachineID(), MachineName: s.n.Name(), Status: s.n.statusFor(principalFrom(ctx))}, nil
}

func (s *machineService) Watch(ctx context.Context, req *machinev1.WatchRequest, stream *connect.ServerStream[machinev1.WatchResponse]) error {
	return s.n.Watch(ctx, principalFrom(ctx), req, stream.Send)
}

func (s *machineService) PullWork(ctx context.Context, req *machinev1.PullWorkRequest) (*machinev1.PullWorkResponse, error) {
	return s.n.PullWork(ctx, principalFrom(ctx), req)
}

func (s *machineService) PunchDirect(ctx context.Context, req *machinev1.PunchDirectRequest) (*machinev1.PunchDirectResponse, error) {
	if principalFrom(ctx).Kind != PrincipalOwner {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("direct punch requires owner"))
	}
	addrs := s.n.punchDirect(req.Targets)
	return &machinev1.PunchDirectResponse{DirectQuicAddrs: addrs}, nil
}

func (s *machineService) ListProjects(ctx context.Context, _ *machinev1.ListProjectsRequest) (*machinev1.ListProjectsResponse, error) {
	return s.projects(ctx, principalFrom(ctx), true)
}

// projects lists the projects p may see; with base, each one's base branch
// (a git call per project).
func (s *machineService) projects(ctx context.Context, p *Principal, base bool) (*machinev1.ListProjectsResponse, error) {
	projects, err := s.n.st.Projects()
	if err != nil {
		return nil, err
	}
	active := s.n.ActiveByProject()
	out := &machinev1.ListProjectsResponse{}
	for _, pr := range projects {
		if p.Kind == PrincipalClient && !identity.Allows(p.Grant.ProjectIds, pr.ID) {
			continue
		}
		mp := &machinev1.Project{ProjectId: pr.ID, DefaultBaseRef: pr.DefaultBaseRef, Workspace: pr.Workspace,
			Path: pr.Path, Description: pr.Description, ActiveSessions: active[pr.ID], Repo: isRepoTop(pr.Path)}
		if base {
			mp.Base = projectBase(ctx, pr)
		}
		out.Projects = append(out.Projects, mp)
	}
	return out, nil
}

// projectWorkspace checks a project directory for a workspace mode; an
// unspecified mode is a worktree for a git repository root and in place
// for any other directory.
func projectWorkspace(ctx context.Context, mode identityv1.Workspace, path string) (identityv1.Workspace, string, error) {
	// "~/app", as a device types it or the repositories found are shown
	// (on Windows also "~\app").
	if home, err := os.UserHomeDir(); err == nil && (path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~"+string(filepath.Separator))) {
		path = filepath.Join(home, path[1:])
	}
	switch mode {
	case identityv1.Workspace_WORKSPACE_WORKTREE:
		p, err := ValidateRepo(ctx, path)
		if err != nil {
			err = fmt.Errorf("%w (a project without git works in place)", err)
		}
		return mode, p, err
	case identityv1.Workspace_WORKSPACE_IN_PLACE:
		p, err := ValidateDir(path)
		return mode, p, err
	}
	if p, err := ValidateRepo(ctx, path); err == nil {
		return identityv1.Workspace_WORKSPACE_WORKTREE, p, nil
	}
	p, err := ValidateDir(path)
	return identityv1.Workspace_WORKSPACE_IN_PLACE, p, err
}

// samePath: two project folders are the same folder (links resolved where
// they still exist).
func samePath(a, b string) bool {
	if ra, err := filepath.EvalSymlinks(a); err == nil {
		a = ra
	}
	if rb, err := filepath.EvalSymlinks(b); err == nil {
		b = rb
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

type adminService struct{ n *Machine }

var projectIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// maxDescription is the longest project description, in characters.
const maxDescription = 200

func (s *adminService) ApplyAdminCommand(ctx context.Context, req *machinev1.ApplyAdminCommandRequest) (*machinev1.ApplyAdminCommandResponse, error) {
	if principalFrom(ctx).Kind != PrincipalOwner {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("owner only"))
	}
	apply := s.n.ApplyAdminCommand
	if req.Settings != nil {
		apply = func(ctx context.Context, _ *identityv1.Signed) (string, error) {
			return s.n.ApplySettings(ctx, req.Settings)
		}
	}
	msg, err := apply(ctx, req.Command)
	if err != nil {
		return nil, err
	}
	return &machinev1.ApplyAdminCommandResponse{Message: msg}, nil
}

func (s *adminService) PushRevocations(ctx context.Context, req *machinev1.PushRevocationsRequest) (*machinev1.PushRevocationsResponse, error) {
	if principalFrom(ctx).Kind != PrincipalOwner {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("owner only"))
	}
	if req.RootChain != nil {
		s.n.acceptRootChain(req.RootChain)
	}
	s.n.applyRevocations(req.Revocations)
	return &machinev1.PushRevocationsResponse{Applied: true}, nil
}

func (s *adminService) ListRepos(ctx context.Context, _ *machinev1.ListReposRequest) (*machinev1.ListReposResponse, error) {
	if principalFrom(ctx).Kind != PrincipalOwner {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("owner only"))
	}
	return &machinev1.ListReposResponse{Repos: FindRepos(ctx)}, nil
}

func (s *adminService) ListFolders(ctx context.Context, req *machinev1.ListFoldersRequest) (*machinev1.ListFoldersResponse, error) {
	if principalFrom(ctx).Kind != PrincipalOwner {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("owner only"))
	}
	return ListFolders(req.Path, req.Hidden)
}

func (s *adminService) TestAgent(ctx context.Context, req *machinev1.TestAgentRequest) (*machinev1.TestAgentResponse, error) {
	if principalFrom(ctx).Kind != PrincipalOwner {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("owner only"))
	}
	return s.n.TestAgent(ctx, req.Agent)
}

// ApplyAdminCommand verifies a root-signed command and executes it.
func (n *Machine) ApplyAdminCommand(ctx context.Context, signed *identityv1.Signed) (string, error) {
	trust := n.currentTrust()
	if trust == nil {
		return "", connect.NewError(connect.CodePermissionDenied, errors.New("this machine has no owner"))
	}
	cmd, err := trust.VerifyAdminCommand(signed, n.MachineID(), time.Now())
	if err != nil {
		n.audit.Log(AuditRecord{Kind: "admin", Decision: "deny", Reason: err.Error()})
		return "", connect.NewError(connect.CodePermissionDenied, err)
	}
	return n.applyAdmin(ctx, cmd, "owner")
}

// ApplySettings applies an unsigned command from one of the owner's
// trusted devices (the caller checked the principal): only the machine's
// settings, which grant nothing (projects, pausing, the name, agent
// defaults).
func (n *Machine) ApplySettings(ctx context.Context, cmd *identityv1.AdminCommand) (string, error) {
	switch cmd.Command.(type) {
	case *identityv1.AdminCommand_AddProject, *identityv1.AdminCommand_RemoveProject, *identityv1.AdminCommand_SetPaused, *identityv1.AdminCommand_SetName,
		*identityv1.AdminCommand_SetAgentDefaults, *identityv1.AdminCommand_CreateFolder, *identityv1.AdminCommand_SetMaxRuntimeMode, *identityv1.AdminCommand_SetWorkspacePolicy:
	default:
		n.audit.Log(AuditRecord{Kind: "admin", Decision: "deny", Reason: "unsigned command that is not a setting"})
		return "", connect.NewError(connect.CodePermissionDenied, errors.New("only settings (projects, pausing, the name, agent defaults, storage) may be sent without a signature"))
	}
	if cmd.OwnerId != n.OwnerID() || cmd.TargetMachineId != n.MachineID() {
		return "", connect.NewError(connect.CodePermissionDenied, errors.New("settings for another owner or machine"))
	}
	if cmd.ExpiresAt == nil || time.Now().After(cmd.ExpiresAt.AsTime()) {
		return "", connect.NewError(connect.CodePermissionDenied, errors.New("expired command"))
	}
	return n.applyAdmin(ctx, cmd, "owner device")
}

func (n *Machine) applyAdmin(ctx context.Context, cmd *identityv1.AdminCommand, subject string) (string, error) {
	n.mu.Lock()
	for id, t := range n.adminSeen {
		if time.Since(t) > 10*time.Minute {
			delete(n.adminSeen, id)
		}
	}
	if _, dup := n.adminSeen[cmd.CommandId]; dup {
		n.mu.Unlock()
		return "", connect.NewError(connect.CodeAlreadyExists, errors.New("command already applied"))
	}
	n.adminSeen[cmd.CommandId] = time.Now()
	n.mu.Unlock()
	msg, err := n.execAdmin(ctx, cmd)
	dec := "allow"
	reason := ""
	if err != nil {
		dec, reason = "error", err.Error()
	}
	n.audit.Log(AuditRecord{Kind: "admin", Subject: subject, Action: fmt.Sprintf("%T", cmd.Command), Decision: dec, Reason: reason, Detail: msg})
	return msg, err
}

func (n *Machine) execAdmin(ctx context.Context, cmd *identityv1.AdminCommand) (string, error) {
	switch cmd.Command.(type) {
	case *identityv1.AdminCommand_SetHer, *identityv1.AdminCommand_SetHerAssistantMode, *identityv1.AdminCommand_SetHerCommandConnector:
		if n.cfg.TestBeforeHer {
			return "", connect.NewError(connect.CodeInvalidArgument, errors.New("unknown admin command")) // as 0.1.4 answered
		}
	}
	switch c := cmd.Command.(type) {
	case *identityv1.AdminCommand_AddProject:
		p := c.AddProject
		if !projectIDRE.MatchString(p.ProjectId) {
			return "", connect.NewError(connect.CodeInvalidArgument, errors.New("project id must be lowercase letters, digits, '.', '_' or '-'"))
		}
		if p.ProjectId == kernel.ScratchProjectID {
			return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%q is the name of the machine's temporary workspace; choose another", kernel.ScratchProjectID))
		}
		if utf8.RuneCountInString(p.Description) > maxDescription {
			return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("the description is longer than %d characters", maxDescription))
		}
		// p.Agents, from an older device, is ignored: every signed-in agent
		// provider may work here (docs/settings-environments.md §5.6).
		mode, path, err := projectWorkspace(ctx, p.Workspace, p.Path)
		if err != nil {
			return "", connect.NewError(connect.CodeInvalidArgument, err)
		}
		base := p.DefaultBaseRef
		if mode == identityv1.Workspace_WORKSPACE_IN_PLACE {
			if base != "" {
				return "", connect.NewError(connect.CodeInvalidArgument, errors.New("an in-place project has no base ref (sessions work in the directory as it is)"))
			}
		} else if base == "" {
			base = "HEAD"
		}
		// Adding a project again with its own folder changes its settings (the
		// apps do this); pointing an existing name at another folder would move
		// every grant that names it there, so that takes a remove first.
		if old, err := n.st.Project(p.ProjectId); err != nil {
			return "", err
		} else if old != nil && !samePath(old.Path, path) {
			return "", connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("project %q is already the folder %s; remove it first to use another folder", p.ProjectId, old.Path))
		}
		defer n.changes().project(p.ProjectId)
		if err := n.st.PutProject(Project{ID: p.ProjectId, Path: path, DefaultBaseRef: base, Workspace: mode, Description: strings.TrimSpace(p.Description)}); err != nil {
			return "", err
		}
		if n.sessions != nil {
			_ = n.sessions.SyncWorkspace(p.ProjectId, path, base, mode, strings.TrimSpace(p.Description), nil)
		}
		if mode == identityv1.Workspace_WORKSPACE_IN_PLACE {
			return "project " + p.ProjectId + " added (sessions work in place)", nil
		}
		return "project " + p.ProjectId + " added (a worktree per session)", nil
	case *identityv1.AdminCommand_RemoveProject:
		defer n.changes().project(c.RemoveProject.ProjectId)
		if n.sessions != nil && c.RemoveProject.ProjectId != kernel.ScratchProjectID {
			_ = n.sessions.RemoveWorkspace(c.RemoveProject.ProjectId)
		}
		return "project removed", n.st.DeleteProject(c.RemoveProject.ProjectId)
	case *identityv1.AdminCommand_SetName:
		if err := n.SetName(c.SetName.Name); err != nil {
			return "", err
		}
		n.kickStatus()
		return "renamed to " + n.Name(), nil
	case *identityv1.AdminCommand_CreateFolder:
		path, err := CreateFolder(ctx, c.CreateFolder)
		if err != nil {
			return "", err
		}
		return "folder " + path + " created", nil
	case *identityv1.AdminCommand_SetMaxRuntimeMode:
		if err := n.setMaxRuntimeMode(c.SetMaxRuntimeMode.Mode); err != nil {
			return "", err
		}
		n.kickStatus()
		return "permission mode limited to " + c.SetMaxRuntimeMode.Mode.String(), nil
	case *identityv1.AdminCommand_SetAgentDefaults:
		if err := n.setAgentDefaults(c.SetAgentDefaults); err != nil {
			return "", connect.NewError(connect.CodeInvalidArgument, err)
		}
		n.kickStatus()
		return "defaults set for " + c.SetAgentDefaults.Agent, nil
	case *identityv1.AdminCommand_SetPaused:
		n.mu.Lock()
		n.paused = c.SetPaused.Paused
		n.mu.Unlock()
		v := "0"
		if c.SetPaused.Paused {
			v = "1"
		}
		n.st.Set(kPaused, []byte(v))
		if !c.SetPaused.Paused {
			select {
			case n.inboxWake <- struct{}{}:
			default:
			}
		}
		return "paused=" + v, nil
	case *identityv1.AdminCommand_SetWorkspacePolicy:
		if err := n.setAutoRelease(c.SetWorkspacePolicy.AutoRelease); err != nil {
			return "", err
		}
		return fmt.Sprintf("auto-release=%t", c.SetWorkspacePolicy.AutoRelease), nil
	case *identityv1.AdminCommand_SetHer:
		return n.setHer(c.SetHer.Enabled, c.SetHer.Erase)
	case *identityv1.AdminCommand_SetHerAssistantMode, *identityv1.AdminCommand_SetHerCommandConnector:
		return n.execHer(ctx, cmd)
	}
	return "", errUnknownAdmin
}

// errUnknownAdmin answers a command this version doesn't know: one from a
// device newer than this machine (docs/version-skew.md §6.3).
var errUnknownAdmin = connect.NewError(connect.CodeUnimplemented, errors.New("unknown admin command"))
