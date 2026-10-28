package machine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

type sessionService struct{ n *Machine }

func (s *sessionService) StartSession(ctx context.Context, req *sessionv1.StartSessionRequest) (*sessionv1.StartSessionResponse, error) {
	sess, err := s.n.sessions.Start(ctx, principalFrom(ctx), req)
	if err != nil {
		return nil, err
	}
	return &sessionv1.StartSessionResponse{Session: sess}, nil
}

func (s *sessionService) GetSession(ctx context.Context, req *sessionv1.GetSessionRequest) (*sessionv1.Session, error) {
	return s.n.sessions.Get(principalFrom(ctx), req.SessionId)
}

func (s *sessionService) ListSessions(ctx context.Context, req *sessionv1.ListSessionsRequest) (*sessionv1.ListSessionsResponse, error) {
	list, next, err := s.n.sessions.List(principalFrom(ctx), req)
	if err != nil {
		return nil, err
	}
	return &sessionv1.ListSessionsResponse{Sessions: list, Next: next}, nil
}

func (s *sessionService) WatchSessions(ctx context.Context, req *sessionv1.WatchSessionsRequest, stream *connect.ServerStream[sessionv1.WatchSessionsResponse]) error {
	send := stream.Send
	if req.View == sessionv1.SessionView_SESSION_VIEW_BASIC {
		send = func(m *sessionv1.WatchSessionsResponse) error {
			return stream.Send(&sessionv1.WatchSessionsResponse{Sessions: BasicSessions(m.Sessions), DeletedSessionIds: m.DeletedSessionIds})
		}
	}
	return s.n.sessions.Watch(ctx, principalFrom(ctx), send)
}

func (s *sessionService) ReadToolOutput(ctx context.Context, req *sessionv1.ReadToolOutputRequest, stream *connect.ServerStream[sessionv1.ToolOutputChunk]) error {
	return s.n.sessions.ReadToolOutput(principalFrom(ctx), req, stream.Send)
}

func (s *sessionService) StreamEvents(ctx context.Context, req *sessionv1.StreamEventsRequest, stream *connect.ServerStream[sessionv1.SessionEvent]) error {
	return s.n.sessions.Stream(ctx, principalFrom(ctx), req, stream.Send)
}

func (s *sessionService) SendMessage(ctx context.Context, req *sessionv1.SendMessageRequest) (*sessionv1.SendMessageResponse, error) {
	sess, err := s.n.sessions.Send(ctx, principalFrom(ctx), req.SessionId, req.Text, req.Attachments)
	if err != nil {
		return nil, err
	}
	return &sessionv1.SendMessageResponse{Session: sess}, nil
}

func (s *sessionService) ListEvents(ctx context.Context, req *sessionv1.ListEventsRequest) (*sessionv1.ListEventsResponse, error) {
	return s.n.sessions.ListEvents(principalFrom(ctx), req)
}

func (s *sessionService) InterruptSession(ctx context.Context, req *sessionv1.InterruptSessionRequest) (*sessionv1.Session, error) {
	return s.n.sessions.Interrupt(ctx, principalFrom(ctx), req.SessionId)
}

func (s *sessionService) ArchiveSession(ctx context.Context, req *sessionv1.ArchiveSessionRequest) (*sessionv1.Session, error) {
	return s.n.sessions.Archive(ctx, principalFrom(ctx), req.SessionId)
}

func (s *sessionService) UnarchiveSession(ctx context.Context, req *sessionv1.UnarchiveSessionRequest) (*sessionv1.Session, error) {
	return s.n.sessions.Unarchive(ctx, principalFrom(ctx), req.SessionId)
}

func (s *sessionService) DeleteSession(ctx context.Context, req *sessionv1.DeleteSessionRequest) (*sessionv1.DeleteSessionResponse, error) {
	if err := s.n.sessions.Delete(ctx, principalFrom(ctx), req.SessionId); err != nil {
		return nil, err
	}
	return &sessionv1.DeleteSessionResponse{}, nil
}

func (s *sessionService) RenameSession(ctx context.Context, req *sessionv1.RenameSessionRequest) (*sessionv1.Session, error) {
	return s.n.sessions.Rename(ctx, principalFrom(ctx), req.SessionId, req.Title)
}

func (s *sessionService) GetDiff(ctx context.Context, req *sessionv1.GetDiffRequest) (*sessionv1.GetDiffResponse, error) {
	return s.n.sessions.Diff(ctx, principalFrom(ctx), req)
}

func (s *sessionService) AnswerQuestion(ctx context.Context, req *sessionv1.AnswerQuestionRequest) (*sessionv1.Session, error) {
	return s.n.sessions.AnswerQuestion(ctx, principalFrom(ctx), req)
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
	return &machinev1.GetInfoResponse{MachineId: s.n.machineID, MachineName: s.n.Name(), Status: s.n.Status()}, nil
}

func (s *machineService) Watch(ctx context.Context, req *machinev1.WatchRequest, stream *connect.ServerStream[machinev1.WatchResponse]) error {
	return s.n.Watch(ctx, principalFrom(ctx), req, stream.Send)
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
	active := s.n.sessions.ActiveByProject()
	out := &machinev1.ListProjectsResponse{}
	for _, pr := range projects {
		if p.Kind == PrincipalClient && !identity.Allows(p.Grant.ProjectIds, pr.ID) {
			continue
		}
		mp := &machinev1.Project{ProjectId: pr.ID, DefaultBaseRef: pr.DefaultBaseRef, Workspace: pr.Workspace,
			Path: pr.Path, Description: pr.Description, ActiveSessions: active[pr.ID], Agents: pr.Agents}
		if base {
			mp.Base = projectBase(ctx, pr)
		}
		out.Projects = append(out.Projects, mp)
	}
	return out, nil
}

// projectAgents checks the coding agents a project is for: known names,
// each once, in the order given (none: all).
func projectAgents(names []string) ([]string, error) {
	var out []string
	for _, a := range names {
		if !slices.Contains(AgentNames(), a) {
			return nil, fmt.Errorf("unknown agent %q (one of: %s)", a, strings.Join(AgentNames(), ", "))
		}
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out, nil
}

// projectWorkspace checks a project directory for a workspace mode; an
// unspecified mode is a worktree for a git repository root and in place
// for any other directory.
func projectWorkspace(ctx context.Context, mode identityv1.Workspace, path string) (identityv1.Workspace, string, error) {
	// "~/app", as a device types it or the repositories found are shown.
	if home, err := os.UserHomeDir(); err == nil && (path == "~" || strings.HasPrefix(path, "~/")) {
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

func (s *adminService) TestAgent(ctx context.Context, req *machinev1.TestAgentRequest) (*machinev1.TestAgentResponse, error) {
	if principalFrom(ctx).Kind != PrincipalOwner {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("owner only"))
	}
	return s.n.TestAgent(ctx, req.Agent)
}

// ApplyAdminCommand verifies a root-signed command and executes it.
func (n *Machine) ApplyAdminCommand(ctx context.Context, signed *identityv1.Signed) (string, error) {
	cmd, err := n.trust.VerifyAdminCommand(signed, n.machineID, time.Now())
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
		*identityv1.AdminCommand_SetAgentDefaults:
	default:
		n.audit.Log(AuditRecord{Kind: "admin", Decision: "deny", Reason: "unsigned command that is not a setting"})
		return "", connect.NewError(connect.CodePermissionDenied, errors.New("only settings (projects, pausing, the name, agent defaults) may be sent without a signature"))
	}
	if cmd.OwnerId != n.OwnerID() || cmd.TargetMachineId != n.machineID {
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
	switch c := cmd.Command.(type) {
	case *identityv1.AdminCommand_AddProject:
		p := c.AddProject
		if !projectIDRE.MatchString(p.ProjectId) {
			return "", connect.NewError(connect.CodeInvalidArgument, errors.New("project id must be lowercase letters, digits, '.', '_' or '-'"))
		}
		if utf8.RuneCountInString(p.Description) > maxDescription {
			return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("the description is longer than %d characters", maxDescription))
		}
		agents, err := projectAgents(p.Agents)
		if err != nil {
			return "", connect.NewError(connect.CodeInvalidArgument, err)
		}
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
		defer n.sessions.changes().project(p.ProjectId)
		if err := n.st.PutProject(Project{ID: p.ProjectId, Path: path, DefaultBaseRef: base, Workspace: mode, Description: strings.TrimSpace(p.Description), Agents: agents}); err != nil {
			return "", err
		}
		if mode == identityv1.Workspace_WORKSPACE_IN_PLACE {
			return "project " + p.ProjectId + " added (sessions work in place)", nil
		}
		return "project " + p.ProjectId + " added (a worktree per session)", nil
	case *identityv1.AdminCommand_RemoveProject:
		defer n.sessions.changes().project(c.RemoveProject.ProjectId)
		return "project removed", n.st.DeleteProject(c.RemoveProject.ProjectId)
	case *identityv1.AdminCommand_SetName:
		if err := n.SetName(c.SetName.Name); err != nil {
			return "", err
		}
		n.kickStatus()
		return "renamed to " + n.Name(), nil
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
	}
	return "", connect.NewError(connect.CodeInvalidArgument, errors.New("unknown admin command"))
}

func (s *sessionService) GetUsage(ctx context.Context, req *sessionv1.GetUsageRequest) (*sessionv1.GetUsageResponse, error) {
	return s.n.sessions.Usage(principalFrom(ctx), req)
}

func (s *sessionService) GetActivity(ctx context.Context, req *sessionv1.GetActivityRequest) (*sessionv1.GetActivityResponse, error) {
	return s.n.sessions.Activity(principalFrom(ctx), req, time.Now())
}
