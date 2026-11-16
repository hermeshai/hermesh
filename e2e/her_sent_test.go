package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// TestHerDeliveries: the files Her sent are listed apart from her
// conversation (docs/her-deliverables.md §4.2–§4.4), through this Mac's
// hermeshd: the machine says it answers the two requests; a file sent,
// changed and sent again is one file with two versions, each with the copy
// to read; her row counts the files the owner has not seen, until the list
// was seen. A file a routine made is the routine's (§4.11): the list shows one
// source's files, and says which sources there are; a filtered page is not
// the one kept here.
func TestHerDeliveries(t *testing.T) {
	env := newHerEnvironment(t)
	if env.container != "" {
		t.Skip("the test writes the machine's files from here")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	eventually(t, 30*time.Second, "the machine lists the three requests", func() bool {
		methods := listedMachine(t, env.c, env.machine).GetHer().GetMethods()
		return slices.Contains(methods, "deliveries/list") && slices.Contains(methods, "deliveries/seen") && slices.Contains(methods, "deliveries/sources")
	})

	type listed struct {
		Files []struct {
			Path     string `json:"path"`
			Name     string `json:"name"`
			Kind     string `json:"kind"`
			Versions []struct {
				Seq      int    `json:"seq"`
				CallID   string `json:"call_id"`
				Size     int    `json:"size"`
				Snapshot string `json:"snapshot"`
			} `json:"versions"`
		} `json:"files"`
		Seen int `json:"seen"`
	}
	listOf := func(source map[string]string) (l listed) {
		t.Helper()
		params := map[string]any{"bot_id": env.bot.ID}
		if source != nil {
			params["source"] = source
		}
		if err := json.Unmarshal(herCall(t, env, "", "deliveries/list", params), &l); err != nil {
			t.Fatal(err)
		}
		return l
	}
	list := func() listed { return listOf(nil) }
	// Her's row, as bots/subscribe lists it now.
	row := func() (r struct {
		New    int `json:"new_deliveries"`
		Latest int `json:"deliveries_latest"`
	}) {
		t.Helper()
		var all struct {
			Bots []json.RawMessage `json:"bots"`
		}
		json.Unmarshal(herCall(t, env, "", "bots/subscribe", nil), &all)
		for _, b := range all.Bots {
			var id struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(b, &id); id.ID == env.bot.ID {
				json.Unmarshal(b, &r)
			}
		}
		return r
	}
	if l := list(); len(l.Files) != 0 || l.Seen != 0 {
		t.Fatalf("before anything was sent: %+v", l)
	}

	stream, op, session := openAssistant(t, ctx, env)
	defer stream.Close()
	send := func(path string) {
		t.Helper()
		res, err := env.c.HerSend(ctx, &localv1.HerSendRequest{MachineId: env.machine, BotId: env.bot.ID, Text: "[[mock:attach:" + path + "]] Send it."})
		if err != nil || !res.Delivered {
			t.Fatalf("HerSend: %v %v", res, err)
		}
		untilEvent(t, stream, "Her's file", func(e herEvent) bool {
			return e.Type == "tool_completed" && e.Result.ToolName == "send_to_user" && !e.Result.IsError
		})
	}
	down := func(what, path, want string) {
		t.Helper()
		got, err := env.c.HerDownload(ctx, &localv1.HerDownloadRequest{MachineId: env.machine, BotId: env.bot.ID, Path: path, LocalDir: t.TempDir()})
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if b, _ := os.ReadFile(got.LocalPath); string(b) != want {
			t.Fatalf("%s came down as %q, want %q", what, b, want)
		}
	}
	folder := filepath.Join(env.home, "bots", env.bot.ID, "files")
	write := func(name, text string) {
		t.Helper()
		if err := os.MkdirAll(folder, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	const draft, final = "first draft\n", "second draft, longer\n"
	write("report.md", draft)
	write("data.csv", "a,b\n1,2\n")
	send("report.md")
	send("data.csv")
	write("report.md", final)
	send("report.md")

	// Written as the events arrive: the list has them shortly.
	var l listed
	eventually(t, 30*time.Second, "one file with two versions, and another", func() bool {
		l = list()
		return len(l.Files) == 2 && len(l.Files[0].Versions) == 2
	})
	report, data := l.Files[0], l.Files[1]
	if report.Name != "report.md" || report.Kind != "doc" || report.Path != filepath.Join(folder, "report.md") ||
		data.Name != "data.csv" || data.Kind != "sheet" || len(data.Versions) != 1 || l.Seen != 0 {
		t.Fatalf("the list, newest first: %+v", l)
	}
	if v := report.Versions; v[0].Seq != 3 || v[1].Seq != 1 || v[0].Size != len(final) || v[1].Size != len(draft) || v[0].CallID == v[1].CallID {
		t.Fatalf("the report's versions, newest first: %+v", v)
	}
	down("the latest version", report.Versions[0].Snapshot, final)
	down("the first version", report.Versions[1].Snapshot, draft)

	// Two files the owner has not seen; seen, none, and the list says where to.
	if r := row(); r.New != 2 || r.Latest != 3 {
		t.Fatalf("Her's row: %+v", r)
	}
	herCall(t, env, "", "deliveries/seen", map[string]any{"bot_id": env.bot.ID, "seq": 3})
	if r, l := row(), list(); r.New != 0 || r.Latest != 3 || l.Seen != 3 {
		t.Fatalf("after the list was seen: %+v, seen %d", r, l.Seen)
	}

	// This Mac keeps the first page as her last answered it, with the mark
	// seen moved: what the apps show while the machine is offline (§4.4).
	kept, err := env.c.GetHerConversation(ctx, &localv1.GetHerConversationRequest{MachineId: env.machine})
	if err != nil {
		t.Fatal(err)
	}
	var page listed
	if err := json.Unmarshal(kept.Deliveries, &page); err != nil || len(page.Files) != 2 || page.Files[0].Name != "report.md" || len(page.Files[0].Versions) != 2 || page.Seen != 3 {
		t.Fatalf("the page kept here: %s (%v)", kept.Deliveries, err)
	}

	// A routine that makes a file each run, sent on by Her when its report
	// wakes her: the routine's, by the run that wrote it. Her changes files
	// without asking, so that a run, which cannot ask, can write; making the
	// routine is asked about, and the owner says yes.
	if _, err := env.c.SetHerAssistantMode(ctx, &localv1.SetHerAssistantModeRequest{MachineId: env.machine, BotId: env.bot.ID, PermissionMode: "accept-edits"}); err != nil {
		t.Fatal(err)
	}
	res, err := env.c.HerSend(ctx, &localv1.HerSendRequest{MachineId: env.machine, BotId: env.bot.ID, Text: "[[mock:routine_file:Morning brief|in 2s|brief.md]] Every morning, a brief."})
	if err != nil || !res.Delivered {
		t.Fatalf("HerSend: %v %v", res, err)
	}
	untilEvent(t, stream, "the brief sent on", func(e herEvent) bool {
		if e.Type == "approval_requested" && e.Name == "manage_routine" {
			herCall(t, env, op.ConnectionId, "session/op", map[string]any{"session_id": session,
				"op": map[string]any{"type": "approval", "call_id": e.CallID, "decision": map[string]string{"type": "approve"}}})
		}
		return e.Type == "tool_completed" && e.Result.ToolName == "send_to_user" && !e.Result.IsError && sentFile(e.Result.Details, "brief.md")
	})
	var sources struct {
		Sources []struct {
			Filter map[string]string `json:"filter"`
			Name   string            `json:"name"`
			Files  int               `json:"files"`
		} `json:"sources"`
	}
	var raw json.RawMessage
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		raw = herCall(t, env, "", "deliveries/sources", map[string]string{"bot_id": env.bot.ID})
		json.Unmarshal(raw, &sources)
		if len(sources.Sources) == 2 && sources.Sources[0].Filter["kind"] == "routine" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the routine among the sources: %s", raw)
		}
	}
	routine := sources.Sources[0]
	if routine.Name != "Morning brief" || routine.Files != 1 || sources.Sources[1].Filter["kind"] != "conversation" || sources.Sources[1].Files != 2 {
		t.Fatalf("the sources: %+v", sources)
	}
	if all := list(); len(all.Files) != 3 || all.Files[0].Name != "brief.md" {
		t.Fatalf("all of them: %+v", all)
	}
	if only := listOf(routine.Filter); len(only.Files) != 1 || only.Files[0].Name != "brief.md" {
		t.Fatalf("the routine's: %+v", only)
	}
	if only := listOf(map[string]string{"kind": "conversation"}); len(only.Files) != 2 {
		t.Fatalf("the conversation's: %+v", only)
	}
	if kept, err = env.c.GetHerConversation(ctx, &localv1.GetHerConversationRequest{MachineId: env.machine}); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(kept.Deliveries, &page); err != nil || len(page.Files) != 3 {
		t.Fatalf("the page kept here is all of them, not the last filtered one: %s (%v)", kept.Deliveries, err)
	}
}

// sentFile: a send_to_user result's details name a file of that name.
func sentFile(details json.RawMessage, name string) bool {
	var d struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	json.Unmarshal(details, &d)
	for _, f := range d.Files {
		if filepath.Base(f.Path) == name {
			return true
		}
	}
	return false
}

// TestHerSentCopies: her keeps a copy of each file Her sends, as it was when
// she sent it (docs/her-deliverables.md §4.1), and this Mac's hermeshd brings
// a copy down like a file of the assistant's folder. A file changed on the
// machine and sent again: each message names its own copy, and each comes
// down as it was sent, while the file itself comes down as it is now. A file
// outside the assistant's folder, which a download refuses where it is, comes
// down by its copy.
func TestHerSentCopies(t *testing.T) {
	env := newHerEnvironment(t)
	if env.container != "" {
		t.Skip("the test writes the machine's files from here")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	stream, _, _ := openAssistant(t, ctx, env)
	defer stream.Close()

	type sentFile struct {
		Path     string `json:"path"`
		Size     int    `json:"size"`
		Sha256   string `json:"sha256"`
		Snapshot string `json:"snapshot"`
	}
	// Her sends the file at path (her-mock's attach), and what the message's
	// result says of it.
	send := func(path string) sentFile {
		t.Helper()
		res, err := env.c.HerSend(ctx, &localv1.HerSendRequest{MachineId: env.machine, BotId: env.bot.ID, Text: "[[mock:attach:" + path + "]] Send it."})
		if err != nil || !res.Delivered {
			t.Fatalf("HerSend: %v %v", res, err)
		}
		var attachment struct {
			Files []sentFile `json:"files"`
		}
		untilEvent(t, stream, "Her's file", func(e herEvent) bool {
			attachment.Files = nil
			return e.Type == "tool_completed" && e.Result.ToolName == "send_to_user" && !e.Result.IsError &&
				json.Unmarshal(e.Result.Details, &attachment) == nil && len(attachment.Files) == 1
		})
		return attachment.Files[0]
	}
	// The file comes down as want, under name.
	down := func(what, path, name, want string) {
		t.Helper()
		got, err := env.c.HerDownload(ctx, &localv1.HerDownloadRequest{MachineId: env.machine, BotId: env.bot.ID, Path: path, LocalDir: t.TempDir()})
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if b, _ := os.ReadFile(got.LocalPath); filepath.Base(got.LocalPath) != name || string(b) != want {
			t.Fatalf("%s came down as %s: %q, want %q", what, got.LocalPath, b, want)
		}
	}
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Where her keeps the copy of that content, under that name.
	kept := func(text, name string) string {
		sum := sha256.Sum256([]byte(text))
		return filepath.Join(env.home, "bots", env.bot.ID, "sent", hex.EncodeToString(sum[:]), name)
	}

	const draft, final = "first draft\n", "second draft, longer\n"
	report := filepath.Join(env.home, "bots", env.bot.ID, "files", "report.md")
	write(report, draft)
	first := send("report.md")
	write(report, final)
	second := send("report.md")

	if first.Path != report || second.Path != report {
		t.Fatalf("the same file both times: %q, %q, want %q", first.Path, second.Path, report)
	}
	if first.Snapshot != kept(draft, "report.md") || second.Snapshot != kept(final, "report.md") || first.Size != len(draft) || second.Size != len(final) {
		t.Fatalf("each message names the copy of what it sent:\n%+v\n%+v", first, second)
	}
	down("the first message's copy", first.Snapshot, "report.md", draft)
	down("the second message's copy", second.Snapshot, "report.md", final)
	down("the file where it is", report, "report.md", final)
	if again := send("report.md"); again.Snapshot != second.Snapshot {
		t.Fatalf("the same content sent again is the same copy: %+v", again)
	}

	// Outside the assistant's folder: not to be had where it is, and had by
	// its copy.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	contract := filepath.Join(dir, "contract.txt")
	write(contract, "outside her folder\n")
	third := send(contract)
	if third.Path != contract || third.Snapshot != kept("outside her folder\n", "contract.txt") {
		t.Fatalf("a file from outside her folder: %+v", third)
	}
	if _, err := env.c.HerDownload(ctx, &localv1.HerDownloadRequest{MachineId: env.machine, BotId: env.bot.ID, Path: contract, LocalDir: t.TempDir()}); err == nil {
		t.Fatal("a file outside the assistant's folder came down by where it is")
	}
	down("its copy", third.Snapshot, "contract.txt", "outside her folder\n")
}
