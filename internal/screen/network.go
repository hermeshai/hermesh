package screen

import (
	"net"

	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/stdnet"
)

// systemNet enumerates interfaces for each gather. stdnet otherwise keeps
// the addresses it saw when the ICE agent was first created, even on restart.
// Each enumeration owns its snapshot: ongoing gathers never race a refresh.
type systemNet struct {
	*stdnet.Net
}

func newSystemNet() (*systemNet, error) {
	n, err := stdnet.NewNet()
	if err != nil {
		return nil, err
	}
	return &systemNet{Net: n}, nil
}

func (*systemNet) Interfaces() ([]*transport.Interface, error) {
	n, err := stdnet.NewNet()
	if err != nil {
		return nil, err
	}
	return n.Interfaces()
}

func (n *systemNet) ListenUDP(network string, addr *net.UDPAddr) (transport.UDPConn, error) {
	c, err := net.ListenUDP(network, addr)
	if err != nil {
		return nil, err
	}
	if err := bindUDPInterface(c, addr); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}
