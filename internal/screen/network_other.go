//go:build !darwin && !ios

package screen

import "net"

func bindUDPInterface(*net.UDPConn, *net.UDPAddr) error { return nil }
