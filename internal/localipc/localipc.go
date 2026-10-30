// Package localipc is how Hermesh's processes on one computer reach each
// other: endpoints that only this user can open (Unix sockets; on Windows,
// named pipes: docs/windows.md §4.2), and which process is at the other
// end.
package localipc

// Process is the local process at the other end of a connection, as far as
// the OS tells: a hint for the person (which program asked), not proof of
// anything.
type Process struct {
	PID        int
	Executable string
}
