package e2e

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/session/v1/sessionv1connect"
)

// fakeOutput is what the scripted agent's "output n" emits.
func fakeOutput(n int) string {
	var b strings.Builder
	for i := 1; b.Len() < n-4; i++ {
		fmt.Fprintf(&b, "line %07d of the output\n", i)
	}
	return b.String()[:max(n-4, 0)] + "END\n"
}

// TestToolOutputs exercises unified tool-output storage and retrieval across
// local and remote machines: GetSession inline-truncates outputs > 16 KiB while
// recording their content-addressed blob digest, DownloadBlob retrieves the full
// output on demand (in milliseconds on this Mac), BASIC session lists stay
// compact without reply bodies, and deleting a session cleans up its blobs.
func TestToolOutputs(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	w, vpsID, _ := farOwnerWithVPS(t, 0)
	ctx := context.Background()
	if _, err := w.app.client.AddProject(ctx, &localv1.AddProjectRequest{MachineId: vpsID, ProjectId: "demo", Path: gitRepo(t, map[string]string{"README.md": "# vps\n"})}); err != nil {
		t.Fatal(err)
	}
	blobBySession := map[string]string{}
	for _, project := range []string{"owner-mac/demo", "vps-1/demo"} {
		s, r := w.startSession(time.Minute, "--project", project, "--prompt", "output 65536\noutput 3145728\noutput 58\nsay done", "--wait")
		if s.Status != "idle" {
			t.Fatalf("%s: %+v", project, r)
		}
		getRes, err := w.app.sessions.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: s.SessionID})
		if err != nil {
			t.Fatalf("%s GetSession: %v", project, err)
		}
		proj := getRes.GetProjection()
		machineID := proj.GetSession().GetMachineId()
		var tools []*sessionv1.TurnItem
		for _, it := range proj.GetItems() {
			if it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_TOOL_CALL || it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_COMMAND_EXECUTION {
				tools = append(tools, it)
			}
		}
		if len(tools) != 3 {
			t.Fatalf("%s: %d tool items, want 3", project, len(tools))
		}
		blobBySession[s.SessionID] = tools[0].OutputBlobSha256

		for i, want := range []string{fakeOutput(65536), fakeOutput(3 << 20), fakeOutput(58)} {
			it := tools[i]
			if i < 2 {
				if !it.OutputTruncated || it.OutputBlobSha256 == "" || it.OutputSize < 65536 || len(it.Output) >= 32<<10 {
					t.Errorf("%s: output %d: truncated=%v blob=%q size=%d inline=%d", project, i, it.OutputTruncated, it.OutputBlobSha256, it.OutputSize, len(it.Output))
				}
				began := time.Now()
				dl, err := w.app.sessions.DownloadBlob(ctx, &sessionv1.DownloadBlobRequest{MachineId: machineID, BlobSha256: it.OutputBlobSha256})
				if err != nil {
					t.Fatalf("%s: DownloadBlob %d: %v", project, i, err)
				}
				first := time.Since(began)
				whole := string(dl.Data)
				switch i {
				case 1: // 3 MiB: capped to first and last 512 KiB
					if !strings.HasPrefix(whole, want[:512<<10-64]) || !strings.HasSuffix(whole, want[len(want)-(512<<10-64):]) || !strings.Contains(whole, "bytes left out") || len(whole) > 1<<20+64 {
						t.Errorf("%s: the 3 MiB output read as %d bytes", project, len(whole))
					}
				default:
					if whole != want {
						t.Errorf("%s: output %d read as %d bytes, want %d", project, i, len(whole), len(want))
					}
				}
				if dl.SizeBytes != uint64(len(dl.Data)) || it.OutputSize != uint64(len(dl.Data)) {
					t.Errorf("%s: output %d SizeBytes=%d OutputSize=%d, len=%d", project, i, dl.SizeBytes, it.OutputSize, len(dl.Data))
				}
				if project == "owner-mac/demo" {
					for range 2 {
						first = min(first, downloadBlobTime(t, w.app.sessions, machineID, it.OutputBlobSha256))
					}
					t.Logf("%s: output %d (%d bytes): read in %v", project, i, len(whole), first.Round(time.Microsecond))
					if first > 50*time.Millisecond {
						t.Errorf("reading blob on this Mac took %v, want at most 50ms", first)
					}
				}
			} else {
				if it.OutputTruncated || it.OutputBlobSha256 != "" || it.Output != want {
					t.Errorf("%s: short output: truncated=%v blob=%q output=%q", project, it.OutputTruncated, it.OutputBlobSha256, it.Output)
				}
			}
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
	if sizes[0] > 650 {
		t.Errorf("a BASIC session is %d bytes, want at most 650", sizes[0])
	}
	if full, err := w.app.client.ListSessions(ctx, &localv1.ListSessionsRequest{Limit: 50}); err != nil || full.Sessions[0].Result == "" {
		t.Errorf("an unspecified view leaves out the reply: %v", err)
	}

	// Deleted, its outputs go too.
	target := list.Sessions[0]
	sha := blobBySession[target.SessionId]
	if _, err := w.app.sessions.DeleteSession(ctx, &sessionv1.DeleteSessionRequest{SessionId: target.SessionId}); err != nil {
		t.Fatal(err)
	}
	_, err = w.app.sessions.DownloadBlob(ctx, &sessionv1.DownloadBlobRequest{MachineId: target.MachineId, BlobSha256: sha})
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("an output blob of a deleted session: %v (sha=%s)", err, sha)
	}
}

func downloadBlobTime(t *testing.T, sc sessionv1connect.SessionServiceClient, machineID, sha string) time.Duration {
	t.Helper()
	began := time.Now()
	if _, err := sc.DownloadBlob(context.Background(), &sessionv1.DownloadBlobRequest{MachineId: machineID, BlobSha256: sha}); err != nil {
		t.Fatal(err)
	}
	return time.Since(began)
}
