package localipc

import (
	"context"
	"errors"
	"fmt"
	"net"
)

// The endpoints become named pipes in phase W1 (docs/windows.md §4.2);
// until then they are not supported.

func Listen(path string) (net.Listener, error) { return nil, unsupported(path) }

func Dial(ctx context.Context, path string) (net.Conn, error) { return nil, unsupported(path) }

func Peer(c net.Conn) (p Process, ok bool) { return Process{}, false }

func unsupported(path string) error {
	return fmt.Errorf("local endpoint %s: %w on Windows yet", path, errors.ErrUnsupported)
}
