package service

import (
	"strings"
	"testing"
)

// The task runs the windowless daemon with its arguments quoted for the
// command line, and its file reads back as written.
func TestTaskXML(t *testing.T) {
	x := taskXML(`C:\Users\A B\AppData\Local\Programs\Hermesh\bin\hermeshdw.exe`, []string{"run", "--supervise", "--data-dir", `C:\Users\A B\AppData\Local\Hermesh`}, "S-1-5-21-1")
	for _, want := range []string{
		`<Command>C:\Users\A B\AppData\Local\Programs\Hermesh\bin\hermeshdw.exe</Command>`,
		`<Arguments>run --supervise --data-dir "C:\Users\A B\AppData\Local\Hermesh"</Arguments>`,
		`<UserId>S-1-5-21-1</UserId>`,
		`<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>`,
	} {
		if !strings.Contains(x, want) {
			t.Errorf("no %s in\n%s", want, x)
		}
	}
	if got := fromUTF16(utf16File(x)); got != x {
		t.Fatalf("read back as %q", got)
	}
}
