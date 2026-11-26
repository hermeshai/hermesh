package kernel

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// A session whose two turns write the same files differently: the files read as each
// turn left them, as they are now, and after the worktree is released.
func TestSessionFilesByTurnAndNow(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	repo := initGitProject(t)
	_ = os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("secret.env\n"), 0o644)
	for _, args := range [][]string{{"add", ".gitignore"}, {"commit", "-m", "ignore"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := eng.SyncWorkspace("repo", repo, "main", identityv1.Workspace_WORKSPACE_WORKTREE, "", nil); err != nil {
		t.Fatal(err)
	}
	owner := OwnerPrincipal("dev_owner", "Owner")
	started, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ProjectId:      "repo",
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "scripted", Model: "scripted-fast"},
		InitialPrompt:  "write docs/demo.html|<p>one</p>\nwrite docs/a10.txt|x\nwrite docs/a2.txt|x\nwrite secret.env|TOKEN=1",
	})
	if err != nil {
		t.Fatal(err)
	}
	sid := started.Session.SessionId
	wt := waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE).Session.WorkspaceStrategy.WorktreePath
	if _, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{SessionId: sid, Text: "write docs/demo.html|<p>two, longer</p>"}); err != nil {
		t.Fatal(err)
	}
	waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)
	run := map[uint32]string{}
	cps, _ := eng.ListCheckpoints(ctx, owner, sid)
	for _, c := range cps {
		run[c.RunOrdinal] = c.RunId
	}
	if run[1] == "" || run[2] == "" {
		t.Fatalf("checkpoints: %v", cps)
	}

	// Which paths are files, as each form a reply writes them in.
	res, err := eng.ResolveFiles(ctx, owner, &sessionv1.ResolveFilesRequest{SessionId: sid, RunId: run[1], Paths: []string{
		"file://" + strings.ReplaceAll(filepath.Join(wt, "docs", "demo.html"), " ", "%20"),
		"docs/demo.html", "docs", "nope.txt", "/etc/hosts", ".git/config", "main.go",
	}})
	if err != nil {
		t.Fatal(err)
	}
	kinds := func(fs []*sessionv1.ResolvedFile) []string {
		var out []string
		for _, f := range fs {
			out = append(out, strings.TrimPrefix(f.Kind.String(), "FILE_REF_KIND_")+":"+f.Path)
		}
		return out
	}
	want := []string{"FILE:docs/demo.html", "FILE:docs/demo.html", "FOLDER:docs", "MISSING:nope.txt", "OUTSIDE:/etc/hosts", "OUTSIDE:" + filepath.Join(wt, ".git/config"), "FILE:main.go"}
	if got := kinds(res.Files); !slices.Equal(got, want) {
		t.Fatalf("resolve = %v, want %v", got, want)
	}
	if res.Files[0].Turn != 1 || res.Files[0].Size != uint64(len("<p>one</p>")) || res.Folder != wt || res.FolderGone {
		t.Fatalf("resolve detail: %+v folder=%q", res.Files[0], res.Folder)
	}

	read := func(p *Principal, runID, path string) (*sessionv1.ReadFileResponse, error) {
		return eng.ReadFile(ctx, p, &sessionv1.ReadFileRequest{SessionId: sid, RunId: runID, Path: path})
	}
	// As turn 1 left it, which is not what is there now.
	r1, err := read(owner, run[1], "docs/demo.html")
	if err != nil || r1.Text != "<p>one</p>" || r1.Turn != 1 || r1.Now != sessionv1.FileNow_FILE_NOW_CHANGED || r1.Kind != sessionv1.FileContentKind_FILE_CONTENT_KIND_TEXT {
		t.Fatalf("turn 1 = %+v, %v", r1, err)
	}
	if !strings.HasPrefix(r1.MimeType, "text/html") {
		t.Fatalf("mime = %q", r1.MimeType)
	}
	r2, err := read(owner, run[2], "docs/demo.html")
	if err != nil || r2.Text != "<p>two, longer</p>" || r2.Turn != 2 || r2.Now != sessionv1.FileNow_FILE_NOW_SAME {
		t.Fatalf("turn 2 = %+v, %v", r2, err)
	}
	now, err := read(owner, "", filepath.Join(wt, "docs/demo.html"))
	if err != nil || now.Text != "<p>two, longer</p>" || now.Turn != 0 || now.Now != sessionv1.FileNow_FILE_NOW_UNSPECIFIED {
		t.Fatalf("now = %+v, %v", now, err)
	}

	// A range, from the turn and from the folder.
	part, err := eng.ReadFileBytes(ctx, owner, &sessionv1.ReadFileBytesRequest{SessionId: sid, RunId: run[1], Path: "docs/demo.html", Offset: 3, Length: 3})
	if err != nil || string(part.Data) != "one" || part.Size != 10 || part.Turn != 1 {
		t.Fatalf("range = %+v, %v", part, err)
	}
	tail, err := eng.ReadFileBytes(ctx, owner, &sessionv1.ReadFileBytesRequest{SessionId: sid, Path: "docs/demo.html", Offset: 12})
	if err != nil || string(tail.Data) != "er</p>" || tail.Turn != 0 {
		t.Fatalf("tail = %+v, %v", tail, err)
	}

	// The folder now: folders first, names as people read them, .git left out.
	list, err := eng.ListFiles(ctx, owner, &sessionv1.ListFilesRequest{SessionId: sid})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range list.Entries {
		names = append(names, e.Name)
	}
	if !slices.Equal(names[:3], []string{".claude", "docs", "skills"}) || slices.Contains(names, ".git") || !slices.Contains(names, "secret.env") {
		t.Fatalf("list = %v", names)
	}
	docs, err := eng.ListFiles(ctx, owner, &sessionv1.ListFilesRequest{SessionId: sid, Dir: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	names = nil
	for _, e := range docs.Entries {
		names = append(names, e.Name)
	}
	if !slices.Equal(names, []string{"a2.txt", "a10.txt", "demo.html"}) {
		t.Fatalf("docs = %v", names)
	}

	// Never outside the folder: a link out, .git, a path that climbs.
	_ = os.Symlink("/etc/hosts", filepath.Join(wt, "leak"))
	for _, p := range []string{"leak", ".git/config", "../" + filepath.Base(wt) + "-other/x", "/etc/hosts"} {
		if _, err := read(owner, "", p); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("read %q: %v, want permission denied", p, err)
		}
	}
	if _, err := read(owner, "", "nope.txt"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing: %v", err)
	}

	// What Git ignores is the owner's: a grant that sees the session reads the rest only.
	grant := &Principal{Actor: &sessionv1.Actor{Kind: sessionv1.ActorKind_ACTOR_KIND_EXTERNAL_AGENT, Id: "agent_x"}, AllowedProjects: []string{"repo"}, TaskVisibility: identityv1.TaskVisibility_TASK_VISIBILITY_ALL}
	if r, err := read(owner, "", "secret.env"); err != nil || r.Text != "TOKEN=1" {
		t.Fatalf("owner, ignored: %+v, %v", r, err)
	}
	if _, err := read(grant, "", "secret.env"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("grant, ignored: %v", err)
	}
	if r, err := read(grant, run[1], "docs/demo.html"); err != nil || r.Text != "<p>one</p>" {
		t.Fatalf("grant, tracked: %+v, %v", r, err)
	}
	other := &Principal{Actor: &sessionv1.Actor{Kind: sessionv1.ActorKind_ACTOR_KIND_EXTERNAL_AGENT, Id: "agent_y"}, AllowedProjects: []string{"elsewhere"}}
	if _, err := read(other, "", "docs/demo.html"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("no grant: %v", err)
	}

	// The worktree released: a turn's files still open, and nothing makes the worktree again.
	if err := eng.vcs.RemoveWorktree(ctx, repo, wt); err != nil {
		t.Fatal(err)
	}
	gone, err := read(owner, run[1], "docs/demo.html")
	if err != nil || gone.Text != "<p>one</p>" || gone.Now != sessionv1.FileNow_FILE_NOW_UNKNOWN {
		t.Fatalf("released, turn 1 = %+v, %v", gone, err)
	}
	last, err := read(owner, "", "docs/demo.html")
	if err != nil || last.Text != "<p>two, longer</p>" || last.Turn != 2 {
		t.Fatalf("released, now = %+v, %v", last, err)
	}
	listed, err := eng.ListFiles(ctx, owner, &sessionv1.ListFilesRequest{SessionId: sid, Dir: "docs"})
	if err != nil || !listed.FolderGone || listed.Turn != 2 || len(listed.Entries) != 3 {
		t.Fatalf("released, list = %+v, %v", listed, err)
	}
	if _, err := os.Stat(wt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the worktree came back: %v", err)
	}

	// Deleted, its turns' files go with it: no checkpoint refs left in the project's repository.
	refs := func() string {
		out, _ := exec.Command("git", "-C", repo, "for-each-ref", "refs/hermesh/checkpoints/").CombinedOutput()
		return strings.TrimSpace(string(out))
	}
	if refs() == "" {
		t.Fatal("no checkpoint refs before deleting")
	}
	if err := eng.DeleteSession(ctx, owner, sid); err != nil {
		t.Fatal(err)
	}
	if got := refs(); got != "" {
		t.Fatalf("checkpoint refs left: %s", got)
	}
}

func TestFileTextDecoding(t *testing.T) {
	for _, c := range []struct {
		in       string
		text     string
		encoding string
		ok       bool
	}{
		{"plain\r\n", "plain\r\n", "utf-8", true},
		{"\xEF\xBB\xBFwith bom", "with bom", "utf-8-bom", true},
		{"\xFF\xFEh\x00i\x00", "hi", "utf-16le", true},
		{"\xFE\xFF\x00h\x00i", "hi", "utf-16be", true},
		{"\xB1\xA8\xB8\xE6,1\n", "报告,1\n", "gb18030", true},
		{"PK\x03\x04\x00\x00binary", "", "", false},
		{"", "", "utf-8", true},
	} {
		text, enc, ok := decodeText([]byte(c.in))
		if text != c.text || enc != c.encoding || ok != c.ok {
			t.Errorf("decode(%q) = %q, %q, %v; want %q, %q, %v", c.in, text, enc, ok, c.text, c.encoding, c.ok)
		}
	}
	out := &sessionv1.ReadFileResponse{}
	describe(out, []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR"))
	if out.Kind != sessionv1.FileContentKind_FILE_CONTENT_KIND_IMAGE || out.MimeType != "image/png" || len(out.Image) == 0 {
		t.Fatalf("png: %+v", out)
	}
	out = &sessionv1.ReadFileResponse{}
	describe(out, []byte("\x00\x01\x02"))
	if out.Kind != sessionv1.FileContentKind_FILE_CONTENT_KIND_BINARY {
		t.Fatalf("binary: %+v", out)
	}
}

func TestNaturalCompare(t *testing.T) {
	names := []string{"b.txt", "a10.txt", "a2.txt", "A1.txt", "a02.txt"}
	slices.SortStableFunc(names, naturalCompare)
	if want := []string{"A1.txt", "a02.txt", "a2.txt", "a10.txt", "b.txt"}; !slices.Equal(names, want) {
		t.Fatalf("sorted = %v, want %v", names, want)
	}
}

func makeTestPNG(t *testing.T, w, h int, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
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

// External screenshots (/tmp, ~/.gemini/.../brain) referenced by an assistant reply are
// readable by the owner (never by scoped grants), verified by magic bytes, pinned into
// blobs/ at turn end so later overwrites or /tmp cleanup do not break historical turns,
// and pruned when the session is deleted.
func TestExternalTurnImagesPinnedAndMagicVerified(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := openTestEngine(t, t.TempDir(), time.Minute)
	defer eng.Close()
	repo := initGitProject(t)
	if err := eng.SyncWorkspace("repo", repo, "main", identityv1.Workspace_WORKSPACE_IN_PLACE, "", nil); err != nil {
		t.Fatal(err)
	}
	extDir := t.TempDir()
	shotPath := filepath.Join(extDir, "ios_simulator_screenshot.png")
	fakePNG := filepath.Join(extDir, "secret_key.png")
	secretTxt := filepath.Join(extDir, "id_rsa")
	pngV1 := makeTestPNG(t, 12, 24, color.NRGBA{R: 30, G: 140, B: 255, A: 255})
	pngV2 := makeTestPNG(t, 20, 20, color.NRGBA{R: 240, G: 80, B: 60, A: 255})
	if err := os.WriteFile(shotPath, pngV1, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fakePNG, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nnot-an-image\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretTxt, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	owner := OwnerPrincipal("dev_owner", "Owner")
	grant := &Principal{
		Actor:           &sessionv1.Actor{Kind: sessionv1.ActorKind_ACTOR_KIND_EXTERNAL_AGENT, Id: "agent_x"},
		AllowedProjects: []string{"repo"},
		TaskVisibility:  identityv1.TaskVisibility_TASK_VISIBILITY_ALL,
	}

	started, err := eng.StartSession(ctx, owner, &sessionv1.StartSessionRequest{
		ProjectId:      "repo",
		ModelSelection: &sessionv1.ModelSelection{InstanceId: "scripted", Model: "scripted-fast"},
		InitialPrompt:  "say ![模拟器截图](" + shotPath + ")\n截图文件：[ios_simulator_screenshot.png](file://" + filepath.ToSlash(shotPath) + ")",
	})
	if err != nil {
		t.Fatal(err)
	}
	sid := started.Session.SessionId
	waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)

	// Turn 2 overwrites the same external screenshot path with different PNG bytes.
	if err := os.WriteFile(shotPath, pngV2, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.DispatchMessage(ctx, owner, &sessionv1.DispatchMessageRequest{
		SessionId: sid,
		Text:      "say 更新后：[新版截图](" + shotPath + ")",
	}); err != nil {
		t.Fatal(err)
	}
	waitForSessionStatus(t, eng, owner, sid, sessionv1.SessionStatus_SESSION_STATUS_IDLE)

	runs := map[uint32]string{}
	cps, _ := eng.ListCheckpoints(ctx, owner, sid)
	for _, c := range cps {
		runs[c.RunOrdinal] = c.RunId
	}
	if runs[1] == "" || runs[2] == "" {
		t.Fatalf("checkpoints = %v", cps)
	}

	// Owner resolves valid external PNG (raw path and file:// URL), while fake .png and non-image stay OUTSIDE.
	res, err := eng.ResolveFiles(ctx, owner, &sessionv1.ResolveFilesRequest{
		SessionId: sid,
		RunId:     runs[1],
		Paths:     []string{shotPath, "file://" + filepath.ToSlash(shotPath), fakePNG, secretTxt},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Files[0].Kind != sessionv1.FileRefKind_FILE_REF_KIND_FILE || res.Files[0].Turn != 1 || res.Files[0].Size != uint64(len(pngV1)) {
		t.Fatalf("resolve shot = %+v", res.Files[0])
	}
	if res.Files[1].Kind != sessionv1.FileRefKind_FILE_REF_KIND_FILE || res.Files[1].Path != res.Files[0].Path {
		t.Fatalf("resolve file:// shot = %+v", res.Files[1])
	}
	if res.Files[2].Kind != sessionv1.FileRefKind_FILE_REF_KIND_OUTSIDE || res.Files[3].Kind != sessionv1.FileRefKind_FILE_REF_KIND_OUTSIDE {
		t.Fatalf("fake/secret must stay OUTSIDE: %+v %+v", res.Files[2], res.Files[3])
	}
	for _, bad := range []string{fakePNG, secretTxt} {
		if _, err := eng.ReadFile(ctx, owner, &sessionv1.ReadFileRequest{SessionId: sid, Path: bad}); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("ReadFile(%s) = %v, want PermissionDenied", bad, err)
		}
	}

	// Scoped external grant cannot resolve or read outside the workspace even for a valid PNG.
	gRes, err := eng.ResolveFiles(ctx, grant, &sessionv1.ResolveFilesRequest{SessionId: sid, RunId: runs[1], Paths: []string{shotPath}})
	if err != nil || gRes.Files[0].Kind != sessionv1.FileRefKind_FILE_REF_KIND_OUTSIDE {
		t.Fatalf("grant resolve = %+v, err=%v", gRes, err)
	}
	if _, err := eng.ReadFile(ctx, grant, &sessionv1.ReadFileRequest{SessionId: sid, RunId: runs[1], Path: shotPath}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("grant read = %v, want PermissionDenied", err)
	}

	// Turn 1 sees Now=CHANGED while v2 is on disk; deleting the /tmp file still preserves both Turn 1 and Turn 2!
	r1Before, err := eng.ReadFile(ctx, owner, &sessionv1.ReadFileRequest{SessionId: sid, RunId: runs[1], Path: shotPath})
	if err != nil || !bytes.Equal(r1Before.Image, pngV1) || r1Before.Now != sessionv1.FileNow_FILE_NOW_CHANGED {
		t.Fatalf("turn 1 before remove = %+v, err=%v", r1Before, err)
	}
	digests := eng.st.SessionTurnImageDigests(sid)
	if len(digests) != 2 {
		t.Fatalf("expected 2 pinned digests, got %v", digests)
	}
	if err := os.Remove(shotPath); err != nil {
		t.Fatal(err)
	}

	r1After, err := eng.ReadFile(ctx, owner, &sessionv1.ReadFileRequest{SessionId: sid, RunId: runs[1], Path: shotPath})
	if err != nil || !bytes.Equal(r1After.Image, pngV1) || r1After.Turn != 1 || r1After.Now != sessionv1.FileNow_FILE_NOW_GONE {
		t.Fatalf("turn 1 after /tmp delete = %+v, err=%v", r1After, err)
	}
	r2After, err := eng.ReadFile(ctx, owner, &sessionv1.ReadFileRequest{SessionId: sid, RunId: runs[2], Path: "file://" + filepath.ToSlash(shotPath)})
	if err != nil || !bytes.Equal(r2After.Image, pngV2) || r2After.Turn != 2 || r2After.Now != sessionv1.FileNow_FILE_NOW_GONE {
		t.Fatalf("turn 2 after /tmp delete = %+v, err=%v", r2After, err)
	}
	bytesAfter, err := eng.ReadFileBytes(ctx, owner, &sessionv1.ReadFileBytesRequest{SessionId: sid, RunId: runs[2], Path: shotPath, Offset: 0, Length: 64})
	if err != nil || !bytes.Equal(bytesAfter.Data, pngV2[:min(64, len(pngV2))]) {
		t.Fatalf("ReadFileBytes pinned = %+v, err=%v", bytesAfter, err)
	}

	// Deleting the session prunes its pinned turn image blobs from disk.
	if err := eng.DeleteSession(ctx, owner, sid); err != nil {
		t.Fatal(err)
	}
	for _, d := range digests {
		if _, err := os.Stat(eng.st.BlobPath(d)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("blob %s not pruned after DeleteSession: %v", d, err)
		}
	}
}
