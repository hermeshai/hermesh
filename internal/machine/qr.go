package machine

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
	"rsc.io/qr"
)

// QRCode draws text as a QR code for a terminal: two modules per line with
// half blocks, in explicit black on white so it scans on any theme, and the
// lines of side beside it.
func QRCode(text string, side []string) string {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return ""
	}
	const quiet = 2
	n := c.Size + 2*quiet
	dark := func(x, y int) bool { return c.Black(x-quiet, y-quiet) }
	var b strings.Builder
	rows := (n + 1) / 2
	first := (rows - len(side)) / 2
	for r := 0; r < rows; r++ {
		b.WriteString("  \x1b[30;47m")
		for x := 0; x < n; x++ {
			top, bottom := dark(x, 2*r), 2*r+1 < n && dark(x, 2*r+1)
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString("\x1b[0m")
		if i := r - first; i >= 0 && i < len(side) {
			b.WriteString("   " + side[i])
		}
		b.WriteString("\n")
	}
	return b.String()
}

// IsTerminal reports whether w is a terminal, where a QR code can be drawn.
func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// TerminalDigits reads a line the person types, from stdin when it is a
// terminal, else from /dev/tty when out is one (under `curl … | sh`, stdin
// is the script); nil when nobody is at a terminal.
func TerminalDigits(out io.Writer) func(prompt string) (string, error) {
	in := os.Stdin
	if !term.IsTerminal(int(in.Fd())) {
		if !IsTerminal(out) {
			return nil
		}
		tty, err := os.Open("/dev/tty")
		if err != nil {
			return nil
		}
		in = tty
	}
	r := bufio.NewReader(in)
	return func(prompt string) (string, error) {
		fmt.Fprint(out, prompt)
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		return strings.TrimSpace(line), nil
	}
}
