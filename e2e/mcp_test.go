package e2e

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/internal/cli"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// TestMCPGateway connects an agent product to a self-hosted relay's MCP
// gateway the way Claude or Codex does (discovery from a 401, dynamic
// registration, authorization code with PKCE, the consent page), before
// Hermesh is even set up, and follows the connection until it works:
//
//   - connected with nothing set up: every tool says how to set Hermesh up;
//   - the owner sets up the Mac: the approval request reaches it by itself,
//     naming the relay as the key's custodian;
//   - approved: the same tokens work, the tools are the CLI's commands and
//     answer with its documents, and the connection sees sessions others
//     started but steers only its own;
//   - refresh rotates tokens; a code used twice is refused;
//   - a second connection made while the Mac is online is approved on the
//     consent page itself;
//   - the app disconnecting (RFC 7009) revokes its grant on the relay, and
//     the owner revoking the first one ends it.
func TestMCPGateway(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	bin := binaries(t)
	tmp := t.TempDir()
	sockDir, _ := os.MkdirTemp("/tmp", "hm")
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	var logs []*proc
	t.Cleanup(func() {
		if t.Failed() {
			for _, p := range logs {
				t.Logf("==== %s output ====\n%s", p.name, p.output())
			}
		}
	})
	ctx := context.Background()

	// A relay serving MCP, not claimed yet: nobody has set Hermesh up.
	port := freePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	relayData := filepath.Join(tmp, "relay")
	relay := start(t, "relay", nil, filepath.Join(bin, "hermesh-relay"), "serve", "--mcp", "--data-dir", relayData, "--listen", fmt.Sprintf("127.0.0.1:%d", port))
	logs = append(logs, relay)
	eventually(t, 20*time.Second, "relay up", func() bool { return strings.Contains(relay.output(), "relay listening") })

	// --- Connect before anything is set up -------------------------------
	app := newOAuthApp(t, base+"/mcp", "Hermesh test")
	page := app.authorize()
	if !strings.Contains(page, "Connect Hermesh test to Hermesh") || !strings.Contains(page, "isn&#39;t set up on a Mac yet") {
		t.Fatalf("consent page:\n%s", page)
	}
	if !strings.Contains(page, "127.0.0.1:") || !strings.Contains(page, "holds this connection&#39;s key") {
		t.Fatalf("the consent page must say where the app returns to and who holds the key:\n%s", page)
	}
	progress := app.connect(page)
	if !strings.Contains(progress, "One more step") || !strings.Contains(progress, "Get Hermesh for Mac") {
		t.Fatalf("after connecting with nothing set up:\n%s", progress)
	}
	tokens := app.finish(progress)
	session := app.mcpSession(tokens.Access)

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	for _, want := range append([]string{"hermesh_status"}, cliToolNames()...) {
		if !slices.Contains(names, want) {
			t.Fatalf("tools %v lack %s", names, want)
		}
	}
	checkMCPDescriptions(t, session, tools.Tools)
	// Before setup the tools answer as for an owner with nothing in it,
	// with a note on what to do.
	doc := callTool(t, session, "project_list", nil)
	if !doc.OK || len(projectsOf(doc).Projects) != 0 || !strings.Contains(doc.Note, "https://github.com/hermeshai/hermesh/releases/latest") {
		t.Fatalf("project_list before setup: %s", doc.raw)
	}
	if doc = callTool(t, session, "session_start", map[string]any{"project": "demo", "prompt": "hi"}); doc.OK || doc.Error.Kind != "not_found" ||
		!strings.Contains(doc.Error.Hint, "https://github.com/hermeshai/hermesh/releases/latest") {
		t.Fatalf("session_start before setup: %s", doc.raw)
	}
	if st := statusOf(t, session); st.State != "awaiting_setup" {
		t.Fatalf("status before setup: %+v", st)
	}

	// --- The owner sets up the Mac ----------------------------------------
	claim := strings.TrimSpace(run(t, nil, 10*time.Second, filepath.Join(bin, "hermesh-relay"), "claim-code", "--data-dir", relayData).stdout)
	sock := filepath.Join(sockDir, "o.sock")
	owner := start(t, "owner", []string{"HERMESH_FAKE_PROVIDERS=1"}, filepath.Join(bin, "hermeshd"), "run", "--owner", "--data-dir", filepath.Join(tmp, "owner-mac"), "--socket", sock)
	logs = append(logs, owner)
	root, _ := identity.GenerateKey()
	mac := newTestApp(t, sock, root)
	setup, err := mac.client.Setup(ctx, &localv1.SetupRequest{RelayUrl: base, ClaimCode: claim, RootPublicKey: rootPub(root), RootLabel: "test-mac", RootPlatform: "macos"})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	local, err := mac.client.SetLocalMachine(ctx, &localv1.SetLocalMachineRequest{Enabled: true, MachineName: "owner-mac"})
	if err != nil {
		t.Fatal(err)
	}
	repo := gitRepo(t, map[string]string{"README.md": "# demo\n"})
	eventually(t, 30*time.Second, "machine online", func() bool {
		res, err := mac.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		return err == nil && len(res.Machines) == 1 && res.Machines[0].Online && res.Machines[0].Status != nil
	})
	if _, err := mac.client.AddProject(ctx, &localv1.AddProjectRequest{MachineId: local.MachineId, ProjectId: "demo", Path: repo}); err != nil {
		t.Fatal(err)
	}

	// The approval request reaches the Mac by itself, naming its custodian.
	var req *localv1.PairingDetails
	eventually(t, 30*time.Second, "the approval request on the Mac", func() bool {
		l, err := mac.client.ListPairingRequests(ctx, &localv1.ListPairingRequestsRequest{})
		if err != nil || len(l.Pairings) != 1 {
			return false
		}
		req = l.Pairings[0]
		return true
	})
	// Confirmed on the consent page: the app shows no code to compare.
	if req.Kind != identityv1.PairingKind_PAIRING_KIND_CLIENT || req.Custodian != base || req.Label != "Hermesh test (127.0.0.1)" || req.Platform != "mcp" || !req.Confirmed {
		t.Fatalf("approval request: %+v", req)
	}
	// Used again while it waits: no second request, and the note sends the
	// person to the Mac, with no code: the Mac shows none for it.
	doc = callTool(t, session, "project_list", nil)
	if !doc.OK || len(projectsOf(doc).Projects) != 0 || !strings.Contains(doc.Note, "the request is waiting there now") || strings.Contains(doc.Note, req.Code) {
		t.Fatalf("project_list while the request waits on the Mac: %s", doc.raw)
	}
	if l, err := mac.client.ListPairingRequests(ctx, &localv1.ListPairingRequestsRequest{}); err != nil || len(l.Pairings) != 1 {
		t.Fatalf("requests on the Mac after another use: %v %v", l, err)
	}
	if _, err := mac.client.ApproveClientPairing(ctx, &localv1.ApproveClientPairingRequest{Code: req.Code, Spec: &localv1.GrantSpec{}}); err != nil {
		t.Fatal(err)
	}

	// --- Approved: the same tokens work ----------------------------------
	var places struct {
		Projects []struct{ Project string } `json:"projects"`
	}
	eventually(t, 30*time.Second, "the tools working after approval", func() bool {
		doc = callTool(t, session, "project_list", nil)
		return doc.OK && json.Unmarshal(doc.raw, &places) == nil && len(places.Projects) == 1
	})
	if places.Projects[0].Project != "owner-mac/demo" {
		t.Fatalf("projects: %s", doc.raw)
	}
	clients, err := mac.client.ListClients(ctx, &localv1.ListClientsRequest{})
	if err != nil || len(clients.Clients) != 1 {
		t.Fatalf("clients: %v %v", clients, err)
	}
	mcpClient := clients.Clients[0]
	if mcpClient.Custodian != base || mcpClient.Spec.TaskVisibility != identityv1.TaskVisibility_TASK_VISIBILITY_ALL {
		t.Fatalf("the MCP client's grant: %+v", mcpClient)
	}
	if st := statusOf(t, session); st.State != "active" {
		t.Fatalf("status once approved: %+v", st)
	}

	// The tools are the CLI's commands, with its documents.
	if doc = callTool(t, session, "session_start", map[string]any{"project": "demo"}); doc.OK || doc.Error.Kind != "usage" {
		t.Fatalf("session_start without a prompt: %s", doc.raw)
	}
	doc = callTool(t, session, "session_start", map[string]any{"project": "demo", "prompt": "say hello from mcp", "wait": true, "timeout": "45s"})
	var started struct {
		Session struct {
			SessionID string `json:"session_id"`
			Status    string `json:"status"`
			Result    string `json:"result"`
			CreatedBy string `json:"created_by"`
		} `json:"session"`
	}
	if !doc.OK || json.Unmarshal(doc.raw, &started) != nil || started.Session.Status != "idle" || !strings.Contains(started.Session.Result, "hello from mcp") {
		t.Fatalf("session_start --wait: %s", doc.raw)
	}
	id := started.Session.SessionID
	if started.Session.CreatedBy != mcpClient.ClientId {
		t.Fatalf("the session is the MCP client's: %s", doc.raw)
	}
	if doc = callTool(t, session, "message_send", map[string]any{"to": id, "message": "say second turn", "wait": true}); !doc.OK ||
		!strings.Contains(string(doc.raw), "second turn") {
		t.Fatalf("message_send: %s", doc.raw)
	}
	if doc = callTool(t, session, "session_events", map[string]any{"session_id": id, "limit": 5}); !doc.OK || !strings.Contains(string(doc.raw), `"page"`) {
		t.Fatalf("session_events: %s", doc.raw)
	}
	// A timed-out wait does not cancel the turn or require resending input.
	doc = callTool(t, session, "message_send", map[string]any{"to": id, "message": "sleep 3s\nsay finished after timeout", "wait": true, "timeout": "1s"})
	if doc.OK || doc.Error.Kind != "wait_timeout" || json.Unmarshal(doc.raw, &started) != nil || started.Session.SessionID != id {
		t.Fatalf("message_send wait timeout: %s", doc.raw)
	}
	eventually(t, 15*time.Second, "poll the turn after wait_timeout", func() bool {
		doc = callTool(t, session, "session_get", map[string]any{"session_id": id})
		return doc.OK && json.Unmarshal(doc.raw, &started) == nil && started.Session.Status == "idle" && strings.Contains(started.Session.Result, "finished after timeout")
	})
	if doc = callTool(t, session, "session_get", map[string]any{"session_id": "s_nope_nope"}); doc.OK || doc.Error.Kind != "not_found" {
		t.Fatalf("session_get of no session: %s", doc.raw)
	}

	// --- The Mac goes offline ----------------------------------------------
	ownerArgs := []string{"run", "--owner", "--data-dir", filepath.Join(tmp, "owner-mac"), "--socket", sock}
	owner.stop()
	mac.cancel()
	eventually(t, 30*time.Second, "the machine listed offline", func() bool {
		doc = callTool(t, session, "project_list", nil)
		l := projectsOf(doc)
		return doc.OK && len(l.Offline) == 1 && l.Offline[0].Machine == "owner-mac" && len(l.Projects) == 0
	})
	if doc = callTool(t, session, "session_get", map[string]any{"session_id": id}); doc.OK || doc.Error.Kind != "unreachable" {
		t.Fatalf("session_get with its machine offline: %s", doc.raw)
	}
	// An app connected now waits for the Mac, and says so.
	app3 := newOAuthApp(t, base+"/mcp", "Third app")
	page3 := app3.connect(app3.authorize())
	if !strings.Contains(page3, "One more step") || !strings.Contains(page3, "the next time it is open") {
		t.Fatalf("connecting while the Mac is offline:\n%s", page3)
	}
	s3 := app3.mcpSession(app3.finish(page3).Access)
	if doc = callTool(t, s3, "project_list", nil); !doc.OK || !strings.Contains(doc.Note, "as soon as it is online") {
		t.Fatalf("a tool while the Mac is offline: %s", doc.raw)
	}
	// The Mac is back: the request reaches it by itself.
	owner = start(t, "owner", []string{"HERMESH_FAKE_PROVIDERS=1"}, filepath.Join(bin, "hermeshd"), ownerArgs...)
	logs = append(logs, owner)
	mac = newTestApp(t, sock, root)
	var req3 *localv1.PairingDetails
	eventually(t, 60*time.Second, "the third request on the Mac", func() bool {
		l, err := mac.client.ListPairingRequests(ctx, &localv1.ListPairingRequestsRequest{})
		if err != nil || len(l.Pairings) != 1 {
			return false
		}
		req3 = l.Pairings[0]
		return true
	})
	if req3.Label != "Third app (127.0.0.1)" {
		t.Fatalf("third request: %+v", req3)
	}
	if _, err := mac.client.ApproveClientPairing(ctx, &localv1.ApproveClientPairingRequest{Code: req3.Code, Spec: &localv1.GrantSpec{}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "both apps working with the Mac back", func() bool {
		d1, d3 := callTool(t, session, "project_list", nil), callTool(t, s3, "project_list", nil)
		l1, l3 := projectsOf(d1), projectsOf(d3)
		return d1.OK && d3.OK && len(l1.Offline) == 0 && len(l3.Projects) == 1 && l3.Projects[0].Project == "owner-mac/demo"
	})

	// It sees a session another agent started, and cannot steer it.
	agentEnv := []string{"HERMESH_HOME=" + filepath.Join(tmp, "agent")}
	login := start(t, "login", agentEnv, filepath.Join(bin, "hermesh"), "auth", "login", setup.ConnectAddress, "--name", "bot")
	if _, err := mac.client.ApproveClientPairing(ctx, &localv1.ApproveClientPairingRequest{Code: login.pairingCode(t), Spec: &localv1.GrantSpec{}}); err != nil {
		t.Fatal(err)
	}
	if c := login.wait(t, 30*time.Second); c != 0 {
		t.Fatalf("login: %s", login.output())
	}
	r := run(t, agentEnv, time.Minute, filepath.Join(bin, "hermesh"), "session", "start", "--project", "demo", "--prompt", "say from the cli", "--wait", "-o", "json")
	other := firstSession(r.stdout).SessionID
	if r.code != 0 || other == "" {
		t.Fatalf("cli session: %+v", r)
	}
	if doc = callTool(t, session, "session_list", nil); !doc.OK || !strings.Contains(string(doc.raw), other) || !strings.Contains(string(doc.raw), id) {
		t.Fatalf("session_list sees every session: %s", doc.raw)
	}
	// Another client's session takes its message as a turn of its own; it
	// still can't interrupt or archive it.
	if doc = callTool(t, session, "message_send", map[string]any{"to": other, "message": "say from the assistant", "wait": true}); !doc.OK ||
		!strings.Contains(string(doc.raw), `"delivered":"turn"`) || !strings.Contains(string(doc.raw), "from the assistant") {
		t.Fatalf("writing to another client's session: %s", doc.raw)
	}
	if doc = callTool(t, session, "session_interrupt", map[string]any{"session_id": other}); doc.OK || doc.Error.Kind != "denied" {
		t.Fatalf("interrupting another client's session: %s", doc.raw)
	}

	// Refresh rotates; the old refresh token is honoured once as a retry,
	// and the tokens it gave before stop working.
	fresh := app.refresh(tokens.Refresh)
	if fresh.Access == tokens.Access || fresh.Refresh == tokens.Refresh {
		t.Fatal("refresh must rotate both tokens")
	}
	session2 := app.mcpSession(fresh.Access)
	if doc = callTool(t, session2, "auth_status", nil); !doc.OK {
		t.Fatalf("auth_status with refreshed token: %s", doc.raw)
	}

	// --- A second app, connected while the Mac is online ------------------
	app2 := newOAuthApp(t, base+"/mcp", "Second app")
	page2 := app2.connect(app2.authorize())
	if !strings.Contains(page2, "Approve on your Mac") {
		t.Fatalf("with the Mac online the page asks to approve there:\n%s", page2)
	}
	var req2 *localv1.PairingDetails
	eventually(t, 30*time.Second, "the second request on the Mac", func() bool {
		l, err := mac.client.ListPairingRequests(ctx, &localv1.ListPairingRequestsRequest{})
		if err != nil || len(l.Pairings) != 1 {
			return false
		}
		req2 = l.Pairings[0]
		return true
	})
	// No code to compare: the Mac shows none for a request confirmed here
	// (the page's script still watches it, to reload when it changes).
	if shown := regexp.MustCompile(`(?s)<script>.*?</script>`).ReplaceAllString(page2, ""); strings.Contains(shown, req2.Code) {
		t.Fatalf("the page shows the request's code (%s):\n%s", req2.Code, page2)
	}
	if _, err := mac.client.ApproveClientPairing(ctx, &localv1.ApproveClientPairingRequest{Code: req2.Code, Spec: &localv1.GrantSpec{}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "the page seeing the approval", func() bool { return app2.consentState() == "active" })
	tokens2 := app2.finish(app2.get(app2.consentURL))
	s2 := app2.mcpSession(tokens2.Access)
	if doc = callTool(t, s2, "project_list", nil); !doc.OK {
		t.Fatalf("second app: %s", doc.raw)
	}
	// Its code used again is refused, and what the code gave is revoked.
	if status, body := app2.tokenRequest(url.Values{"grant_type": {"authorization_code"}, "code": {tokens2.code}, "redirect_uri": {app2.redirect},
		"client_id": {app2.clientID}, "code_verifier": {tokens2.verifier}}); status != 400 || !strings.Contains(body, "invalid_grant") {
		t.Fatalf("a code used twice: %d %s", status, body)
	}
	if _, err := s2.CallTool(ctx, &mcp.CallToolParams{Name: "project_list"}); err == nil {
		t.Fatal("the tokens of a replayed code must stop working")
	}
	if status, body := app2.tokenRequest(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tokens2.Refresh}, "client_id": {app2.clientID}}); status != 400 {
		t.Fatalf("refresh with a replayed code's token: %d %s", status, body)
	}

	// It disconnects (RFC 7009): its grant is revoked on the relay too.
	if status := app2.revoke(tokens2.Refresh); status != 200 {
		t.Fatalf("revoke: %d", status)
	}
	if _, err := s2.CallTool(ctx, &mcp.CallToolParams{Name: "project_list"}); err == nil {
		t.Fatal("tokens of a disconnected app must stop working")
	}
	eventually(t, 30*time.Second, "the second grant gone from the owner's list", func() bool {
		l, err := mac.client.ListClients(ctx, &localv1.ListClientsRequest{})
		if err != nil {
			return false
		}
		for _, c := range l.Clients {
			if c.Label == "Second app (127.0.0.1)" && !c.Revoked {
				return false
			}
		}
		return true
	})

	// --- The owner revokes the first connection ---------------------------
	if _, err := mac.client.RevokeClient(ctx, &localv1.RevokeClientRequest{GrantId: mcpClient.GrantId}); err != nil {
		t.Fatal(err)
	}
	// The next call learns it, and the connection ends: its tokens go.
	doc = callTool(t, session2, "project_list", nil)
	if doc.OK || doc.Error.Kind != "revoked" || !strings.Contains(doc.Error.Hint, "connect the app to Hermesh again") {
		t.Fatalf("a call after the owner revoked the connection: %s", doc.raw)
	}
	if _, err := session2.CallTool(ctx, &mcp.CallToolParams{Name: "project_list"}); err == nil {
		t.Fatal("the tokens of a revoked connection must stop working")
	}
	if status, _ := app.tokenRequest(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {fresh.Refresh}, "client_id": {app.clientID}}); status != 400 {
		t.Fatalf("refresh after the owner revoked the connection: %d", status)
	}
}

// cliToolNames are the CLI commands the gateway serves as tools.
func cliToolNames() []string {
	var out []string
	for _, t := range cli.Tools() {
		out = append(out, t.Name)
	}
	return out
}

type cliDoc struct {
	OK bool `json:"ok"`
	// Note: what to do next, on a list while the connection waits.
	Note  string `json:"note"`
	Error *struct {
		Kind    string `json:"kind"`
		Message string `json:"message"`
		Hint    string `json:"hint"`
	} `json:"error"`
	raw []byte
}

func toolDoc(t *testing.T, res *mcp.CallToolResult) cliDoc {
	t.Helper()
	b, _ := json.Marshal(res.StructuredContent)
	var d cliDoc
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("tool result %s: %v", b, err)
	}
	d.raw = b
	if d.OK == res.IsError {
		t.Fatalf("isError %v but ok %v: %s", res.IsError, d.OK, b)
	}
	return d
}

func callTool(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) cliDoc {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return toolDoc(t, res)
}

type projectList struct {
	Projects []struct {
		Project string `json:"project"`
	} `json:"projects"`
	Offline []struct {
		Machine string `json:"machine"`
	} `json:"offline"`
}

func projectsOf(d cliDoc) projectList {
	var l projectList
	json.Unmarshal(d.raw, &l)
	return l
}

type mcpStatus struct {
	State string `json:"state"`
	Next  string `json:"next"`
}

func statusOf(t *testing.T, s *mcp.ClientSession) mcpStatus {
	t.Helper()
	d := callTool(t, s, "hermesh_status", nil)
	var v struct {
		Status mcpStatus `json:"status"`
	}
	json.Unmarshal(d.raw, &v)
	return v.Status
}

// oauthApp plays an agent product: it finds the authorization server from
// the MCP endpoint's 401, registers, and runs the authorization code flow
// with PKCE through the consent page, as a browser would.
type oauthApp struct {
	t                    *testing.T
	resource, name       string
	issuer, clientID     string
	redirect             string
	hc                   *http.Client // the "browser", with its cookies
	authorizeEP, tokenEP string
	revokeEP             string
	verifier, state      string
	consentURL           string
}

type oauthTokens struct {
	Access, Refresh string
	code, verifier  string
}

func newOAuthApp(t *testing.T, resource, name string) *oauthApp {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	a := &oauthApp{t: t, resource: resource, name: name, redirect: fmt.Sprintf("http://127.0.0.1:%d/callback", freePort(t)),
		hc: &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if strings.HasSuffix(req.URL.Path, "/callback") {
				return http.ErrUseLastResponse // the app's own listener
			}
			return nil
		}}}
	// 401 → protected resource metadata → authorization server metadata.
	res, err := http.Post(resource, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	m := regexp.MustCompile(`resource_metadata="([^"]+)"`).FindStringSubmatch(res.Header.Get("WWW-Authenticate"))
	if res.StatusCode != 401 || m == nil {
		t.Fatalf("unauthenticated MCP request: %d %q", res.StatusCode, res.Header.Get("WWW-Authenticate"))
	}
	var prm struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	getJSON(t, m[1], &prm)
	if prm.Resource != resource || len(prm.AuthorizationServers) != 1 {
		t.Fatalf("protected resource metadata %+v", prm)
	}
	a.issuer = prm.AuthorizationServers[0]
	var as struct {
		Issuer        string   `json:"issuer"`
		Authorization string   `json:"authorization_endpoint"`
		Token         string   `json:"token_endpoint"`
		Registration  string   `json:"registration_endpoint"`
		Revocation    string   `json:"revocation_endpoint"`
		PKCE          []string `json:"code_challenge_methods_supported"`
		CIMD          bool     `json:"client_id_metadata_document_supported"`
		ISS           bool     `json:"authorization_response_iss_parameter_supported"`
	}
	getJSON(t, a.issuer+"/.well-known/oauth-authorization-server", &as)
	if as.Issuer != a.issuer || !slices.Contains(as.PKCE, "S256") || !as.CIMD || !as.ISS {
		t.Fatalf("authorization server metadata %+v", as)
	}
	a.authorizeEP, a.tokenEP, a.revokeEP = as.Authorization, as.Token, as.Revocation
	body, _ := json.Marshal(map[string]any{"client_name": name, "redirect_uris": []string{"http://127.0.0.1/callback"}, "token_endpoint_auth_method": "none",
		"grant_types": []string{"authorization_code", "refresh_token"}, "application_type": "native"})
	res, err = http.Post(as.Registration, "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	var reg struct {
		ClientID string `json:"client_id"`
	}
	json.NewDecoder(res.Body).Decode(&reg)
	res.Body.Close()
	if res.StatusCode != 201 || reg.ClientID == "" {
		t.Fatalf("registration: %d", res.StatusCode)
	}
	a.clientID = reg.ClientID
	return a
}

func getJSON(t *testing.T, u string, v any) {
	t.Helper()
	res, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("GET %s: %d", u, res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(v); err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
}

// authorize opens the authorization URL (port picked per login, as Codex
// and Claude Code do) and returns the consent page.
func (a *oauthApp) authorize() string {
	b := make([]byte, 32)
	rand.Read(b)
	a.verifier = base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(a.verifier))
	a.state = base64.RawURLEncoding.EncodeToString(b[:8])
	q := url.Values{"response_type": {"code"}, "client_id": {a.clientID}, "redirect_uri": {a.redirect}, "state": {a.state},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(h[:])}, "code_challenge_method": {"S256"}, "resource": {a.resource}}
	return a.get(a.authorizeEP + "?" + q.Encode())
}

func (a *oauthApp) get(u string) string {
	a.t.Helper()
	res, err := a.hc.Get(u)
	if err != nil {
		a.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	a.consentURL = res.Request.URL.String()
	return string(b)
}

var csrfRE = regexp.MustCompile(`name="csrf" value="([^"]+)"`)
var idRE = regexp.MustCompile(`name="id" value="([^"]+)"`)

// connect clicks Connect on the consent page and returns the next page.
func (a *oauthApp) connect(page string) string {
	a.t.Helper()
	csrf, id := csrfRE.FindStringSubmatch(page), idRE.FindStringSubmatch(page)
	if csrf == nil || id == nil {
		a.t.Fatalf("no consent form:\n%s", page)
	}
	req, _ := http.NewRequest(http.MethodPost, a.issuer+"/oauth/consent", strings.NewReader(url.Values{"id": {id[1]}, "csrf": {csrf[1]}, "action": {"connect"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", a.issuer)
	res, err := a.hc.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	a.consentURL = res.Request.URL.String()
	return string(b)
}

// consentState is what the waiting page polls.
func (a *oauthApp) consentState() string {
	u, _ := url.Parse(a.consentURL)
	res, err := a.hc.Get(a.issuer + "/oauth/consent/status?id=" + url.QueryEscape(u.Query().Get("id")))
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	var v struct{ State string }
	json.NewDecoder(res.Body).Decode(&v)
	return v.State
}

var continueRE = regexp.MustCompile(`href="(/oauth/consent/continue\?id=[^"]+)"`)

// finish follows the page's link back to the app and exchanges the code.
func (a *oauthApp) finish(page string) oauthTokens {
	a.t.Helper()
	m := continueRE.FindStringSubmatch(page)
	if m == nil {
		a.t.Fatalf("no way back to the app:\n%s", page)
	}
	res, err := a.hc.Get(a.issuer + strings.ReplaceAll(m[1], "&amp;", "&"))
	if err != nil {
		a.t.Fatal(err)
	}
	res.Body.Close()
	loc, _ := url.Parse(res.Header.Get("Location"))
	if res.StatusCode != http.StatusFound || loc == nil || loc.Query().Get("code") == "" {
		a.t.Fatalf("back to the app: %d %v", res.StatusCode, loc)
	}
	if loc.Query().Get("state") != a.state || loc.Query().Get("iss") != a.issuer {
		a.t.Fatalf("the redirect must carry state and iss: %v", loc)
	}
	if !strings.HasPrefix(loc.String(), a.redirect) {
		a.t.Fatalf("redirected to %v, not %s", loc, a.redirect)
	}
	code := loc.Query().Get("code")
	status, body := a.tokenRequest(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {a.redirect},
		"client_id": {a.clientID}, "code_verifier": {a.verifier}, "resource": {a.resource}})
	tok := a.parseTokens(status, body)
	tok.code, tok.verifier = code, a.verifier
	return tok
}

func (a *oauthApp) tokenRequest(form url.Values) (int, string) {
	res, err := http.PostForm(a.tokenEP, form)
	if err != nil {
		a.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func (a *oauthApp) parseTokens(status int, body string) oauthTokens {
	a.t.Helper()
	var v struct {
		Access    string `json:"access_token"`
		Refresh   string `json:"refresh_token"`
		TokenType string `json:"token_type"`
		ExpiresIn int    `json:"expires_in"`
	}
	if status != 200 || json.Unmarshal([]byte(body), &v) != nil || v.TokenType != "Bearer" || v.ExpiresIn <= 0 ||
		!strings.HasPrefix(v.Access, "hm_at_") || !strings.HasPrefix(v.Refresh, "hm_rt_") {
		a.t.Fatalf("token response %d %s", status, body)
	}
	return oauthTokens{Access: v.Access, Refresh: v.Refresh}
}

func (a *oauthApp) refresh(refreshToken string) oauthTokens {
	a.t.Helper()
	return a.parseTokens(a.tokenRequest(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}, "client_id": {a.clientID}, "resource": {a.resource}}))
}

func (a *oauthApp) revoke(token string) int {
	res, err := http.PostForm(a.revokeEP, url.Values{"token": {token}, "client_id": {a.clientID}})
	if err != nil {
		a.t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b *bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

// mcpSession connects an MCP client with an access token.
func (a *oauthApp) mcpSession(token string) *mcp.ClientSession {
	a.t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: a.name, Version: "1"}, nil)
	s, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: a.resource,
		HTTPClient: &http.Client{Transport: &bearer{token: token, next: http.DefaultTransport}}}, nil)
	if err != nil {
		a.t.Fatalf("mcp connect: %v", err)
	}
	a.t.Cleanup(func() { s.Close() })
	return s
}
