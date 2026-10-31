package owner

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/local/v1/localv1connect"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/localipc"
)

// ServeLocal serves LocalService on a local endpoint that only the same
// user may connect to. It blocks until ctx is done.
func (d *Device) ServeLocal(ctx context.Context, sockPath string) error {
	ln, err := localipc.Listen(sockPath)
	if err != nil {
		return err
	}
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{Handler: d.LocalHandler(), Protocols: &protocols, ConnContext: func(ctx context.Context, c net.Conn) context.Context {
		if p, ok := localipc.Peer(c); ok {
			return context.WithValue(ctx, callerKey{}, &localv1.Caller{Pid: int32(p.PID), Executable: p.Executable})
		}
		return ctx
	}}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	err = srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// LocalHandler serves LocalService over HTTP, for a caller that provides
// its own listener and access control (the iPhone app's loopback server).
func (d *Device) LocalHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle(localv1connect.NewLocalServiceHandler(&localService{d: d}, connect.WithInterceptors(connect.UnaryInterceptorFunc(d.kickAfterWrites))))
	return mux
}

// kickAfterWrites tells the screens (Watch) to look again after a call that
// may have changed what they show: any but a read.
func (d *Device) kickAfterWrites(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		res, err := next(ctx, req)
		name := req.Spec().Procedure[strings.LastIndex(req.Spec().Procedure, "/")+1:]
		if !strings.HasPrefix(name, "Get") && !strings.HasPrefix(name, "List") && !strings.HasPrefix(name, "Lookup") {
			d.mu.Lock()
			for _, o := range d.profiles {
				o.rep.mu.Lock()
				o.rep.kickLocked()
				o.rep.mu.Unlock()
			}
			d.mu.Unlock()
		}
		return res, err
	}
}

// localService serves the current profile (profiles.go).
type localService struct{ d *Device }

func (l *localService) o() *Owner { return l.d.owner() }

func (l *localService) RegisterPush(ctx context.Context, req *localv1.RegisterPushRequest) (*localv1.RegisterPushResponse, error) {
	return &localv1.RegisterPushResponse{}, l.o().RegisterPush(ctx, req)
}

type callerKey struct{}

// callerFrom is the local process that made the call in ctx, if known.
func callerFrom(ctx context.Context) *localv1.Caller {
	c, _ := ctx.Value(callerKey{}).(*localv1.Caller)
	return c
}

// GetStatus never waits on the relay: RelayConnected is the connection the
// owner keeps (stayConnected).
func (l *localService) GetStatus(ctx context.Context, _ *localv1.GetStatusRequest) (*localv1.GetStatusResponse, error) {
	return l.o().Status(), nil
}

func (l *localService) ListProfiles(context.Context, *localv1.ListProfilesRequest) (*localv1.ListProfilesResponse, error) {
	return &localv1.ListProfilesResponse{Profiles: l.d.ListProfiles()}, nil
}

func (l *localService) SwitchProfile(_ context.Context, req *localv1.SwitchProfileRequest) (*localv1.SwitchProfileResponse, error) {
	return &localv1.SwitchProfileResponse{}, l.d.SwitchProfile(req.Id)
}

func (l *localService) AddProfile(context.Context, *localv1.AddProfileRequest) (*localv1.AddProfileResponse, error) {
	id, err := l.d.AddProfile()
	return &localv1.AddProfileResponse{Id: id}, err
}

func (l *localService) SignOut(ctx context.Context, _ *localv1.SignOutRequest) (*localv1.SignOutResponse, error) {
	return &localv1.SignOutResponse{}, l.d.SignOut(ctx)
}

func (l *localService) SignIn(ctx context.Context, req *localv1.SignInRequest) (*localv1.SignInResponse, error) {
	return &localv1.SignInResponse{}, l.d.SignIn(ctx, req)
}

func (l *localService) Setup(ctx context.Context, req *localv1.SetupRequest) (*localv1.SetupResponse, error) {
	return l.o().Setup(ctx, req)
}

func (l *localService) RequestJoin(ctx context.Context, req *localv1.RequestJoinRequest) (*localv1.RequestJoinResponse, error) {
	if err := l.d.checkNewOwner(req.OwnerId); err != nil {
		return nil, err
	}
	return l.o().RequestJoin(ctx, req)
}

func (l *localService) WaitJoin(ctx context.Context, req *localv1.WaitJoinRequest) (*localv1.WaitJoinResponse, error) {
	return l.o().WaitJoin(ctx, req.Code)
}

func (l *localService) ConfirmJoin(ctx context.Context, req *localv1.ConfirmJoinRequest) (*localv1.ConfirmJoinResponse, error) {
	return l.o().ConfirmJoin(ctx, req.Code, req.Accept, req.MatchCode)
}

func (l *localService) ListDevices(ctx context.Context, _ *localv1.ListDevicesRequest) (*localv1.ListDevicesResponse, error) {
	d, err := l.o().ListDevices(ctx)
	return &localv1.ListDevicesResponse{Devices: d}, err
}

func (l *localService) ApproveDevicePairing(ctx context.Context, req *localv1.ApproveDevicePairingRequest) (*localv1.ApproveDevicePairingResponse, error) {
	id, match, err := l.o().ApproveDevicePairing(ctx, req.Code)
	l.o().wrote(ctx, err, true)
	return &localv1.ApproveDevicePairingResponse{KeyId: id, MatchCode: match}, err
}

func (l *localService) RemoveDevice(ctx context.Context, req *localv1.RemoveDeviceRequest) (*localv1.RemoveDeviceResponse, error) {
	res, err := l.o().RemoveDevice(ctx, req.KeyId)
	l.o().wrote(ctx, err, true)
	return res, err
}

func (l *localService) Leave(ctx context.Context, _ *localv1.LeaveRequest) (*localv1.LeaveResponse, error) {
	return &localv1.LeaveResponse{}, l.d.Leave(ctx)
}

func (l *localService) EraseEverything(ctx context.Context, _ *localv1.EraseEverythingRequest) (*localv1.EraseEverythingResponse, error) {
	id, err := l.d.EraseEverything(ctx)
	if err != nil {
		return nil, err
	}
	return &localv1.EraseEverythingResponse{EraseId: id}, nil
}

func (l *localService) EraseStatus(ctx context.Context, req *localv1.EraseStatusRequest) (*localv1.EraseStatusResponse, error) {
	return l.o().EraseStatus(ctx, req.EraseId)
}

func (l *localService) FinishErase(ctx context.Context, _ *localv1.FinishEraseRequest) (*localv1.FinishEraseResponse, error) {
	return &localv1.FinishEraseResponse{}, l.d.FinishErase(ctx)
}

func (l *localService) WatchSigningRequests(ctx context.Context, req *localv1.WatchSigningRequestsRequest, stream *connect.ServerStream[localv1.SigningRequest]) error {
	ch, stop := l.d.hub.watch(req.Show)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case r := <-ch:
			if err := stream.Send(r); err != nil {
				return err
			}
		}
	}
}

func (l *localService) CompleteSigningRequest(ctx context.Context, req *localv1.CompleteSigningRequestRequest) (*localv1.CompleteSigningRequestResponse, error) {
	if err := l.d.hub.complete(req); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return &localv1.CompleteSigningRequestResponse{}, nil
}

func (l *localService) ShowSigner(context.Context, *localv1.ShowSignerRequest) (*localv1.ShowSignerResponse, error) {
	l.d.hub.show()
	return &localv1.ShowSignerResponse{}, nil
}

func (l *localService) SetLocalMachine(ctx context.Context, req *localv1.SetLocalMachineRequest) (*localv1.SetLocalMachineResponse, error) {
	id, err := l.o().SetLocalMachine(ctx, req.Enabled, req.MachineName)
	l.o().wrote(ctx, err, true)
	if err != nil {
		return nil, err
	}
	return &localv1.SetLocalMachineResponse{MachineId: id}, nil
}

func (l *localService) LookupPairing(ctx context.Context, req *localv1.LookupPairingRequest) (*localv1.LookupPairingResponse, error) {
	d, err := l.o().LookupPairing(ctx, req.Code)
	if err != nil {
		return nil, err
	}
	return &localv1.LookupPairingResponse{Details: d}, nil
}

func (l *localService) ListPairingRequests(ctx context.Context, _ *localv1.ListPairingRequestsRequest) (*localv1.ListPairingRequestsResponse, error) {
	ps, err := l.o().ListPairingRequests(ctx)
	if err != nil {
		return nil, err
	}
	return &localv1.ListPairingRequestsResponse{Pairings: ps}, nil
}

func (l *localService) ApproveMachinePairing(ctx context.Context, req *localv1.ApproveMachinePairingRequest) (*localv1.ApproveMachinePairingResponse, error) {
	id, match, err := l.o().ApproveMachinePairing(ctx, req.Code, req.MachineName)
	l.o().wrote(ctx, err, true)
	if err != nil {
		return nil, err
	}
	return &localv1.ApproveMachinePairingResponse{MachineId: id, MatchCode: match}, nil
}

func (l *localService) ApproveClientPairing(ctx context.Context, req *localv1.ApproveClientPairingRequest) (*localv1.ApproveClientPairingResponse, error) {
	if req.Spec == nil {
		req.Spec = &localv1.GrantSpec{}
	}
	cid, gid, err := l.o().ApproveClientPairing(ctx, req.Code, req.Spec)
	l.o().wrote(ctx, err, true)
	if err != nil {
		return nil, err
	}
	return &localv1.ApproveClientPairingResponse{ClientId: cid, GrantId: gid}, nil
}

func (l *localService) ListMachines(ctx context.Context, _ *localv1.ListMachinesRequest) (*localv1.ListMachinesResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return &localv1.ListMachinesResponse{Machines: l.o().onlyMachineList(m)}, nil
	}
	machines, err := l.o().machines(ctx)
	if err != nil {
		return nil, err
	}
	l.o().machineStatus(machines)
	return &localv1.ListMachinesResponse{Machines: machines}, nil
}

func (l *localService) RenameMachine(ctx context.Context, req *localv1.RenameMachineRequest) (*localv1.RenameMachineResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err == nil {
			err = onlyMachineSettings(ctx, m, req.MachineId, func(c *identityv1.AdminCommand) {
				c.Command = &identityv1.AdminCommand_SetName{SetName: &identityv1.SetName{Name: SanitizeMachineName(req.MachineName)}}
			})
		}
		return &localv1.RenameMachineResponse{}, err
	}
	err := l.o().RenameMachine(ctx, req.MachineId, req.MachineName)
	l.o().wrote(ctx, err, true, req.MachineId)
	return &localv1.RenameMachineResponse{}, err
}

func (l *localService) SetMachinePaused(ctx context.Context, req *localv1.SetMachinePausedRequest) (*localv1.SetMachinePausedResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err == nil {
			err = onlyMachineSettings(ctx, m, req.MachineId, func(c *identityv1.AdminCommand) {
				c.Command = &identityv1.AdminCommand_SetPaused{SetPaused: &identityv1.SetPaused{Paused: req.Paused}}
			})
		}
		return &localv1.SetMachinePausedResponse{}, err
	}
	err := l.o().SetMachinePaused(ctx, req.MachineId, req.Paused)
	l.o().wrote(ctx, err, false, req.MachineId)
	return &localv1.SetMachinePausedResponse{}, err
}

func (l *localService) SetAgentDefaults(ctx context.Context, req *localv1.SetAgentDefaultsRequest) (*localv1.SetAgentDefaultsResponse, error) {
	if req.Defaults == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("no defaults"))
	}
	if m, ok, err := l.o().onlyMachine(); ok {
		if err == nil {
			err = onlyMachineSettings(ctx, m, req.MachineId, func(c *identityv1.AdminCommand) {
				c.Command = &identityv1.AdminCommand_SetAgentDefaults{SetAgentDefaults: req.Defaults}
			})
		}
		return &localv1.SetAgentDefaultsResponse{}, err
	}
	err := l.o().SetAgentDefaults(ctx, req.MachineId, req.Defaults)
	l.o().wrote(ctx, err, false, req.MachineId)
	return &localv1.SetAgentDefaultsResponse{}, err
}

// CheckAgents checks a machine's coding agents again: another machine end
// to end, waiting for it; this device's own in the background.
func (l *localService) CheckAgents(ctx context.Context, req *localv1.CheckAgentsRequest) (*localv1.CheckAgentsResponse, error) {
	if req.MachineId != "" && (l.o().local() == nil || l.o().local().MachineID() != req.MachineId) {
		return &localv1.CheckAgentsResponse{}, l.o().RecheckAgents(ctx, req.MachineId)
	}
	m, ok, err := l.o().onlyMachine()
	if !ok {
		m = l.o().local()
	}
	if err == nil && m == nil {
		err = connect.NewError(connect.CodeFailedPrecondition, errors.New("this computer shares no coding agents"))
	}
	if err != nil {
		return nil, err
	}
	m.CheckAgents()
	return &localv1.CheckAgentsResponse{}, nil
}

func (l *localService) ListMachineRepos(ctx context.Context, req *localv1.ListMachineReposRequest) (*localv1.ListMachineReposResponse, error) {
	repos, err := l.o().MachineRepos(ctx, req.MachineId)
	return &localv1.ListMachineReposResponse{Repos: repos}, err
}

func (l *localService) ListMachineFolders(ctx context.Context, req *localv1.ListMachineFoldersRequest) (*machinev1.ListFoldersResponse, error) {
	return l.o().MachineFolders(ctx, req.MachineId, req.Path, req.Hidden)
}

func (l *localService) TestMachineAgent(ctx context.Context, req *localv1.TestMachineAgentRequest) (*machinev1.TestAgentResponse, error) {
	return l.o().TestMachineAgent(ctx, req.MachineId, req.Agent)
}

// GetDiagnostics describes this Mac: what it is to its owner, then its
// machine, when it shares its coding agents.
func (l *localService) GetDiagnostics(ctx context.Context, _ *localv1.GetDiagnosticsRequest) (*localv1.GetDiagnosticsResponse, error) {
	st := l.o().Status()
	role := "not set up"
	switch {
	case st.MachineOnly:
		role = "machine only"
	case st.Initialized:
		role = "trusted device"
	}
	text := fmt.Sprintf("this device  %s, owner %s\n", role, st.OwnerId)
	m, ok, err := l.o().onlyMachine()
	if !ok {
		m, err = l.o().local(), nil
	}
	switch {
	case err != nil:
		text += "its machine  not running: " + err.Error() + "\n"
	case m == nil:
		text += "its machine  none: it shares no coding agents\n"
	default:
		text += m.Diagnostics()
	}
	return &localv1.GetDiagnosticsResponse{Text: text}, nil
}

func (l *localService) RemoveMachine(ctx context.Context, req *localv1.RemoveMachineRequest) (*localv1.RemoveMachineResponse, error) {
	err := l.o().RemoveMachine(ctx, req.MachineId)
	l.o().wrote(ctx, err, true)
	return &localv1.RemoveMachineResponse{}, err
}

func (l *localService) ListProjects(ctx context.Context, req *localv1.ListProjectsRequest) (*localv1.ListProjectsResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err == nil {
			err = onlyThisMachine(m, req.MachineId)
		}
		if err != nil {
			return nil, err
		}
		return onlyMachineProjects(ctx, m)
	}
	return l.o().ListProjects(ctx, req.MachineId)
}

func (l *localService) AddProject(ctx context.Context, req *localv1.AddProjectRequest) (*localv1.AddProjectResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err == nil {
			err = onlyMachineSettings(ctx, m, req.MachineId, func(c *identityv1.AdminCommand) {
				c.Command = &identityv1.AdminCommand_AddProject{AddProject: &identityv1.AddProject{ProjectId: req.ProjectId, Path: req.Path, DefaultBaseRef: req.DefaultBaseRef,
					Workspace: req.Workspace, Description: req.Description, Agents: req.Agents}}
			})
		}
		return &localv1.AddProjectResponse{}, err
	}
	err := l.o().AddProject(ctx, req)
	l.o().wrote(ctx, err, false, req.MachineId)
	return &localv1.AddProjectResponse{}, err
}

func (l *localService) RemoveProject(ctx context.Context, req *localv1.RemoveProjectRequest) (*localv1.RemoveProjectResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err == nil {
			err = onlyMachineSettings(ctx, m, req.MachineId, func(c *identityv1.AdminCommand) {
				c.Command = &identityv1.AdminCommand_RemoveProject{RemoveProject: &identityv1.RemoveProject{ProjectId: req.ProjectId}}
			})
		}
		return &localv1.RemoveProjectResponse{}, err
	}
	err := l.o().RemoveProject(ctx, req.MachineId, req.ProjectId)
	l.o().wrote(ctx, err, false, req.MachineId)
	return &localv1.RemoveProjectResponse{}, err
}

func (l *localService) ListClients(ctx context.Context, _ *localv1.ListClientsRequest) (*localv1.ListClientsResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return &localv1.ListClientsResponse{Clients: onlyMachineClients(m)}, nil
	}
	clients, err := l.o().ListClients(ctx)
	if err != nil {
		return nil, err
	}
	return &localv1.ListClientsResponse{Clients: clients}, nil
}

func (l *localService) RenameClient(ctx context.Context, req *localv1.RenameClientRequest) (*localv1.RenameClientResponse, error) {
	v, err := l.o().RenameClient(ctx, req.GrantId, req.Label, req.Icon, req.Image)
	l.o().wrote(ctx, err, true)
	if err != nil {
		return nil, err
	}
	return &localv1.RenameClientResponse{GrantVersion: v}, nil
}

func (l *localService) UpdateClient(ctx context.Context, req *localv1.UpdateClientRequest) (*localv1.UpdateClientResponse, error) {
	if req.Spec == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("missing spec"))
	}
	v, acks, err := l.o().UpdateClient(ctx, req.GrantId, req.Spec)
	l.o().wrote(ctx, err, true)
	if err != nil {
		return nil, err
	}
	return &localv1.UpdateClientResponse{GrantVersion: v, Acks: acks}, nil
}

func (l *localService) RevokeClient(ctx context.Context, req *localv1.RevokeClientRequest) (*localv1.RevokeClientResponse, error) {
	acks, err := l.o().RevokeClient(ctx, req.GrantId)
	l.o().wrote(ctx, err, true)
	if err != nil {
		return nil, err
	}
	return &localv1.RevokeClientResponse{Acks: acks}, nil
}

func (l *localService) ListSessions(ctx context.Context, req *localv1.ListSessionsRequest) (*localv1.ListSessionsResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		res, err := m.ListSessions(&sessionv1.ListSessionsRequest{Limit: req.Limit, Before: req.Before, View: req.View})
		if err != nil {
			return nil, err
		}
		return &localv1.ListSessionsResponse{Sessions: res.Sessions, Next: res.Next}, nil
	}
	return l.o().ListSessions(ctx, req)
}

func (l *localService) QueryWork(ctx context.Context, req *localv1.QueryWorkRequest) (*localv1.QueryWorkResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return QueryWorkOnly(ctx, m, req)
	}
	return l.o().QueryWork(ctx, req)
}

func (l *localService) WatchSessions(ctx context.Context, _ *localv1.WatchSessionsRequest, stream *connect.ServerStream[localv1.WatchSessionsResponse]) error {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return err
		}
		return onlyMachineWatch(ctx, m, stream.Send)
	}
	return l.o().WatchSessions(ctx, stream.Send)
}

func (l *localService) StreamSessionEvents(ctx context.Context, req *localv1.StreamSessionEventsRequest, stream *connect.ServerStream[sessionv1.SessionEvent]) error {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return err
		}
		return m.StreamEvents(ctx, &sessionv1.StreamEventsRequest{SessionId: req.SessionId, AfterSeq: req.AfterSeq, Follow: req.Follow, Partial: req.Partial, View: req.View}, stream.Send)
	}
	return l.o().StreamSessionEvents(ctx, req, stream.Send)
}

func (l *localService) ListSessionEvents(ctx context.Context, req *localv1.ListSessionEventsRequest) (*localv1.ListSessionEventsResponse, error) {
	list := l.o().ListSessionEvents
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		list = func(_ context.Context, r *sessionv1.ListEventsRequest) (*sessionv1.ListEventsResponse, error) {
			return m.ListEvents(r)
		}
	}
	res, err := list(ctx, &sessionv1.ListEventsRequest{SessionId: req.SessionId, AfterSeq: req.AfterSeq, BeforeSeq: req.BeforeSeq, Limit: req.Limit, View: req.View})
	if err != nil {
		return nil, err
	}
	return &localv1.ListSessionEventsResponse{Events: res.Events, HasMoreBefore: res.HasMoreBefore, HasMoreAfter: res.HasMoreAfter}, nil
}

func (l *localService) ReadToolOutput(ctx context.Context, req *localv1.ReadToolOutputRequest, stream *connect.ServerStream[sessionv1.ToolOutputChunk]) error {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return err
		}
		return m.ReadToolOutput(&sessionv1.ReadToolOutputRequest{SessionId: req.SessionId, CallId: req.CallId}, stream.Send)
	}
	return l.o().ReadToolOutput(ctx, req, stream.Send)
}

func (l *localService) AnswerQuestion(ctx context.Context, req *localv1.AnswerQuestionRequest) (*localv1.AnswerQuestionResponse, error) {
	answer := &sessionv1.AnswerQuestionRequest{SessionId: req.SessionId, QuestionId: req.QuestionId, Answers: req.Answers, Decline: req.Decline}
	answerer := l.o().AnswerQuestion
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		answerer = m.AnswerQuestion
	}
	s, err := answerer(ctx, answer)
	if err != nil {
		return nil, err
	}
	return &localv1.AnswerQuestionResponse{Session: s}, nil
}

func (l *localService) InterruptSession(ctx context.Context, req *localv1.InterruptSessionRequest) (*localv1.SessionResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return sessionResponse(m.Interrupt(ctx, req.SessionId))
	}
	return sessionResponse(l.o().InterruptSession(ctx, req.SessionId))
}

func (l *localService) ArchiveSession(ctx context.Context, req *localv1.ArchiveSessionRequest) (*localv1.SessionResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return sessionResponse(m.Archive(ctx, req.SessionId))
	}
	return sessionResponse(l.o().ArchiveSession(ctx, req.SessionId))
}

func (l *localService) DeleteSession(ctx context.Context, req *localv1.DeleteSessionRequest) (*localv1.DeleteSessionResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err == nil {
			err = m.Delete(ctx, req.SessionId)
		}
		if err != nil {
			return nil, err
		}
		return &localv1.DeleteSessionResponse{}, nil
	}
	if err := l.o().DeleteSession(ctx, req.SessionId); err != nil {
		return nil, err
	}
	return &localv1.DeleteSessionResponse{}, nil
}

func (l *localService) RenameSession(ctx context.Context, req *localv1.RenameSessionRequest) (*localv1.SessionResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return sessionResponse(m.Rename(ctx, req.SessionId, req.Title))
	}
	return sessionResponse(l.o().RenameSession(ctx, req.SessionId, req.Title))
}

func (l *localService) UnarchiveSession(ctx context.Context, req *localv1.UnarchiveSessionRequest) (*localv1.SessionResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return sessionResponse(m.Unarchive(ctx, req.SessionId))
	}
	return sessionResponse(l.o().UnarchiveSession(ctx, req.SessionId))
}

func sessionResponse(s *sessionv1.Session, err error) (*localv1.SessionResponse, error) {
	if err != nil {
		return nil, err
	}
	return &localv1.SessionResponse{Session: s}, nil
}

func (l *localService) GetDiff(ctx context.Context, req *localv1.GetDiffRequest) (*sessionv1.GetDiffResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return m.Diff(ctx, &sessionv1.GetDiffRequest{SessionId: req.SessionId, Turn: req.Turn})
	}
	return l.o().GetDiff(ctx, req.SessionId, req.Turn)
}
