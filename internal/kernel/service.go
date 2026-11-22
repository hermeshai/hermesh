package kernel

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	threadv2 "github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2"
	"github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2/threadv2connect"
	"github.com/hermeshai/hermesh/internal/kernel/store"
)

// Service adapts Engine to threadv2connect.ThreadServiceHandler.
type Service struct {
	engine    *Engine
	principal func(context.Context) *Principal
}

var _ threadv2connect.ThreadServiceHandler = (*Service)(nil)

func NewService(engine *Engine, principal func(context.Context) *Principal) *Service {
	return &Service{engine: engine, principal: principal}
}

func (s *Service) ListProviders(ctx context.Context, req *threadv2.ListProvidersRequest) (*threadv2.ListProvidersResponse, error) {
	return s.engine.ListProviders(ctx, s.principal(ctx), req.ProjectId, req.Refresh)
}

func (s *Service) UpsertProviderInstance(ctx context.Context, req *threadv2.UpsertProviderInstanceRequest) (*threadv2.UpsertProviderInstanceResponse, error) {
	inst, err := s.engine.UpsertProviderInstance(ctx, s.principal(ctx), req.Config)
	if err != nil {
		return nil, err
	}
	return &threadv2.UpsertProviderInstanceResponse{Provider: inst}, nil
}

func (s *Service) RemoveProviderInstance(ctx context.Context, req *threadv2.RemoveProviderInstanceRequest) (*threadv2.RemoveProviderInstanceResponse, error) {
	return &threadv2.RemoveProviderInstanceResponse{}, s.engine.RemoveProviderInstance(ctx, s.principal(ctx), req.InstanceId)
}

func (s *Service) LaunchThread(ctx context.Context, req *threadv2.LaunchThreadRequest) (*threadv2.LaunchThreadResponse, error) {
	return s.engine.LaunchThread(ctx, s.principal(ctx), req)
}

func (s *Service) GetThread(ctx context.Context, req *threadv2.GetThreadRequest) (*threadv2.GetThreadResponse, error) {
	proj, err := s.engine.GetThread(ctx, s.principal(ctx), req.ThreadId)
	if err != nil {
		return nil, err
	}
	return &threadv2.GetThreadResponse{Projection: proj}, nil
}

func (s *Service) ListThreads(ctx context.Context, req *threadv2.ListThreadsRequest) (*threadv2.ListThreadsResponse, error) {
	list, err := s.engine.ListThreads(ctx, s.principal(ctx), req.ProjectId, req.IncludeArchived, req.Limit)
	if err != nil {
		return nil, err
	}
	return &threadv2.ListThreadsResponse{Threads: list}, nil
}

func (s *Service) SubscribeThreads(ctx context.Context, req *threadv2.SubscribeThreadsRequest, stream *connect.ServerStream[threadv2.SubscribeThreadsResponse]) error {
	return s.engine.SubscribeThreads(ctx, s.principal(ctx), req.IncludeArchived, stream.Send)
}

func (s *Service) SubscribeThread(ctx context.Context, req *threadv2.SubscribeThreadRequest, stream *connect.ServerStream[threadv2.ThreadEvent]) error {
	return s.engine.SubscribeThread(ctx, s.principal(ctx), req.ThreadId, req.AfterSeq, stream.Send)
}

func (s *Service) DispatchMessage(ctx context.Context, req *threadv2.DispatchMessageRequest) (*threadv2.DispatchMessageResponse, error) {
	return s.engine.DispatchMessage(ctx, s.principal(ctx), req)
}

func (s *Service) InterruptRun(ctx context.Context, req *threadv2.InterruptRunRequest) (*threadv2.InterruptRunResponse, error) {
	return s.engine.InterruptRun(ctx, s.principal(ctx), req)
}

func (s *Service) ManageQueue(ctx context.Context, req *threadv2.ManageQueueRequest) (*threadv2.ManageQueueResponse, error) {
	return s.engine.ManageQueue(ctx, s.principal(ctx), req)
}

func (s *Service) UpdateThreadConfig(ctx context.Context, req *threadv2.UpdateThreadConfigRequest) (*threadv2.UpdateThreadConfigResponse, error) {
	return s.engine.UpdateThreadConfig(ctx, s.principal(ctx), req)
}

func (s *Service) RespondRuntimeRequest(ctx context.Context, req *threadv2.RespondRuntimeRequestRequest) (*threadv2.RespondRuntimeRequestResponse, error) {
	return s.engine.RespondRuntimeRequest(ctx, s.principal(ctx), req)
}

func (s *Service) ListCheckpoints(ctx context.Context, req *threadv2.ListCheckpointsRequest) (*threadv2.ListCheckpointsResponse, error) {
	list, err := s.engine.ListCheckpoints(ctx, s.principal(ctx), req.ThreadId)
	if err != nil {
		return nil, err
	}
	return &threadv2.ListCheckpointsResponse{Checkpoints: list}, nil
}

func (s *Service) RollbackCheckpoint(ctx context.Context, req *threadv2.RollbackCheckpointRequest) (*threadv2.RollbackCheckpointResponse, error) {
	return s.engine.RollbackCheckpoint(ctx, s.principal(ctx), req)
}

func (s *Service) GetThreadDiff(ctx context.Context, req *threadv2.GetThreadDiffRequest) (*threadv2.GetThreadDiffResponse, error) {
	return s.engine.GetThreadDiff(ctx, s.principal(ctx), req.ThreadId, req.CheckpointId)
}

func (s *Service) ForkThread(ctx context.Context, req *threadv2.ForkThreadRequest) (*threadv2.ForkThreadResponse, error) {
	return s.engine.ForkThread(ctx, s.principal(ctx), req)
}

func (s *Service) DeleteThread(ctx context.Context, req *threadv2.DeleteThreadRequest) (*threadv2.DeleteThreadResponse, error) {
	return &threadv2.DeleteThreadResponse{}, s.engine.DeleteThread(ctx, s.principal(ctx), req.ThreadId)
}

func (s *Service) UploadBlob(ctx context.Context, req *threadv2.UploadBlobRequest) (*threadv2.UploadBlobResponse, error) {
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
	return &threadv2.UploadBlobResponse{Attachment: ref}, nil
}

func (s *Service) DownloadBlob(ctx context.Context, req *threadv2.DownloadBlobRequest) (*threadv2.DownloadBlobResponse, error) {
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
	return &threadv2.DownloadBlobResponse{Data: data, Name: name, MimeType: mime}, nil
}

func (s *Service) ListOutbox(context.Context, *threadv2.ListOutboxRequest) (*threadv2.ListOutboxResponse, error) {
	// Machine itself executes directly; Owner overrides ListOutbox with its local Outbox entries.
	return &threadv2.ListOutboxResponse{}, nil
}
