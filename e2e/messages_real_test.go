package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// Messages with the real Claude Code (HERMESH_REAL_CLI=1; it costs tokens).

func realAgents(t *testing.T) {
	t.Helper()
	if testing.Short() || os.Getenv("HERMESH_REAL_CLI") != "1" {
		t.Skip("real Claude Code only: HERMESH_REAL_CLI=1")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed")
	}
}

// TestRealSessionWrites: Claude Code in a Hermesh session writes to the
// agent that started it, with the message_send tool Hermesh gives it.
func TestRealSessionWrites(t *testing.T) {
	realAgents(t)
	w := newOwnerMac(t, []string{"PATH=" + os.Getenv("PATH")}, &localv1.GrantSpec{})
	s, r := w.startSession(4*time.Minute, "--agent", "claude", "--model", "haiku", "--wait", "--prompt",
		"Use the message_send tool (from the hermesh MCP server) to send the agent that started this session the message: pong. Then reply with just: done")
	if r.code != 0 {
		t.Fatalf("start: %+v", r)
	}
	var got messageDoc
	for _, m := range listMessages(t, w, w.cliEnv).Messages {
		if m.From.Session == s.SessionID && m.News == nil {
			got = m
		}
	}
	if !strings.Contains(strings.ToLower(got.Text), "pong") {
		t.Fatalf("the session's message: %+v (result %q)", listMessages(t, w, w.cliEnv), s.Result)
	}
}

// TestRealClaudeInbox: a Claude Code conversation on the user's computer,
// with Hermesh's hooks, gets a message addressed to it through its own
// inbox socket, and an idle conversation answers it in a new turn.
func TestRealClaudeInbox(t *testing.T) {
	realAgents(t)
	w := newOwnerMac(t, []string{"HERMESH_FAKE_PROVIDERS=1"}, &localv1.GrantSpec{})
	helper := w.addAgent("helper", &localv1.GrantSpec{})
	hermesh := filepath.Join(w.bin, "hermesh")
	hooks, _ := json.Marshal(map[string]any{"hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{
		map[string]any{"type": "command", "command": strconv.Quote(hermesh) + " message hook claude"}}}}}})
	cmd := exec.Command("claude", "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--model", "haiku",
		"--settings", string(hooks))
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), w.cliEnv...)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); cmd.Process.Kill(); cmd.Wait() })
	lines := make(chan map[string]any, 100)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for sc.Scan() {
			var v map[string]any
			if json.Unmarshal(sc.Bytes(), &v) == nil {
				lines <- v
			}
		}
		close(lines)
	}()
	say := func(text string) {
		b, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}})
		fmt.Fprintf(stdin, "%s\n", b)
	}
	// A result, and the conversation's id.
	result := func(timeout time.Duration) (string, string) {
		deadline := time.After(timeout)
		var id string
		for {
			select {
			case v, ok := <-lines:
				if !ok {
					t.Fatal("claude ended")
				}
				if sid, _ := v["session_id"].(string); sid != "" {
					id = sid
				}
				if v["type"] == "result" {
					r, _ := v["result"].(string)
					return r, id
				}
			case <-deadline:
				t.Fatal("no result in time")
			}
		}
	}
	say("Reply with just: ready")
	_, conv := result(3 * time.Minute)
	if conv == "" {
		t.Fatal("no session id")
	}
	eventually(t, 20*time.Second, "the watch running", func() bool {
		_, err := os.Stat(filepath.Join(w.tmp, "agent", "watch-"+conv+".pid"))
		return err == nil
	})
	if d, r := sendMessage(t, w, helper, 30*time.Second, "--to", contactAddress(t, w, helper, "bot")+"/"+conv,
		"--message", "Reply with just the word: received"); !d.OK {
		t.Fatalf("helper's message: %+v", r)
	}
	// Idle, the conversation takes it as a new turn.
	got, _ := result(3 * time.Minute)
	if !strings.Contains(strings.ToLower(got), "received") {
		t.Fatalf("the turn the message started: %q", got)
	}
}
