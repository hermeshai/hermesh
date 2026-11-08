package machine

import (
	"context"
	"errors"
	"io"
	"net/http"

	"connectrpc.com/connect"

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
)

// Her (docs/her-server.md): the in-process side of hermesh.her.v1.HerService,
// for the owner role when the machine is this Mac. Each does what the
// service does over the tunnel. Not built yet: each answers Unimplemented.

var errHerNotBuilt = connect.NewError(connect.CodeUnimplemented, errors.New("Her is not built yet"))

// HerOpen is HerService.Open: send gets the `opened` event, then her's
// notifications, until ctx ends or the connection fails.
func (l *Local) HerOpen(ctx context.Context, req *herv1.OpenRequest, send func(*herv1.OpenEvent) error) error {
	return errHerNotBuilt
}

// HerCall is HerService.Call.
func (l *Local) HerCall(ctx context.Context, req *herv1.CallRequest) (*herv1.CallResponse, error) {
	return nil, errHerNotBuilt
}

// HerUpload is HerService.Upload: start, then the file's bytes from r.
func (l *Local) HerUpload(ctx context.Context, start *herv1.UploadStart, r io.Reader) (*herv1.UploadResponse, error) {
	return nil, errHerNotBuilt
}

// HerDownload is HerService.Download: send gets `start`, then the bytes.
func (l *Local) HerDownload(ctx context.Context, req *herv1.DownloadRequest, send func(*herv1.DownloadResponse) error) error {
	return errHerNotBuilt
}

// HerConsole serves the console's HTTP routes (paths under /her/console/),
// as the tunnel does, to this Mac's owner role.
func (l *Local) HerConsole() http.Handler {
	return http.NotFoundHandler()
}
