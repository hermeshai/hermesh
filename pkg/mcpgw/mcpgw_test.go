package mcpgw

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hermeshai/hermesh/internal/sqlitedb"
	"github.com/hermeshai/hermesh/pkg/relay"
)

// testAccounts: one account, signed in, with no owner yet.
type testAccounts struct{}

func (testAccounts) Browser(*http.Request) (Account, bool) {
	return Account{ID: "a1", Hint: "t***@example.com"}, true
}
func (testAccounts) SignInURL(next string) string { return "/signin?next=" + url.QueryEscape(next) }
func (testAccounts) Owner(context.Context, string) (string, error) {
	return "", nil
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type fixture struct {
	t     *testing.T
	g     *Gateway
	srv   *httptest.Server
	clock *clock
}

func newFixture(t *testing.T, mod func(*Config)) *fixture {
	t.Helper()
	dir := t.TempDir()
	r, err := relay.New(relay.Config{DataDir: filepath.Join(dir, "relay"), Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	db, err := sqlitedb.Open(filepath.Join(dir, "mcp.db"), []string{Schema(false)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	vault, _ := NewAESVault(make([]byte, 32))
	f := &fixture{t: t, clock: &clock{t: time.Now()}}
	mux := http.NewServeMux()
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	cfg := Config{Issuer: f.srv.URL, ResourceURL: f.srv.URL + "/mcp", Relay: r, Backend: &SQLBackend{DB: db}, Vault: vault,
		Accounts: testAccounts{}, Logger: slog.New(slog.DiscardHandler), Now: f.clock.now}
	if mod != nil {
		mod(&cfg)
	}
	f.g, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.g.Close)
	f.g.Mount(mux)
	return f
}

// register registers a public client by DCR.
func (f *fixture) register(redirect string) string {
	f.t.Helper()
	res, err := http.Post(f.srv.URL+"/oauth/register", "application/json", strings.NewReader(fmt.Sprintf(`{"client_name":"Test","redirect_uris":[%q]}`, redirect)))
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	var v struct {
		ClientID string `json:"client_id"`
	}
	json.NewDecoder(res.Body).Decode(&v)
	if res.StatusCode != 201 {
		f.t.Fatalf("register: %d", res.StatusCode)
	}
	return v.ClientID
}

type browser struct {
	*http.Client
	f *fixture
}

func (f *fixture) browser() *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{f: f, Client: &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, _ []*http.Request) error {
		if req.URL.Host != strings.TrimPrefix(f.srv.URL, "http://") {
			return http.ErrUseLastResponse
		}
		return nil
	}}}
}

const verifier = "a-code-verifier-that-is-long-enough-for-pkce-0123456789"

func challenge(v string) string {
	h := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func (f *fixture) authorizeURL(clientID, redirect string, extra url.Values) string {
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect}, "state": {"st"},
		"code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}, "resource": {f.srv.URL + "/mcp"}}
	for k, v := range extra {
		q[k] = v
	}
	return f.srv.URL + "/oauth/authorize?" + q.Encode()
}

func (b *browser) get(u string) (*http.Response, string) {
	b.f.t.Helper()
	res, err := b.Get(u)
	if err != nil {
		b.f.t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res, string(body)
}

// consentCode runs the consent page and returns the authorization code.
func (b *browser) consentCode(clientID, redirect string) string {
	b.f.t.Helper()
	_, page := b.get(b.f.authorizeURL(clientID, redirect, nil))
	csrf := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(page)
	id := regexp.MustCompile(`name="id" value="([^"]+)"`).FindStringSubmatch(page)
	if csrf == nil || id == nil {
		b.f.t.Fatalf("consent page:\n%s", page)
	}
	res, err := b.PostForm(b.f.srv.URL+"/oauth/consent", url.Values{"id": {id[1]}, "csrf": {csrf[1]}, "action": {"connect"}})
	if err != nil {
		b.f.t.Fatal(err)
	}
	res.Body.Close()
	res, _ = b.get(b.f.srv.URL + "/oauth/consent/continue?id=" + url.QueryEscape(id[1]))
	loc, _ := url.Parse(res.Header.Get("Location"))
	if loc == nil || loc.Query().Get("code") == "" {
		b.f.t.Fatalf("no code: %d %v", res.StatusCode, res.Header)
	}
	return loc.Query().Get("code")
}

func (f *fixture) token(form url.Values) (int, map[string]any) {
	f.t.Helper()
	res, err := http.PostForm(f.srv.URL+"/oauth/token", form)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	var v map[string]any
	json.NewDecoder(res.Body).Decode(&v)
	return res.StatusCode, v
}

func (f *fixture) mcpStatus(access string) int {
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestAuthorizeRefusesBadRequests(t *testing.T) {
	f := newFixture(t, nil)
	redirect := "http://127.0.0.1/callback"
	client := f.register(redirect)
	b := f.browser()

	// Before the client and redirect are known good: a page, never a redirect.
	for name, u := range map[string]string{
		"unknown client":     f.authorizeURL("dcr_nope", redirect, nil),
		"foreign redirect":   f.authorizeURL(client, "https://evil.example/cb", nil),
		"no redirect":        f.authorizeURL(client, "", nil),
		"redirect path":      f.authorizeURL(client, "http://127.0.0.1/other", nil),
		"https for loopback": f.authorizeURL(client, "https://127.0.0.1/callback", nil),
	} {
		res, _ := b.get(u)
		if res.StatusCode != 400 || res.Header.Get("Location") != "" {
			t.Errorf("%s: %d %q", name, res.StatusCode, res.Header.Get("Location"))
		}
	}
	// A loopback redirect may use any port.
	if res, _ := b.get(f.authorizeURL(client, "http://127.0.0.1:53682/callback", nil)); res.StatusCode != 200 {
		t.Errorf("loopback port: %d", res.StatusCode)
	}
	// After: errors go back to the app, with state and iss.
	for name, c := range map[string]struct {
		extra url.Values
		code  string
	}{
		"plain PKCE":     {url.Values{"code_challenge_method": {"plain"}}, "invalid_request"},
		"no PKCE":        {url.Values{"code_challenge": {""}}, "invalid_request"},
		"token response": {url.Values{"response_type": {"token"}}, "unsupported_response_type"},
		"other resource": {url.Values{"resource": {"https://other.example/mcp"}}, "invalid_target"},
	} {
		res, _ := b.get(f.authorizeURL(client, redirect, c.extra))
		loc, _ := url.Parse(res.Header.Get("Location"))
		if res.StatusCode != 302 || loc.Query().Get("error") != c.code || loc.Query().Get("state") != "st" || loc.Query().Get("iss") != f.srv.URL {
			t.Errorf("%s: %d %v", name, res.StatusCode, loc)
		}
	}
	// The consent page works only in the browser that started it.
	res, _ := b.get(f.authorizeURL(client, redirect, nil))
	id := res.Request.URL.Query().Get("id")
	if res, _ := f.browser().get(f.srv.URL + "/oauth/consent?id=" + url.QueryEscape(id)); res.StatusCode != 400 {
		t.Errorf("consent page in another browser: %d", res.StatusCode)
	}
	// A form posted from another site is refused; "null" (a page with no
	// referrer, like the consent page) is not another site.
	csrf := f.g.csrfToken(id)
	post := func(origin string) int {
		req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/oauth/consent", strings.NewReader(url.Values{"id": {id}, "csrf": {csrf}, "action": {"connect"}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", origin)
		res, err := b.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if code := post("https://evil.example"); code != 403 {
		t.Errorf("consent from another site: %d", code)
	}
	if code := post("null"); code != 200 {
		t.Errorf("consent with Origin null: %d", code)
	}
	// A form posted without its CSRF token is refused.
	r2, err := b.PostForm(f.srv.URL+"/oauth/consent", url.Values{"id": {id}, "action": {"connect"}})
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != 400 {
		t.Errorf("consent without CSRF: %d", r2.StatusCode)
	}
}

// With the key in its address (a self-hosted relay), nothing served to
// whoever doesn't know it names it, and only an authorization naming it
// gets to the consent page.
func TestKeyedResource(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.ResourceURL, c.KeyedResource = c.ResourceURL+"/k3y", true })
	redirect := "http://127.0.0.1/callback"
	client := f.register(redirect)
	b := f.browser()
	if res, body := b.get(f.srv.URL + "/.well-known/oauth-protected-resource"); res.StatusCode != 404 || strings.Contains(body, "k3y") {
		t.Fatalf("metadata without the key: %d %s", res.StatusCode, body)
	}
	if res, body := b.get(f.srv.URL + "/.well-known/oauth-authorization-server"); res.StatusCode != 200 || strings.Contains(body, "k3y") {
		t.Fatalf("authorization server metadata: %d %s", res.StatusCode, body)
	}
	if res, body := b.get(f.srv.URL + "/.well-known/oauth-protected-resource/mcp/k3y"); res.StatusCode != 200 || !strings.Contains(body, f.srv.URL+"/mcp/k3y") {
		t.Fatalf("metadata with the key: %d %s", res.StatusCode, body)
	}
	for name, resource := range map[string][]string{"none": nil, "without the key": {f.srv.URL + "/mcp"}, "a wrong key": {f.srv.URL + "/mcp/k3z"}} {
		res, _ := b.get(f.authorizeURL(client, redirect, url.Values{"resource": resource}))
		loc, _ := url.Parse(res.Header.Get("Location"))
		if res.StatusCode != 302 || loc.Query().Get("error") != "invalid_target" || strings.Contains(loc.String(), "k3y") {
			t.Errorf("resource %s: %d %v", name, res.StatusCode, loc)
		}
	}
	if res, body := b.get(f.authorizeURL(client, redirect, url.Values{"resource": {f.srv.URL + "/mcp/k3y"}})); res.StatusCode != 200 || !strings.Contains(body, `name="csrf"`) {
		t.Fatalf("with the key: %d %s", res.StatusCode, body)
	}
}

func TestTokensRotateAndReplaysAreRevoked(t *testing.T) {
	f := newFixture(t, nil)
	redirect := "http://127.0.0.1/callback"
	client := f.register(redirect)
	code := f.browser().consentCode(client, redirect)
	exchange := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "client_id": {client}, "code_verifier": {verifier}}

	bad := url.Values{}
	for k, v := range exchange {
		bad[k] = v
	}
	bad.Set("code_verifier", verifier+"x")
	// A wrong verifier spends the code: it was already presented.
	if status, v := f.token(bad); status != 400 || v["error"] != "invalid_grant" {
		t.Fatalf("wrong verifier: %d %v", status, v)
	}
	code = f.browser().consentCode(client, redirect)
	exchange.Set("code", code)
	status, tok := f.token(exchange)
	if status != 200 || tok["token_type"] != "Bearer" {
		t.Fatalf("exchange: %d %v", status, tok)
	}
	access, refresh := tok["access_token"].(string), tok["refresh_token"].(string)
	if f.mcpStatus(access) != 200 {
		t.Fatal("access token refused")
	}
	if f.mcpStatus("hm_at_nope") != 401 {
		t.Fatal("unknown token accepted")
	}
	// The code again: refused, and what it gave is revoked.
	if status, v := f.token(exchange); status != 400 || v["error"] != "invalid_grant" {
		t.Fatalf("replayed code: %d %v", status, v)
	}
	if f.mcpStatus(access) != 401 {
		t.Fatal("tokens of a replayed code must be revoked")
	}

	// A fresh family: rotation, a retry honoured once, theft detected.
	code = f.browser().consentCode(client, redirect)
	exchange.Set("code", code)
	_, tok = f.token(exchange)
	r1 := tok["refresh_token"].(string)
	ref := func(rt string) (int, map[string]any) {
		return f.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {client}})
	}
	status, tok = ref(r1)
	if status != 200 || tok["refresh_token"] == r1 {
		t.Fatalf("refresh: %d %v", status, tok)
	}
	a2, r2 := tok["access_token"].(string), tok["refresh_token"].(string)
	// The client lost that answer and retries with r1 within the window.
	status, tok = ref(r1)
	if status != 200 {
		t.Fatalf("retry within the window: %d %v", status, tok)
	}
	a3, r3 := tok["access_token"].(string), tok["refresh_token"].(string)
	if f.mcpStatus(a2) != 401 || f.mcpStatus(a3) != 200 {
		t.Fatal("a retry revokes what the lost answer carried")
	}
	if status, _ := ref(r2); status != 400 {
		t.Fatal("the lost answer's refresh token must be revoked")
	}
	// r1 again after the window: stolen. The family ends.
	f.clock.add(2 * time.Minute)
	if status, v := ref(r1); status != 400 || v["error"] != "invalid_grant" {
		t.Fatalf("reuse after the window: %d %v", status, v)
	}
	if f.mcpStatus(a3) != 401 {
		t.Fatal("reuse must revoke the whole family")
	}
	if status, _ := ref(r3); status != 400 {
		t.Fatal("reuse must revoke the family's refresh token")
	}
	_ = refresh
	// Another client's refresh token is not this client's.
	if status, _ := f.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {"dcr_other"}}); status != 400 {
		t.Fatal("refresh by another client")
	}
}

// A connection waits for the owner's approval as long as the app keeps
// using it (no deadline), and ends once unused for a day.
func TestUnapprovedConnectionsWaitWhileUsed(t *testing.T) {
	f := newFixture(t, nil)
	redirect := "http://127.0.0.1/callback"
	client := f.register(redirect)
	code := f.browser().consentCode(client, redirect)
	_, tok := f.token(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "client_id": {client}, "code_verifier": {verifier}})
	conns, _ := f.g.db.ListConnections(context.Background(), ConnectionFilter{AccountID: "a1"})
	if len(conns) != 1 || conns[0].State != StateAwaitingSetup || conns[0].ClientName != "Test (127.0.0.1)" || !conns[0].Asked {
		t.Fatalf("connection: %+v", conns)
	}
	refresh := func(rt string) (int, map[string]any) {
		return f.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {client}})
	}
	// Days later, still used: still waiting, still working.
	for range 3 {
		f.clock.add(20 * time.Hour)
		status, next := refresh(tok["refresh_token"].(string))
		if status != 200 {
			t.Fatalf("refresh after 20 hours: %d %v", status, next)
		}
		tok = next
		f.g.advanceWaiting(context.Background())
		if c, _ := f.g.db.GetConnection(context.Background(), conns[0].ID); c.State != StateAwaitingSetup {
			t.Fatalf("a connection in use ended: %+v", c)
		}
	}
	// Then left alone: it ends with its tokens.
	f.clock.add(waitingIdle + time.Minute)
	f.g.advanceWaiting(context.Background())
	c, _ := f.g.db.GetConnection(context.Background(), conns[0].ID)
	if c.State != StateExpired || c.SealedKey != nil {
		t.Fatalf("unused for a day: %+v", c)
	}
	if status, _ := refresh(tok["refresh_token"].(string)); status != 400 {
		t.Fatal("an ended connection's tokens must stop working")
	}
}

// Anyone who can reach a self-hosted relay can connect: connections
// waiting for approval make room for newer ones, the oldest first, rather
// than keep the owner's own apps out.
func TestWaitingConnectionsMakeRoom(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	redirect := "http://127.0.0.1/callback"
	var first string
	for i := range maxWaiting {
		f.browser().consentCode(f.register(redirect), redirect)
		if i == 0 {
			conns, _ := f.g.db.ListConnections(ctx, ConnectionFilter{AccountID: "a1"})
			first = conns[0].ID
		}
		f.clock.add(time.Minute)
	}
	// The owner's own app, an hour later (registrations are limited per
	// address and hour).
	f.clock.add(time.Hour)
	f.browser().consentCode(f.register(redirect), redirect)
	waiting, _ := f.g.db.ListConnections(ctx, ConnectionFilter{AccountID: "a1", States: []string{StateAwaitingSetup, StateAwaitingApproval}})
	if len(waiting) != maxWaiting {
		t.Fatalf("%d connections waiting, want %d", len(waiting), maxWaiting)
	}
	if c, _ := f.g.db.GetConnection(ctx, first); c.State != StateRevoked || c.SealedKey != nil {
		t.Fatalf("the oldest waiting connection should have made room: %+v", c)
	}
}

func TestClientMetadataDocuments(t *testing.T) {
	docs := map[string]string{}
	var mu sync.Mutex
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		body, ok := docs[r.URL.Path]
		mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=60")
		io.WriteString(w, body)
	}))
	defer srv.Close()
	put := func(path, body string) string {
		mu.Lock()
		docs[path] = strings.ReplaceAll(body, "$ID", srv.URL+path)
		mu.Unlock()
		return srv.URL + path
	}
	f := newFixture(t, func(c *Config) { c.CIMDClient = srv.Client(); c.VerifiedClients = []string{"127.0.0.1"} })
	redirect := "https://app.example/callback"
	good := put("/client.json", `{"client_id":"$ID","client_name":"Good App","redirect_uris":["https://app.example/callback"],"token_endpoint_auth_method":"none"}`)
	b := f.browser()
	res, page := b.get(f.authorizeURL(good, redirect, nil))
	if res.StatusCode != 200 || !strings.Contains(page, "Connect Good App to Hermesh") || !strings.Contains(page, "Verified app") {
		t.Fatalf("CIMD client: %d\n%s", res.StatusCode, page)
	}
	for name, body := range map[string]string{
		"other client_id":   `{"client_id":"https://elsewhere.example/c.json","client_name":"X","redirect_uris":["https://app.example/callback"]}`,
		"confidential":      `{"client_id":"$ID","client_name":"X","redirect_uris":["https://app.example/callback"],"token_endpoint_auth_method":"client_secret_basic"}`,
		"no redirect_uris":  `{"client_id":"$ID","client_name":"X"}`,
		"http redirect":     `{"client_id":"$ID","client_name":"X","redirect_uris":["http://app.example/callback"]}`,
		"too large":         `{"client_id":"$ID","client_name":"` + strings.Repeat("x", maxCIMDBytes) + `","redirect_uris":["https://app.example/callback"]}`,
		"not a json object": `[]`,
	} {
		u := put("/"+strings.ReplaceAll(name, " ", "-")+".json", body)
		if res, _ := b.get(f.authorizeURL(u, redirect, nil)); res.StatusCode != 400 {
			t.Errorf("%s: %d", name, res.StatusCode)
		}
	}
	// Unverified hosts get the warning.
	f2 := newFixture(t, func(c *Config) { c.CIMDClient = srv.Client(); c.VerifiedClients = []string{"claude.ai"} })
	if _, page := f2.browser().get(f2.authorizeURL(good, redirect, nil)); !strings.Contains(page, "could not verify this app") {
		t.Fatalf("unverified client:\n%s", page)
	}
}

func TestSafeClientRefusesPrivateAddresses(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "{}") }))
	defer srv.Close()
	_, err := safeHTTPClient().Get(srv.URL + "/client.json")
	if err == nil || !strings.Contains(err.Error(), "not a public address") {
		t.Fatalf("fetching from loopback: %v", err)
	}
	for addr, public := range map[string]bool{"8.8.8.8": true, "10.0.0.1": false, "192.168.1.1": false, "169.254.169.254": false, "100.64.0.1": false,
		"::1": false, "fd00::1": false, "2001:4860:4860::8888": true, "0.0.0.0": false, "::ffff:127.0.0.1": false} {
		ip, _ := netip.ParseAddr(addr)
		if publicAddr(ip) != public {
			t.Errorf("%s: public %v", addr, !public)
		}
	}
}

func TestVaultBindsKeysToRows(t *testing.T) {
	v, _ := NewAESVault(make([]byte, 32))
	sealed, err := v.Seal([]byte("secret"), []byte("row-1"))
	if err != nil {
		t.Fatal(err)
	}
	if p, err := v.Open(sealed, []byte("row-1")); err != nil || string(p) != "secret" {
		t.Fatalf("open: %q %v", p, err)
	}
	if _, err := v.Open(sealed, []byte("row-2")); err == nil {
		t.Fatal("a sealed key must not open for another row")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := v.Open(sealed, []byte("row-1")); err == nil {
		t.Fatal("a tampered key must not open")
	}
	other, _ := NewAESVault(append(make([]byte, 31), 1))
	s2, _ := v.Seal([]byte("secret"), nil)
	if _, err := other.Open(s2, nil); err == nil {
		t.Fatal("another master key must not open it")
	}
}

func TestRedirectMatching(t *testing.T) {
	for _, c := range []struct {
		reg, req string
		ok       bool
	}{
		{"https://claude.ai/api/mcp/auth_callback", "https://claude.ai/api/mcp/auth_callback", true},
		{"https://claude.ai/api/mcp/auth_callback", "https://claude.ai/api/mcp/auth_callback/", false},
		{"https://claude.ai/cb", "https://claude.ai:8443/cb", false},
		{"http://127.0.0.1/callback", "http://127.0.0.1:61234/callback", true},
		{"http://localhost/callback", "http://localhost:3118/callback", true},
		{"http://127.0.0.1/callback", "http://localhost:3118/callback", false},
		{"http://127.0.0.1/callback", "http://127.0.0.1:1/other", false},
	} {
		if redirectMatches(c.reg, c.req) != c.ok {
			t.Errorf("%s vs %s: want %v", c.reg, c.req, c.ok)
		}
	}
}

func TestLabel(t *testing.T) {
	if l := label("Claude", "https://claude.ai/api/mcp/auth_callback"); l != "Claude (claude.ai)" {
		t.Fatal(l)
	}
	if l := label("Codex", "http://127.0.0.1:1455/callback"); l != "Codex (127.0.0.1)" {
		t.Fatal(l)
	}
	if l := label(strings.Repeat("Long name ", 20), "https://app.example/cb"); len(l) > 64 || !strings.HasSuffix(l, "… (app.example)") {
		t.Fatalf("%q (%d)", l, len(l))
	}
}

func TestAccountManagesItsConnections(t *testing.T) {
	f := newFixture(t, nil)
	redirect := "http://127.0.0.1/callback"
	client := f.register(redirect)
	code := f.browser().consentCode(client, redirect)
	_, tok := f.token(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "client_id": {client}, "code_verifier": {verifier}})
	access := tok["access_token"].(string)
	ctx := context.Background()
	list, err := f.g.Connections(ctx, "a1")
	if err != nil || len(list) != 1 || list[0].Name != "Test" || list[0].State != StateAwaitingSetup || list[0].Verified {
		t.Fatalf("connections: %+v %v", list, err)
	}
	if others, _ := f.g.Connections(ctx, "a2"); len(others) != 0 {
		t.Fatalf("another account sees %+v", others)
	}
	id := list[0].ID
	if err := f.g.ResendApproval(ctx, "a1", id); err != nil {
		t.Fatalf("resend while waiting for setup: %v", err)
	}
	if err := f.g.Disconnect(ctx, "a2", id); err != ErrNoConnection {
		t.Fatalf("another account disconnects: %v", err)
	}
	if err := f.g.Disconnect(ctx, "a1", id); err != nil {
		t.Fatal(err)
	}
	if f.mcpStatus(access) != 401 {
		t.Fatal("a disconnected connection's tokens must stop working")
	}
	if list, _ := f.g.Connections(ctx, "a1"); len(list) != 0 {
		t.Fatalf("ended connections are not listed: %+v", list)
	}
}

// HERMESH_MCP_TRACE logs what a connection's MCP requests carry besides
// the call, never its token, addresses or arguments.
func TestTraceLogsWhatCallersSend(t *testing.T) {
	t.Setenv("HERMESH_MCP_TRACE", "a1")
	var logs bytes.Buffer
	f := newFixture(t, func(c *Config) { c.Logger = slog.New(slog.NewTextHandler(&logs, nil)) })
	redirect := "http://127.0.0.1/callback"
	client := f.register(redirect)
	code := f.browser().consentCode(client, redirect)
	_, tok := f.token(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "client_id": {client}, "code_verifier": {verifier}})
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"grok-bot","version":"1.2"},"_meta":{"bot":"tech-demos"}}}`
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok["access_token"].(string))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.Header.Set("X-Bot-Id", "bot_42")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"hermesh_status","arguments":{"secret":"do-not-log"}}}`
	req, _ = http.NewRequest(http.MethodPost, f.srv.URL+"/mcp", strings.NewReader(call))
	req.Header = res.Request.Header.Clone()
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	out := logs.String()
	for _, want := range []string{"mcp trace", "grok-bot", "tech-demos", "X-Bot-Id", "bot_42", "hermesh_status", "argumentNames"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the trace lacks %q: %s", want, out)
		}
	}
	for _, never := range []string{tok["access_token"].(string), "203.0.113.9", "do-not-log"} {
		if strings.Contains(out, never) {
			t.Fatalf("the trace logged %q: %s", never, out)
		}
	}
}

// An owner the server forgot (it started over, or erased everything): its
// connections end, and their inboxes and its doorbells (the bots' webhook
// keys) go; another owner's stay.
func TestOwnerGoneForgetsConnectionsAndDoorbells(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	now := f.clock.now()
	for _, c := range []*Connection{{ID: "mc_1", AccountID: "a1", OwnerID: "o_x", State: StateActive, CreatedAt: now},
		{ID: "mc_2", AccountID: "a2", OwnerID: "o_y", State: StateActive, CreatedAt: now}} {
		if err := f.g.db.PutConnection(ctx, c); err != nil {
			t.Fatal(err)
		}
		if err := f.g.inboxes.PutInbox(ctx, &Inbox{ConnectionID: c.ID, SealedKey: []byte("k"), UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := f.g.doorbells.PutDoorbell(ctx, &Doorbell{OwnerID: c.OwnerID, Inbox: "c_bot", URL: "https://bot.example/hook", SealedSecret: []byte("s"), UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	f.g.OwnerGone(ctx, "o_x")
	if c, _ := f.g.db.GetConnection(ctx, "mc_1"); c == nil || c.Live() {
		t.Fatalf("the forgotten owner's connection: %+v", c)
	}
	if in, _ := f.g.inboxes.GetInbox(ctx, "mc_1"); in != nil {
		t.Fatal("its inbox is kept")
	}
	if d, _ := f.g.doorbells.ListDoorbells(ctx, "o_x", ""); len(d) != 0 {
		t.Fatalf("its doorbells are kept: %+v", d)
	}
	if c, _ := f.g.db.GetConnection(ctx, "mc_2"); c == nil || !c.Live() {
		t.Fatalf("another owner's connection: %+v", c)
	}
	if in, _ := f.g.inboxes.GetInbox(ctx, "mc_2"); in == nil {
		t.Fatal("another owner's inbox went")
	}
	if d, _ := f.g.doorbells.ListDoorbells(ctx, "o_y", ""); len(d) != 1 {
		t.Fatalf("another owner's doorbells: %+v", d)
	}
}
