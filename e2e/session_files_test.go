package e2e

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

func makeE2EPNG(t *testing.T, w, h int, r, g, b uint8) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	c := color.NRGBA{R: r, G: g, B: b, A: 255}
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestSessionFiles reads a session's files as the apps do (docs/session-files.md §6),
// for a session on this Mac and one on another machine: the files a reply links,
// as each turn left them and as they are now, a folder's entries, a range of bytes;
// then, with the other machine stopped, what this device read before still reads.
func TestSessionFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	w, vpsID, restart := farOwnerWithVPS(t, 0)
	ctx := context.Background()
	repo := gitRepo(t, map[string]string{"README.md": "# vps\n", "main.go": "package main\n"})
	if _, err := w.app.client.AddProject(ctx, &localv1.AddProjectRequest{MachineId: vpsID, ProjectId: "demo", Path: repo}); err != nil {
		t.Fatal(err)
	}
	sc := w.app.sessions
	extShot := filepath.Join(t.TempDir(), "vps_simulator_shot.png")
	extPNG := makeE2EPNG(t, 24, 48, 40, 130, 245)
	if err := os.WriteFile(extShot, extPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	var far struct{ id, run1, run2, folder string }
	for _, project := range []string{"owner-mac/demo", "vps-1/demo"} {
		s, r := w.startSession(time.Minute, "--project", project, "--prompt", "write docs/demo.html|<p>one</p>\nwrite docs/demo.js|go()\nsay done", "--wait")
		if s.Status != "idle" {
			t.Fatalf("%s: %+v", project, r)
		}
		if s2, r := w.send(time.Minute, s.SessionID, "write docs/demo.html|<p>two</p>\nsay ![shot]("+extShot+")"); s2.Status != "idle" {
			t.Fatalf("%s turn 2: %+v", project, r)
		}
		got, err := sc.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: s.SessionID})
		if err != nil {
			t.Fatal(err)
		}
		runs := map[uint32]string{}
		for _, cp := range got.GetProjection().GetCheckpoints() {
			runs[cp.RunOrdinal] = cp.RunId
		}
		if runs[1] == "" || runs[2] == "" {
			t.Fatalf("%s: checkpoints %v", project, got.GetProjection().GetCheckpoints())
		}

		res, err := sc.ResolveFiles(ctx, &sessionv1.ResolveFilesRequest{SessionId: s.SessionID, RunId: runs[1], Paths: []string{"docs/demo.html", "docs", "nope.md", "/etc/hosts"}})
		if err != nil {
			t.Fatalf("%s resolve: %v", project, err)
		}
		var kinds []string
		for _, f := range res.Files {
			kinds = append(kinds, strings.TrimPrefix(f.Kind.String(), "FILE_REF_KIND_"))
		}
		if !slices.Equal(kinds, []string{"FILE", "FOLDER", "MISSING", "OUTSIDE"}) || res.Files[0].Turn != 1 || res.Folder == "" {
			t.Fatalf("%s resolve = %v %+v", project, kinds, res)
		}
		// The link a reply writes, as file:// with its folder.
		abs, err := sc.ResolveFiles(ctx, &sessionv1.ResolveFilesRequest{SessionId: s.SessionID, Paths: []string{"file://" + filepath.Join(res.Folder, "docs", "demo.js")}})
		if err != nil || abs.Files[0].Kind != sessionv1.FileRefKind_FILE_REF_KIND_FILE || abs.Files[0].Path != "docs/demo.js" {
			t.Fatalf("%s file://: %+v %v", project, abs, err)
		}

		one, err := sc.ReadFile(ctx, &sessionv1.ReadFileRequest{SessionId: s.SessionID, RunId: runs[1], Path: "docs/demo.html"})
		if err != nil || one.Text != "<p>one</p>" || one.Turn != 1 || one.Now != sessionv1.FileNow_FILE_NOW_CHANGED {
			t.Fatalf("%s turn 1: %+v %v", project, one, err)
		}
		now, err := sc.ReadFile(ctx, &sessionv1.ReadFileRequest{SessionId: s.SessionID, Path: "docs/demo.html"})
		if err != nil || now.Text != "<p>two</p>" || now.Turn != 0 {
			t.Fatalf("%s now: %+v %v", project, now, err)
		}
		part, err := sc.ReadFileBytes(ctx, &sessionv1.ReadFileBytesRequest{SessionId: s.SessionID, RunId: runs[1], Path: "docs/demo.html", Offset: 3, Length: 3})
		if err != nil || string(part.Data) != "one" || !strings.HasPrefix(part.MimeType, "text/html") {
			t.Fatalf("%s bytes: %+v %v", project, part, err)
		}
		list, err := sc.ListFiles(ctx, &sessionv1.ListFilesRequest{SessionId: s.SessionID})
		if err != nil {
			t.Fatalf("%s list: %v", project, err)
		}
		var names []string
		for _, e := range list.Entries {
			names = append(names, e.Name)
		}
		// Folders first; the two projects' files differ, both have a README.
		if len(names) < 2 || names[0] != "docs" || !slices.Contains(names, "README.md") || slices.Contains(names, ".git") {
			t.Fatalf("%s list = %v", project, names)
		}
		if _, err := sc.ReadFile(ctx, &sessionv1.ReadFileRequest{SessionId: s.SessionID, Path: "/etc/hosts"}); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("%s outside: %v", project, err)
		}
		if project == "vps-1/demo" {
			far.id, far.run1, far.run2, far.folder = s.SessionID, runs[1], runs[2], res.Folder
		}
	}

	// Remove the external screenshot from /tmp: turn 2's pinned blob still resolves and reads on vps-1.
	if err := os.Remove(extShot); err != nil {
		t.Fatal(err)
	}
	extRes, err := sc.ResolveFiles(ctx, &sessionv1.ResolveFilesRequest{SessionId: far.id, RunId: far.run2, Paths: []string{extShot}})
	if err != nil || extRes.Files[0].Kind != sessionv1.FileRefKind_FILE_REF_KIND_FILE || extRes.Files[0].Turn != 2 {
		t.Fatalf("vps-1 pinned external image resolve: %+v %v", extRes, err)
	}
	extRead, err := sc.ReadFile(ctx, &sessionv1.ReadFileRequest{SessionId: far.id, RunId: far.run2, Path: extShot})
	if err != nil || !bytes.Equal(extRead.Image, extPNG) || extRead.MimeType != "image/png" {
		t.Fatalf("vps-1 pinned external image read: %+v %v", extRead, err)
	}

	// The other machine stopped: what this device read before still opens; nothing else does.
	restart(true)
	eventually(t, 30*time.Second, "vps-1 offline", func() bool {
		res, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		for _, m := range res.GetMachines() {
			if m.MachineId == vpsID {
				return err == nil && !m.Online
			}
		}
		return false
	})
	one, err := sc.ReadFile(ctx, &sessionv1.ReadFileRequest{SessionId: far.id, RunId: far.run1, Path: "docs/demo.html"})
	if err != nil || one.Text != "<p>one</p>" {
		t.Fatalf("offline, kept: %+v %v", one, err)
	}
	part, err := sc.ReadFileBytes(ctx, &sessionv1.ReadFileBytesRequest{SessionId: far.id, RunId: far.run1, Path: "docs/demo.html", Offset: 3, Length: 3})
	if err != nil || string(part.Data) != "one" {
		t.Fatalf("offline, kept bytes: %+v %v", part, err)
	}
	extOffline, err := sc.ReadFile(ctx, &sessionv1.ReadFileRequest{SessionId: far.id, RunId: far.run2, Path: extShot})
	if err != nil || !bytes.Equal(extOffline.Image, extPNG) {
		t.Fatalf("offline, kept external image: %+v %v", extOffline, err)
	}
	if _, err := sc.ReadFile(ctx, &sessionv1.ReadFileRequest{SessionId: far.id, RunId: far.run2, Path: "docs/demo.js"}); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("offline, never read: %v", err)
	}
}
