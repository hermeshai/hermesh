package kernel

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/session/v1/sessionv1connect"
	"github.com/hermeshai/hermesh/internal/kernel/store"
)

// Service adapts Engine to sessionv1connect.SessionServiceHandler.
type Service struct {
	engine    *Engine
	principal func(context.Context) *Principal
}

var _ sessionv1connect.SessionServiceHandler = (*Service)(nil)

func NewService(engine *Engine, principal func(context.Context) *Principal) *Service {
	return &Service{engine: engine, principal: principal}
}

func (s *Service) ListProviders(ctx context.Context, req *sessionv1.ListProvidersRequest) (*sessionv1.ListProvidersResponse, error) {
	return s.engine.ListProviders(ctx, s.principal(ctx), req.ProjectId, req.Refresh)
}

func (s *Service) UpsertProviderInstance(ctx context.Context, req *sessionv1.UpsertProviderInstanceRequest) (*sessionv1.UpsertProviderInstanceResponse, error) {
	inst, err := s.engine.UpsertProviderInstance(ctx, s.principal(ctx), req.Config)
	if err != nil {
		return nil, err
	}
	return &sessionv1.UpsertProviderInstanceResponse{Provider: inst}, nil
}

func (s *Service) RemoveProviderInstance(ctx context.Context, req *sessionv1.RemoveProviderInstanceRequest) (*sessionv1.RemoveProviderInstanceResponse, error) {
	return &sessionv1.RemoveProviderInstanceResponse{}, s.engine.RemoveProviderInstance(ctx, s.principal(ctx), req.InstanceId)
}

func (s *Service) StartSession(ctx context.Context, req *sessionv1.StartSessionRequest) (*sessionv1.StartSessionResponse, error) {
	return s.engine.StartSession(ctx, s.principal(ctx), req)
}

func (s *Service) GetSession(ctx context.Context, req *sessionv1.GetSessionRequest) (*sessionv1.GetSessionResponse, error) {
	proj, err := s.engine.GetSession(ctx, s.principal(ctx), req.SessionId)
	if err != nil {
		return nil, err
	}
	return &sessionv1.GetSessionResponse{Projection: proj}, nil
}

func (s *Service) ListSessions(ctx context.Context, req *sessionv1.ListSessionsRequest) (*sessionv1.ListSessionsResponse, error) {
	return s.engine.ListSessions(ctx, s.principal(ctx), req)
}

func (s *Service) WatchSessions(ctx context.Context, req *sessionv1.WatchSessionsRequest, stream *connect.ServerStream[sessionv1.WatchSessionsResponse]) error {
	return s.engine.WatchSessions(ctx, s.principal(ctx), req.IncludeArchived, stream.Send)
}

func (s *Service) SubscribeSession(ctx context.Context, req *sessionv1.SubscribeSessionRequest, stream *connect.ServerStream[sessionv1.SessionEvent]) error {
	return s.engine.SubscribeSession(ctx, s.principal(ctx), req.SessionId, req.AfterSeq, stream.Send)
}

func (s *Service) DispatchMessage(ctx context.Context, req *sessionv1.DispatchMessageRequest) (*sessionv1.DispatchMessageResponse, error) {
	return s.engine.DispatchMessage(ctx, s.principal(ctx), req)
}

func (s *Service) DeliverMessage(ctx context.Context, req *sessionv1.DeliverMessageRequest) (*sessionv1.DeliverMessageResponse, error) {
	return s.engine.DeliverMessage(ctx, s.principal(ctx), req)
}

func (s *Service) InterruptSession(ctx context.Context, req *sessionv1.InterruptSessionRequest) (*sessionv1.InterruptSessionResponse, error) {
	return s.engine.InterruptSession(ctx, s.principal(ctx), req)
}

func (s *Service) StopBackgroundTask(ctx context.Context, req *sessionv1.StopBackgroundTaskRequest) (*sessionv1.StopBackgroundTaskResponse, error) {
	return s.engine.StopBackgroundTask(ctx, s.principal(ctx), req)
}

func (s *Service) ManageQueue(ctx context.Context, req *sessionv1.ManageQueueRequest) (*sessionv1.ManageQueueResponse, error) {
	return s.engine.ManageQueue(ctx, s.principal(ctx), req)
}

func (s *Service) UpdateSession(ctx context.Context, req *sessionv1.UpdateSessionRequest) (*sessionv1.UpdateSessionResponse, error) {
	return s.engine.UpdateSession(ctx, s.principal(ctx), req)
}

func (s *Service) RespondRuntimeRequest(ctx context.Context, req *sessionv1.RespondRuntimeRequestRequest) (*sessionv1.RespondRuntimeRequestResponse, error) {
	return s.engine.RespondRuntimeRequest(ctx, s.principal(ctx), req)
}

func (s *Service) ListCheckpoints(ctx context.Context, req *sessionv1.ListCheckpointsRequest) (*sessionv1.ListCheckpointsResponse, error) {
	list, err := s.engine.ListCheckpoints(ctx, s.principal(ctx), req.SessionId)
	if err != nil {
		return nil, err
	}
	return &sessionv1.ListCheckpointsResponse{Checkpoints: list}, nil
}

func (s *Service) RollbackCheckpoint(ctx context.Context, req *sessionv1.RollbackCheckpointRequest) (*sessionv1.RollbackCheckpointResponse, error) {
	return s.engine.RollbackCheckpoint(ctx, s.principal(ctx), req)
}

func (s *Service) GetDiff(ctx context.Context, req *sessionv1.GetDiffRequest) (*sessionv1.GetDiffResponse, error) {
	return s.engine.GetDiff(ctx, s.principal(ctx), req)
}

func (s *Service) ForkSession(ctx context.Context, req *sessionv1.ForkSessionRequest) (*sessionv1.ForkSessionResponse, error) {
	return s.engine.ForkSession(ctx, s.principal(ctx), req)
}

func (s *Service) DeleteSession(ctx context.Context, req *sessionv1.DeleteSessionRequest) (*sessionv1.DeleteSessionResponse, error) {
	return &sessionv1.DeleteSessionResponse{}, s.engine.DeleteSession(ctx, s.principal(ctx), req.SessionId)
}

func (s *Service) UploadBlob(ctx context.Context, req *sessionv1.UploadBlobRequest) (*sessionv1.UploadBlobResponse, error) {
	if s.principal != nil && s.principal(ctx) == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	if len(req.Data) > 64<<20 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("attachment exceeds 64 MiB limit"))
	}
	ref, err := s.engine.Store().PutBlob(req.Name, req.MimeType, req.Data)
	if err != nil {
		return nil, err
	}
	return &sessionv1.UploadBlobResponse{Attachment: ref}, nil
}

func (s *Service) DownloadBlob(ctx context.Context, req *sessionv1.DownloadBlobRequest) (*sessionv1.DownloadBlobResponse, error) {
	if s.principal != nil && s.principal(ctx) == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	data, name, mime, err := s.engine.Store().GetBlob(req.BlobSha256)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("blob not found"))
	}
	if err != nil {
		return nil, err
	}
	return &sessionv1.DownloadBlobResponse{Data: data, Name: name, MimeType: mime, SizeBytes: uint64(len(data))}, nil
}

func (s *Service) ListOutbox(context.Context, *sessionv1.ListOutboxRequest) (*sessionv1.ListOutboxResponse, error) {
	return &sessionv1.ListOutboxResponse{}, nil
}
