package cli

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/relay"
)

func TestToolsAreTheCommands(t *testing.T) {
	tools := map[string]Tool{}
	for _, tl := range Tools() {
		tools[tl.Name] = tl
	}
	for _, name := range []string{"auth_status", "project_list", "session_start", "session_get", "session_list", "session_events",
		"session_answer", "session_interrupt", "session_archive", "session_unarchive", "session_diff",
		"message_send", "message_list", "message_ack", "message_status", "message_contacts"} {
		if _, ok := tools[name]; !ok {
			t.Errorf("no tool %s", name)
		}
	}
	for _, name := range []string{"auth_login", "auth_logout", "version", "session_send"} {
		if _, ok := tools[name]; ok {
			t.Errorf("%s is the local CLI's own", name)
		}
	}
	start := tools["session_start"]
	param := func(tl Tool, name string) *ToolParam {
		i := slices.IndexFunc(tl.Params, func(p ToolParam) bool { return p.Name == name })
		if i < 0 {
			return nil
		}
		return &tl.Params[i]
	}
	if p := param(start, "project"); p == nil || !p.Required || p.Type != "string" {
		t.Errorf("project: %+v", p)
	}
	if p := param(start, "wait"); p == nil || p.Type != "boolean" {
		t.Errorf("wait: %+v", p)
	}
	if p := param(start, "question_timeout"); p == nil || p.Type != "duration" || p.Flag != "question-timeout" {
		t.Errorf("question_timeout: %+v", p)
	}
	for _, local := range []string{"file", "prompt_file", "output"} {
		if param(start, local) != nil {
			t.Errorf("%s reads this computer's files", local)
		}
	}
	if get := tools["session_get"]; get.Arg == nil || get.Arg.Name != "session_id" || !get.Arg.Required {
		t.Errorf("session_get's argument: %+v", get.Arg)
	}
	if p := param(tools["session_answer"], "choose"); p == nil || p.Type != "array" {
		t.Errorf("choose: %+v", p)
	}
	if p := param(tools["session_events"], "limit"); p == nil || p.Type != "integer" {
		t.Errorf("limit: %+v", p)
	}
	// A list of ids as the positional argument.
	if ack := tools["message_ack"]; ack.Arg == nil || ack.Arg.Name != "message_id" || ack.Arg.Type != "array" {
		t.Errorf("message_ack's argument: %+v", ack.Arg)
	} else if args, err := ack.Args(map[string]any{"message_id": []any{"msg_a", "msg_b"}, "held": true}); err != nil ||
		!slices.Equal(args, []string{"message", "ack", "--held=true", "--", "msg_a", "msg_b"}) {
		t.Errorf("message_ack's args: %v %v", args, err)
	}
	if p := param(tools["message_send"], "to"); p == nil || !p.Required {
		t.Errorf("message_send's to: %+v", p)
	}
}

func TestToolArgs(t *testing.T) {
	var answer Tool
	for _, tl := range Tools() {
		if tl.Name == "session_answer" {
			answer = tl
		}
	}
	args, err := answer.Args(map[string]any{"session_id": "-s_x", "choose": []any{"Yes", "2"}, "wait": true, "text": "--danger"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"session", "answer", "--choose=Yes", "--choose=2", "--text=--danger", "--wait=true", "--", "-s_x"}
	for _, w := range want {
		if !slices.Contains(args, w) {
			t.Fatalf("args %q lack %q", args, w)
		}
	}
	if args[len(args)-2] != "--" {
		t.Fatalf("the session id comes after --: %q", args)
	}
	if _, err := answer.Args(map[string]any{}); err == nil {
		t.Fatal("the session id is required")
	}
	if _, err := answer.Args(map[string]any{"session_id": "s", "wait": "yes"}); err == nil {
		t.Fatal("wait is a boolean")
	}
}

func TestExecWithoutGrant(t *testing.T) {
	key, _ := identity.GenerateKey()
	doc, code := Exec(context.Background(), ExecOptions{Identity: Identity{RelayURL: "http://relay.invalid", OwnerID: "o_x", Key: key}}, []string{"project", "list"})
	var d struct {
		OK    bool `json:"ok"`
		Error struct {
			Kind string `json:"kind"`
		} `json:"error"`
	}
	if err := json.Unmarshal(doc, &d); err != nil || d.OK || d.Error.Kind != "not_paired" || code != exitDenied {
		t.Fatalf("doc %s code %d", doc, code)
	}
	// A usage error is a document too.
	doc, code = Exec(context.Background(), ExecOptions{}, []string{"session", "get"})
	if json.Unmarshal(doc, &d) != nil || d.OK || code != exitUsage {
		t.Fatalf("usage: %s %d", doc, code)
	}
}

// Embedded, an agent has no files to forget: its caller keeps the
// credentials, and the process's directory is not the agent's home.
func TestExecForgetsNoFiles(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, f := range []string{"client.key", "state.json"} {
		os.WriteFile(f, []byte("not the agent's"), 0o600)
	}
	a := &app{mem: &ExecOptions{}}
	if removed, err := a.forget(); removed || err != nil {
		t.Fatalf("forget: %v %v", removed, err)
	}
	for _, f := range []string{"client.key", "state.json"} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
}

// A Peer keeps one relay connection for the commands run with it, presents
// the newest credentials any of them picked up, and closes once the last
// command using it ends.
func TestPeer(t *testing.T) {
	// A relay claimed by an owner, whose root key signs the grants here.
	root, _ := identity.GenerateKey()
	rootPub := identity.MarshalPublicKey(root.Public())
	ownerID := identity.OwnerID(rootPub)
	rootCert, _ := identity.Sign(&identityv1.RootCert{OwnerId: ownerID, Version: 1, IssuedAt: timestamppb.Now(),
		RootKeys: []*identityv1.RootKey{{PublicKey: rootPub, Label: "test-mac", Platform: "macos"}}}, root)
	chain := &identityv1.RootChain{Certs: []*identityv1.Signed{rootCert}}
	dir := t.TempDir()
	st, err := relay.OpenStore(filepath.Join(dir, "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	device, _ := identity.Sign(&identityv1.OwnerDeviceCert{OwnerId: ownerID, DevicePublicKey: rootPub, Label: "test-mac"}, root)
	if err := st.SaveClaim(ownerID, chain, device); err != nil {
		t.Fatal(err)
	}
	st.Close()
	r, err := relay.New(relay.Config{DataDir: dir, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var dials atomic.Int32
	h := r.Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/v1/connect" {
			dials.Add(1)
		}
		h.ServeHTTP(w, req)
	}))
	defer srv.Close()
	key, _ := identity.GenerateKey()
	grant := func(version uint64) (*identityv1.Grant, *identityv1.Signed) {
		pub := identity.MarshalPublicKey(key.Public())
		g := &identityv1.Grant{OwnerId: ownerID, GrantId: "g_test", Version: version, ClientId: identity.ClientID(pub), ClientPublicKey: pub,
			MachineIds: []string{"*"}, ProjectIds: []string{"*"}, Agents: []string{"*"}, ExpiresAt: timestamppb.New(time.Now().Add(time.Hour))}
		s, err := identity.Sign(g, root)
		if err != nil {
			t.Fatal(err)
		}
		return g, s
	}
	_, g1 := grant(1)
	p := &Peer{}
	opts := ExecOptions{Identity: Identity{RelayURL: srv.URL, OwnerID: ownerID, Key: key, RootChain: chain, Grant: g1}, Peer: p}
	ctx := context.Background()
	run := func() {
		t.Helper()
		if doc, code := Exec(ctx, opts, []string{"project", "list"}); code != 0 {
			t.Fatalf("project list: %d %s", code, doc)
		}
	}
	run()
	run()
	if n := dials.Load(); n != 1 {
		t.Fatalf("%d relay connections for 2 commands, want 1", n)
	}

	// A command picks up the grant the owner renewed: the peer presents it.
	g2, s2 := grant(2)
	if err := r.Backend().PutGrant(ownerID, g2, s2); err != nil {
		t.Fatal(err)
	}
	run() // with the old grant, as a caller that has not stored the new one yet
	if p.version != 2 || !proto.Equal(p.creds.Load().GetGrant(), s2) {
		t.Fatalf("after a command adopted v2 the peer presents v%d", p.version)
	}
	// One that holds older credentials changes nothing.
	stale := &app{mem: &opts}
	if err := stale.load(); err != nil {
		t.Fatal(err)
	}
	p.adopt(stale)
	if p.version != 2 || !proto.Equal(p.creds.Load().GetGrant(), s2) {
		t.Fatalf("a command with v1 made the peer present v%d", p.version)
	}

	// Closed while a command uses it: open until that command ends.
	c, err := p.open(ctx, stale)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("closed under a command using it: %v", err)
	}
	c.Close()
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("not closed once the command ended")
	}
	if _, code := Exec(ctx, opts, []string{"project", "list"}); code == 0 || dials.Load() != 1 {
		t.Fatalf("a closed peer connected again: exit %d, %d relay connections", code, dials.Load())
	}
}
