//go:build darwin || ios

package screen

import (
	"bytes"
	"net"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Exercise the OS socket, not only the address-to-interface lookup. A host
// candidate must keep its interface and still exchange real datagrams.
func TestHostUDPUsesItsInterface(t *testing.T) {
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, network := range []string{"udp4", "udp6"} {
		t.Run(network, func(t *testing.T) {
			for _, iface := range ifaces {
				if iface.Flags&(net.FlagUp|net.FlagBroadcast) != net.FlagUp|net.FlagBroadcast {
					continue
				}
				addrs, _ := iface.Addrs()
				for _, a := range addrs {
					ip, ok := a.(*net.IPNet)
					if !ok || !ip.IP.IsGlobalUnicast() || (ip.IP.To4() != nil) != (network == "udp4") {
						continue
					}
					testHostUDP(t, network, ip.IP, iface.Index)
					return
				}
			}
			t.Skip("no active broadcast interface with this address family")
		})
	}
}

func testHostUDP(t *testing.T, network string, ip net.IP, index int) {
	t.Helper()
	n, err := newSystemNet()
	if err != nil {
		t.Fatal(err)
	}
	c, err := n.ListenUDP(network, &net.UDPAddr{IP: ip})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	raw, err := c.(*net.UDPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var bound int
	var sockerr error
	err = raw.Control(func(fd uintptr) {
		if network == "udp4" {
			bound, sockerr = unix.GetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF)
		} else {
			bound, sockerr = unix.GetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_BOUND_IF)
		}
	})
	if err != nil || sockerr != nil || bound != index {
		t.Fatalf("interface=%d, want %d; control=%v, socket=%v", bound, index, err, sockerr)
	}
	peer, err := net.ListenUDP(network, &net.UDPAddr{IP: ip})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	c.SetDeadline(time.Now().Add(time.Second))
	peer.SetDeadline(time.Now().Add(time.Second))
	data := []byte("host connectivity check")
	if _, err := c.WriteTo(data, peer.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 128)
	count, from, err := peer.ReadFrom(buf)
	if err != nil || !bytes.Equal(buf[:count], data) {
		t.Fatalf("receiving the check: n=%d, err=%v", count, err)
	}
	if _, err := peer.WriteTo(data, from); err != nil {
		t.Fatal(err)
	}
	count, _, err = c.ReadFrom(buf)
	if err != nil || !bytes.Equal(buf[:count], data) {
		t.Fatalf("receiving the response: n=%d, err=%v", count, err)
	}
}
