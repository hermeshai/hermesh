package owner

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"connectrpc.com/connect"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/local/v1/localv1connect"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/peercred"
)

// ServeLocal serves LocalService on a Unix socket that only the same user
// may connect to. It blocks until ctx is done.
func (d *Device) ServeLocal(ctx context.Context, sockPath string) error {
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o700); err != nil {
		return err
	}
	os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return err
	}
	os.Chmod(sockPath, 0o600)
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{Handler: d.LocalHandler(), Protocols: &protocols, ConnContext: func(ctx context.Context, c net.Conn) context.Context {
		if uc, ok := c.(*net.UnixConn); ok {
			p := peercred.PeerProcess(uc)
			return context.WithValue(ctx, callerKey{}, &localv1.Caller{Pid: int32(p.PID), Executable: p.Executable})
		}
		return ctx
	}}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	err = srv.Serve(&peercred.SameUserListener{UnixListener: ln.(*net.UnixListener)})
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// LocalHandler serves LocalService over HTTP, for a caller that provides
// its own listener and access control (the iPhone app's loopback server).
func (d *Device) LocalHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle(localv1connect.NewLocalServiceHandler(&localService{d: d}))
	return mux
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

func (l *localService) GetStatus(ctx context.Context, _ *localv1.GetStatusRequest) (*localv1.GetStatusResponse, error) {
	o := l.o()
	st := o.Status()
	if st.Initialized && !st.SignedOut && !st.RelayConnected {
		if _, err := o.relay(ctx); err == nil {
			st.RelayConnected = true
		}
	}
	return st, nil
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
	return &localv1.ApproveDevicePairingResponse{KeyId: id, MatchCode: match}, err
}

func (l *localService) RemoveDevice(ctx context.Context, req *localv1.RemoveDeviceRequest) (*localv1.RemoveDeviceResponse, error) {
	return l.o().RemoveDevice(ctx, req.KeyId)
}

func (l *localService) Leave(ctx context.Context, _ *localv1.LeaveRequest) (*localv1.LeaveResponse, error) {
	return &localv1.LeaveResponse{}, l.d.Leave(ctx)
}

func (l *localService) WatchSigningRequests(ctx context.Context, _ *localv1.WatchSigningRequestsRequest, stream *connect.ServerStream[localv1.SigningRequest]) error {
	ch, stop := l.d.hub.watch()
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

func (l *localService) SetLocalMachine(ctx context.Context, req *localv1.SetLocalMachineRequest) (*localv1.SetLocalMachineResponse, error) {
	id, err := l.o().SetLocalMachine(ctx, req.Enabled, req.MachineName)
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
	l.o().machineStatus(ctx, machines)
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
	return &localv1.RenameMachineResponse{}, l.o().RenameMachine(ctx, req.MachineId, req.MachineName)
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
	return &localv1.SetMachinePausedResponse{}, l.o().SetMachinePaused(ctx, req.MachineId, req.Paused)
}

// CheckAgents checks this Mac's coding agents again, in the background.
func (l *localService) CheckAgents(ctx context.Context, _ *localv1.CheckAgentsRequest) (*localv1.CheckAgentsResponse, error) {
	m, ok, err := l.o().onlyMachine()
	if !ok {
		m = l.o().local()
	}
	if err == nil && m == nil {
		err = connect.NewError(connect.CodeFailedPrecondition, errors.New("this Mac shares no coding agents"))
	}
	if err != nil {
		return nil, err
	}
	m.CheckAgents()
	return &localv1.CheckAgentsResponse{}, nil
}

func (l *localService) RemoveMachine(ctx context.Context, req *localv1.RemoveMachineRequest) (*localv1.RemoveMachineResponse, error) {
	return &localv1.RemoveMachineResponse{}, l.o().RemoveMachine(ctx, req.MachineId)
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
	return &localv1.AddProjectResponse{}, l.o().AddProject(ctx, req)
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
	return &localv1.RemoveProjectResponse{}, l.o().RemoveProject(ctx, req.MachineId, req.ProjectId)
}

func (l *localService) ListClients(ctx context.Context, _ *localv1.ListClientsRequest) (*localv1.ListClientsResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		// A machine only knows the agents that reached it, by the names their grants gave them.
		if err != nil {
			return nil, err
		}
		ids, grants, labels := m.Clients()
		res := &localv1.ListClientsResponse{}
		for i := range ids {
			res.Clients = append(res.Clients, &localv1.Client{ClientId: ids[i], GrantId: grants[i], Label: labels[i]})
		}
		slices.SortFunc(res.Clients, func(a, b *localv1.Client) int { return strings.Compare(a.Label, b.Label) })
		return res, nil
	}
	clients, err := l.o().ListClients(ctx)
	if err != nil {
		return nil, err
	}
	return &localv1.ListClientsResponse{Clients: clients}, nil
}

func (l *localService) UpdateClient(ctx context.Context, req *localv1.UpdateClientRequest) (*localv1.UpdateClientResponse, error) {
	if req.Spec == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("missing spec"))
	}
	v, acks, err := l.o().UpdateClient(ctx, req.GrantId, req.Spec)
	if err != nil {
		return nil, err
	}
	return &localv1.UpdateClientResponse{GrantVersion: v, Acks: acks}, nil
}

func (l *localService) RevokeClient(ctx context.Context, req *localv1.RevokeClientRequest) (*localv1.RevokeClientResponse, error) {
	acks, err := l.o().RevokeClient(ctx, req.GrantId)
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
		res, err := m.ListSessions(&sessionv1.ListSessionsRequest{Limit: req.Limit, Before: req.Before})
		if err != nil {
			return nil, err
		}
		return &localv1.ListSessionsResponse{Sessions: res.Sessions, Next: res.Next}, nil
	}
	return l.o().ListSessions(ctx, req)
}

func (l *localService) GetUsage(ctx context.Context, req *localv1.GetUsageRequest) (*localv1.GetUsageResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		res, err := m.Usage(&sessionv1.GetUsageRequest{Since: req.Since, TimeZone: req.TimeZone})
		if err != nil {
			return nil, err
		}
		return &localv1.GetUsageResponse{Days: res.Days}, nil
	}
	return l.o().GetUsage(ctx, req)
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
		return m.StreamEvents(ctx, &sessionv1.StreamEventsRequest{SessionId: req.SessionId, AfterSeq: req.AfterSeq, Follow: req.Follow, Partial: req.Partial}, stream.Send)
	}
	return l.o().StreamSessionEvents(ctx, req, stream.Send)
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
