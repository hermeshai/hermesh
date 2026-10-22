package transport

import "connectrpc.com/connect"

// RPC traffic between peers is either end-to-end TLS or local; gzip only
// burns CPU, so it is disabled on both sides.
var (
	NoCompression       = connect.WithCompression("gzip", nil, nil)
	ClientNoCompression = connect.WithAcceptCompression("gzip", nil, nil)
)
