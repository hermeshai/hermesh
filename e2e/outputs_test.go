package e2e

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/local/v1/localv1connect"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// fakeOutput is what the fake agent's "output n" prints.
func fakeOutput(n int) string {
	var b strings.Builder
	for i := 1; b.Len() < n-4; i++ {
		fmt.Fprintf(&b, "line %07d of the output\n", i)
	}
	return b.String()[:max(n-4, 0)] + "END\n"
}

// TestToolOutputs (docs/state-sync.md, step 3): on this Mac and on another
// machine, a conversation opened as the apps open it (BASIC) carries a
// tool's output as a three-line preview with its size, and the row opened
// reads it whole (ReadToolOutput): 64 KiB as printed, 3 MiB as its first
// and last 512 KiB; the first bytes of one on this Mac come in milliseconds.
// Callers from before views (the CLI) see what they saw: the first 16 KiB.
// Lists in the BASIC view leave out what they do not show. Deleting the
// session takes its outputs with it.
func TestToolOutputs(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	w, vpsID, _ := farOwnerWithVPS(t, 0)
	ctx := context.Background()
	if _, err := w.app.client.AddProject(ctx, &localv1.AddProjectRequest{MachineId: vpsID, ProjectId: "demo", Path: gitRepo(t, map[string]string{"README.md": "# vps\n"})}); err != nil {
		t.Fatal(err)
	}
	for _, project := range []string{"owner-mac/demo", "vps-1/demo"} {
		s, r := w.startSession(time.Minute, "--project", project, "--prompt", "output 65536\noutput 3145728\noutput 58\nsay done", "--wait")
		if s.Status != "idle" {
			t.Fatalf("%s: %+v", project, r)
		}
		events := func(view sessionv1.EventView) []*sessionv1.ToolResult {
			stream, err := w.app.client.StreamSessionEvents(ctx, &localv1.StreamSessionEventsRequest{SessionId: s.SessionID, View: view})
			if err != nil {
				t.Fatal(err)
			}
			var out []*sessionv1.ToolResult
			for stream.Receive() {
				if r := stream.Msg().GetToolResult(); r != nil {
					out = append(out, r)
				}
			}
			if err := stream.Err(); err != nil {
				t.Fatalf("%s: %v", project, err)
			}
			return out
		}
		basic := events(sessionv1.EventView_EVENT_VIEW_BASIC)
		if len(basic) != 3 {
			t.Fatalf("%s: %d tool results", project, len(basic))
		}
		for i, want := range []string{fakeOutput(65536), fakeOutput(3 << 20), fakeOutput(58)} {
			r := basic[i]
			if i < 2 && (!r.Preview || r.Summary != strings.Join(strings.SplitN(want, "\n", 4)[:3], "\n") || r.Size < 65536) {
				t.Errorf("%s: output %d: preview %v, %d bytes of %d: %q", project, i, r.Preview, len(r.Summary), r.Size, r.Summary)
			}
			if i == 2 && (r.Preview || r.Summary != want) {
				t.Errorf("%s: the short output: %+v", project, r)
			}
			began := time.Now()
			stream, err := w.app.client.ReadToolOutput(ctx, &localv1.ReadToolOutputRequest{SessionId: s.SessionID, CallId: r.CallId, Seq: 0})
			if err != nil {
				t.Fatal(err)
			}
			var got bytes.Buffer
			var size uint64
			var first time.Duration
			for stream.Receive() {
				if got.Len() == 0 {
					first, size = time.Since(began), stream.Msg().Size
				}
				got.Write(stream.Msg().Data)
			}
			if err := stream.Err(); err != nil {
				t.Fatalf("%s: read output %d: %v", project, i, err)
			}
			whole := got.String()
			switch i {
			case 1: // 3 MiB: its first and last 512 KiB
				if !strings.HasPrefix(whole, want[:512<<10-64]) || !strings.HasSuffix(whole, want[len(want)-(512<<10-64):]) || !strings.Contains(whole, "bytes left out") || len(whole) > 1<<20+64 {
					t.Errorf("%s: the 3 MiB output read as %d bytes", project, len(whole))
				}
			default:
				if whole != want {
					t.Errorf("%s: output %d read as %d bytes, want %d", project, i, len(whole), len(want))
				}
			}
			if size != uint64(len(whole)) {
				t.Errorf("%s: output %d: first chunk says %d bytes, read %d", project, i, size, len(whole))
			}
			if project == "owner-mac/demo" {
				// Of the best of three reads: a busy machine (CI) stalls one
				// now and then, while reading it whole first, or through the
				// relay, makes each slow.
				for range 2 {
					first = min(first, firstBytes(t, w.app.client, s.SessionID, r.CallId))
				}
			}
			t.Logf("%s: output %d (%d bytes): first bytes after %v", project, i, len(whole), first.Round(time.Microsecond))
			if project == "owner-mac/demo" && first > 50*time.Millisecond {
				t.Errorf("first bytes of an output on this Mac after %v, want at most 50ms", first)
			}
		}
		// What callers from before views see.
		full := events(sessionv1.EventView_EVENT_VIEW_UNSPECIFIED)
		if r := full[0]; r.Preview || !r.Truncated || len(r.Summary) != 16<<10 || r.Size != 65536 {
			t.Errorf("%s: unspecified view: %d bytes, truncated %v, size %d", project, len(r.Summary), r.Truncated, r.Size)
		}
		if out := w.cli(time.Minute, "session", "events", s.SessionID, "-o", "json"); out.code != 0 || !strings.Contains(out.stdout, "line 0000001") {
			t.Errorf("%s: the CLI's events: %+v", project, out)
		}
	}

	// A BASIC list leaves out the replies, which no list shows.
	list, err := w.app.client.ListSessions(ctx, &localv1.ListSessionsRequest{Limit: 50, View: sessionv1.SessionView_SESSION_VIEW_BASIC})
	if err != nil || len(list.Sessions) != 2 {
		t.Fatalf("list: %v %v", list, err)
	}
	var sizes []int
	for _, s := range list.Sessions {
		if s.Result != "" || s.GetProgress().GetLastMessageExcerpt() != "" {
			t.Errorf("a BASIC session carries its reply: %q %q", s.Result, s.GetProgress().GetLastMessageExcerpt())
		}
		sizes = append(sizes, proto.Size(s))
	}
	slices.Sort(sizes)
	t.Logf("BASIC sessions: %v bytes", sizes)
	if sizes[0] > 400 {
		t.Errorf("a BASIC session is %d bytes, want at most 400", sizes[0])
	}
	if full, err := w.app.client.ListSessions(ctx, &localv1.ListSessionsRequest{Limit: 50}); err != nil || full.Sessions[0].Result == "" {
		t.Errorf("an unspecified view leaves out the reply: %v", err)
	}

	// Deleted, its outputs go too.
	id := list.Sessions[0].SessionId
	if _, err := w.app.client.DeleteSession(ctx, &localv1.DeleteSessionRequest{SessionId: id}); err != nil {
		t.Fatal(err)
	}
	stream, err := w.app.client.ReadToolOutput(ctx, &localv1.ReadToolOutputRequest{SessionId: id, CallId: "call-b"})
	if err == nil {
		for stream.Receive() {
		}
		err = stream.Err()
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("an output of a deleted session: %v", err)
	}
}

// firstBytes reads a tool output until its first bytes come, and says when.
func firstBytes(t *testing.T, client localv1connect.LocalServiceClient, session, call string) time.Duration {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	began := time.Now()
	stream, err := client.ReadToolOutput(ctx, &localv1.ReadToolOutputRequest{SessionId: session, CallId: call, Seq: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if !stream.Receive() {
		t.Fatalf("read %s: %v", call, stream.Err())
	}
	return time.Since(began)
}
