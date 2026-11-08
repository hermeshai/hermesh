package owner

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// Her (docs/her-server.md): LocalService's calls for Her on the owner's
// machines. Not built yet: each answers Unimplemented.

var errHerNotBuilt = connect.NewError(connect.CodeUnimplemented, errors.New("Her is not built yet"))

func (l *localService) SetHer(ctx context.Context, req *localv1.SetHerRequest) (*localv1.SetHerResponse, error) {
	return nil, errHerNotBuilt
}

func (l *localService) SetHerAssistantMode(ctx context.Context, req *localv1.SetHerAssistantModeRequest) (*localv1.SetHerAssistantModeResponse, error) {
	return nil, errHerNotBuilt
}

func (l *localService) SetHerCommandConnector(ctx context.Context, req *localv1.SetHerCommandConnectorRequest) (*localv1.SetHerCommandConnectorResponse, error) {
	return nil, errHerNotBuilt
}

func (l *localService) HerOpen(ctx context.Context, req *localv1.HerOpenRequest, stream *connect.ServerStream[herv1.OpenEvent]) error {
	return errHerNotBuilt
}

func (l *localService) HerCall(ctx context.Context, req *localv1.HerCallRequest) (*herv1.CallResponse, error) {
	return nil, errHerNotBuilt
}

func (l *localService) HerSend(ctx context.Context, req *localv1.HerSendRequest) (*localv1.HerSendResponse, error) {
	return nil, errHerNotBuilt
}

func (l *localService) ListHerWaiting(ctx context.Context, req *localv1.ListHerWaitingRequest) (*localv1.ListHerWaitingResponse, error) {
	return nil, errHerNotBuilt
}

func (l *localService) HerUpload(ctx context.Context, req *localv1.HerUploadRequest) (*localv1.HerUploadResponse, error) {
	return nil, errHerNotBuilt
}

func (l *localService) HerDownload(ctx context.Context, req *localv1.HerDownloadRequest) (*localv1.HerDownloadResponse, error) {
	return nil, errHerNotBuilt
}

func (l *localService) GetHerConsole(ctx context.Context, req *localv1.GetHerConsoleRequest) (*localv1.GetHerConsoleResponse, error) {
	return nil, errHerNotBuilt
}
