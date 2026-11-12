//go:build darwin || ios

package screen

import (
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// A host candidate belongs to a network, not just an IP. Pin its socket to
// that interface so a VPN's default route cannot move its connectivity checks
// onto another network. Wildcard sockets (STUN and TURN) keep system routing.
func bindUDPInterface(conn *net.UDPConn, addr *net.UDPAddr) error {
	if addr == nil || len(addr.IP) == 0 || addr.IP.IsUnspecified() || addr.IP.IsLoopback() {
		return nil
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ip, ok := a.(*net.IPNet)
			if !ok || !ip.IP.Equal(addr.IP) {
				continue
			}
			raw, err := conn.SyscallConn()
			if err != nil {
				return err
			}
			var sockerr error
			err = raw.Control(func(fd uintptr) {
				if addr.IP.To4() != nil {
					sockerr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, iface.Index)
				} else {
					sockerr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_BOUND_IF, iface.Index)
				}
			})
			if err != nil {
				return err
			}
			return sockerr
		}
	}
	return syscall.ENODEV // the address disappeared while gathering
}
