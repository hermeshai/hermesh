package owner

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// Her (docs/her-server.md): LocalService's calls for Her on the owner's
// machines (her.go).

func (l *localService) SetHer(ctx context.Context, req *localv1.SetHerRequest) (*localv1.SetHerResponse, error) {
	err := l.o().SetHer(ctx, req.MachineId, req.Enabled)
	l.o().wrote(ctx, err, false, req.MachineId)
	if err != nil {
		return nil, err
	}
	return &localv1.SetHerResponse{}, nil
}

func (l *localService) SetHerAssistantMode(ctx context.Context, req *localv1.SetHerAssistantModeRequest) (*localv1.SetHerAssistantModeResponse, error) {
	if err := l.o().SetHerAssistantMode(ctx, req.MachineId, req.BotId, req.PermissionMode); err != nil {
		return nil, err
	}
	return &localv1.SetHerAssistantModeResponse{}, nil
}

func (l *localService) SetHerCommandConnector(ctx context.Context, req *localv1.SetHerCommandConnectorRequest) (*localv1.SetHerCommandConnectorResponse, error) {
	if err := l.o().SetHerCommandConnector(ctx, req.MachineId, req.Connector); err != nil {
		return nil, err
	}
	return &localv1.SetHerCommandConnectorResponse{}, nil
}

func (l *localService) HerOpen(ctx context.Context, req *localv1.HerOpenRequest, stream *connect.ServerStream[herv1.OpenEvent]) error {
	return l.o().HerOpen(ctx, req.MachineId, req.Open, stream.Send)
}

func (l *localService) HerCall(ctx context.Context, req *localv1.HerCallRequest) (*herv1.CallResponse, error) {
	return l.o().HerCall(ctx, req.MachineId, req.Call)
}

func (l *localService) HerSend(ctx context.Context, req *localv1.HerSendRequest) (*localv1.HerSendResponse, error) {
	return l.o().HerSend(ctx, req)
}

func (l *localService) ListHerWaiting(ctx context.Context, req *localv1.ListHerWaitingRequest) (*localv1.ListHerWaitingResponse, error) {
	list, err := l.o().st.herWaiting(req.MachineId, req.BotId)
	if err != nil {
		return nil, err
	}
	return &localv1.ListHerWaitingResponse{Waiting: list}, nil
}

func (l *localService) DeleteHerWaiting(ctx context.Context, req *localv1.DeleteHerWaitingRequest) (*localv1.DeleteHerWaitingResponse, error) {
	if err := l.o().DeleteHerWaiting(req.MachineId, req.Id); err != nil {
		return nil, err
	}
	return &localv1.DeleteHerWaitingResponse{}, nil
}

func (l *localService) GetHerConversation(ctx context.Context, req *localv1.GetHerConversationRequest) (*localv1.GetHerConversationResponse, error) {
	if req.MachineId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("no machine"))
	}
	bot, snapshot, err := l.o().st.herKept(req.MachineId)
	if err != nil {
		return nil, err
	}
	return &localv1.GetHerConversationResponse{Bot: bot, Snapshot: snapshot}, nil
}

func (l *localService) HerUpload(ctx context.Context, req *localv1.HerUploadRequest) (*localv1.HerUploadResponse, error) {
	path, err := l.o().HerUpload(ctx, req.MachineId, req.BotId, req.LocalPath)
	if err != nil {
		return nil, err
	}
	return &localv1.HerUploadResponse{Path: path}, nil
}

func (l *localService) HerDownload(ctx context.Context, req *localv1.HerDownloadRequest) (*localv1.HerDownloadResponse, error) {
	path, err := l.o().HerDownload(ctx, req)
	if err != nil {
		return nil, err
	}
	return &localv1.HerDownloadResponse{LocalPath: path}, nil
}

func (l *localService) GetHerConsole(ctx context.Context, req *localv1.GetHerConsoleRequest) (*localv1.GetHerConsoleResponse, error) {
	url, err := l.o().GetHerConsole(req.MachineId)
	if err != nil {
		return nil, err
	}
	return &localv1.GetHerConsoleResponse{Url: url}, nil
}
