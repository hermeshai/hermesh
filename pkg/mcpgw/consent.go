package mcpgw

import (
	"context"
	"crypto/hmac"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The consent page: who asks and what for, Connect or Cancel; then what
// is left to do (approve on the Mac, set Hermesh up), and on to the app.
// It always lets the connection be made: approval may come later.

// authorization returns the browser's authorization, checking that this
// browser started it.
func (g *Gateway) authorization(r *http.Request, id string) (*Authorization, string) {
	if id == "" {
		return nil, "missing"
	}
	if ck, err := r.Cookie(g.authzCookie()); err != nil || ck.Value != id {
		return nil, "browser"
	}
	a, err := g.db.GetAuthorization(r.Context(), hashSecret("authz", id))
	if err != nil || a == nil || g.cfg.Now().After(a.ExpiresAt) {
		return nil, "expired"
	}
	return a, ""
}

type consentData struct {
	T            map[string]string
	Lang         string
	ID           string
	CSRF         string
	App          string
	AppHost      string
	Verified     bool
	RedirectHost string
	LoopbackOnly bool
	Account      string
	Server       string
	OwnerReady   bool
	// Progress: after Connect.
	Progress    bool
	State       string
	Code        string
	SetupURL    string
	ContinueURL string
	Message     string
}

func (g *Gateway) handleConsentPage(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	a, why := g.authorization(r, id)
	if a == nil {
		g.errorPage(w, r, http.StatusBadRequest, map[string]string{
			"missing": "This page needs the link the app opened.",
			"browser": "Open the connection from the app again, in this browser.",
			"expired": "This connection request has expired. Connect from the app again.",
		}[why])
		return
	}
	acct, ok := g.cfg.Accounts.Browser(r)
	if !ok {
		http.Redirect(w, r, g.cfg.Accounts.SignInURL("/oauth/consent?id="+url.QueryEscape(id)), http.StatusFound)
		return
	}
	if a.AccountID != "" && a.AccountID != acct.ID {
		g.errorPage(w, r, http.StatusForbidden, "This connection request belongs to another account. Connect from the app again.")
		return
	}
	ctx := r.Context()
	c, err := g.client(ctx, a.ClientID)
	if err != nil || c == nil {
		g.errorPage(w, r, http.StatusBadRequest, "The app could not be identified. Connect from the app again.")
		return
	}
	d := g.pageData(r, id, c, a, acct)
	if a.ConnectionID != "" {
		conn, err := g.advance(ctx, a.ConnectionID)
		if err != nil || conn == nil {
			g.errorPage(w, r, http.StatusInternalServerError, "Something went wrong. Connect from the app again.")
			return
		}
		st := g.status(conn)
		d.Progress, d.State, d.Code, d.Message = true, conn.State, conn.PairingCode, st.Next
		d.ContinueURL = "/oauth/consent/continue?id=" + url.QueryEscape(id)
	}
	g.render(w, consentPage, d)
}

func (g *Gateway) pageData(r *http.Request, id string, c *Client, a *Authorization, acct Account) consentData {
	lang, t := strings_(r)
	d := consentData{T: t, Lang: lang, ID: id, CSRF: g.csrfToken(id), App: c.Name, Verified: c.Verified, LoopbackOnly: c.loopbackOnly(),
		Account: acct.Hint, Server: g.issuer.Host, OwnerReady: acct.OwnerID != "", SetupURL: g.cfg.SetupURL}
	if u, err := url.Parse(c.ID); err == nil && c.Source == "cimd" {
		d.AppHost = u.Hostname()
	}
	if u, err := url.Parse(a.RedirectURI); err == nil {
		d.RedirectHost = u.Host
	}
	return d
}

// handleConsentAction answers Connect, Cancel and Finish later.
func (g *Gateway) handleConsentAction(w http.ResponseWriter, r *http.Request) {
	// Browsers send "null" for a form on a page with no referrer (this one):
	// the CSRF token and the browser-bound id are what protect the form.
	if o := r.Header.Get("Origin"); o != "" && o != "null" && o != g.issuer.Scheme+"://"+g.issuer.Host {
		g.errorPage(w, r, http.StatusForbidden, "This request came from another site.")
		return
	}
	if err := r.ParseForm(); err != nil {
		g.errorPage(w, r, http.StatusBadRequest, "Bad request.")
		return
	}
	id := r.PostForm.Get("id")
	a, _ := g.authorization(r, id)
	if a == nil || !hmacEqual(r.PostForm.Get("csrf"), g.csrfToken(id)) {
		g.errorPage(w, r, http.StatusBadRequest, "This connection request has expired. Connect from the app again.")
		return
	}
	acct, ok := g.cfg.Accounts.Browser(r)
	if !ok || a.AccountID != "" && a.AccountID != acct.ID {
		g.errorPage(w, r, http.StatusForbidden, "Sign in again, then connect from the app again.")
		return
	}
	ctx := r.Context()
	switch r.PostForm.Get("action") {
	case "cancel":
		g.consume(ctx, a)
		redirectWith(w, r, a.RedirectURI, url.Values{"error": {"access_denied"}, "error_description": {"the person cancelled"},
			"state": {a.State}, "iss": {g.issuer.String()}})
		return
	case "connect":
		if a.ConnectionID == "" {
			c, err := g.client(ctx, a.ClientID)
			if err != nil || c == nil {
				g.errorPage(w, r, http.StatusBadRequest, "The app could not be identified. Connect from the app again.")
				return
			}
			conn, err := g.createConnection(ctx, a, acct, c, r.UserAgent())
			if err == errTooMany {
				g.errorPage(w, r, http.StatusConflict, "You have connected as many apps as Hermesh allows. Disconnect one in Hermesh, then connect again.")
				return
			}
			if err != nil {
				g.log.Error("mcp: create connection", "err", err)
				g.errorPage(w, r, http.StatusInternalServerError, "Something went wrong. Connect from the app again.")
				return
			}
			a.AccountID, a.ConnectionID = acct.ID, conn.ID
			if err := g.db.PutAuthorization(ctx, a); err != nil {
				g.errorPage(w, r, http.StatusInternalServerError, "Something went wrong. Connect from the app again.")
				return
			}
		}
	}
	http.Redirect(w, r, "/oauth/consent?id="+url.QueryEscape(id), http.StatusSeeOther)
}

// handleConsentStatus is what the page polls while it waits for the Mac.
func (g *Gateway) handleConsentStatus(w http.ResponseWriter, r *http.Request) {
	a, _ := g.authorization(r, r.URL.Query().Get("id"))
	if a == nil || a.ConnectionID == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"state": ""})
		return
	}
	c, err := g.advance(r.Context(), a.ConnectionID)
	if err != nil || c == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"state": ""})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": c.State, "code": c.PairingCode})
}

// handleConsentContinue sends the browser back to the app with a code.
func (g *Gateway) handleConsentContinue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a, _ := g.authorization(r, r.URL.Query().Get("id"))
	if a == nil || a.ConnectionID == "" {
		g.errorPage(w, r, http.StatusBadRequest, "This connection request has expired. Connect from the app again.")
		return
	}
	if acct, ok := g.cfg.Accounts.Browser(r); !ok || acct.ID != a.AccountID {
		g.errorPage(w, r, http.StatusForbidden, "Sign in again, then connect from the app again.")
		return
	}
	code := randomToken(32)
	now := g.cfg.Now()
	if err := g.db.PutCode(ctx, &Code{Hash: hashSecret("code", code), ConnectionID: a.ConnectionID, ClientID: a.ClientID, RedirectURI: a.RedirectURI,
		CodeChallenge: a.CodeChallenge, Resource: a.Resource, ExpiresAt: now.Add(codeTTL)}); err != nil {
		g.errorPage(w, r, http.StatusInternalServerError, "Something went wrong. Connect from the app again.")
		return
	}
	g.consume(ctx, a)
	http.SetCookie(w, &http.Cookie{Name: g.authzCookie(), Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: g.secureCookies()})
	redirectWith(w, r, a.RedirectURI, url.Values{"code": {code}, "state": {a.State}, "iss": {g.issuer.String()}})
}

// consume ends an authorization: it gives one code, or one refusal.
func (g *Gateway) consume(ctx context.Context, a *Authorization) {
	a.ExpiresAt = g.cfg.Now().Add(-time.Second)
	g.db.PutAuthorization(ctx, a)
}

func hmacEqual(a, b string) bool { return a != "" && hmac.Equal([]byte(a), []byte(b)) }

// errorPage shows a problem in the browser; it never redirects.
func (g *Gateway) errorPage(w http.ResponseWriter, r *http.Request, status int, msg string) {
	lang, t := strings_(r)
	if zh, ok := zhErrors[msg]; ok && lang == "zh" {
		msg = zh
	}
	w.WriteHeader(status)
	g.render(w, errorPageTmpl, consentData{T: t, Lang: lang, Message: msg})
}

func (g *Gateway) render(w http.ResponseWriter, t *template.Template, d consentData) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	if err := t.Execute(w, d); err != nil {
		g.log.Warn("mcp: render", "err", err)
	}
}

// strings_ picks the page's language from Accept-Language.
func strings_(r *http.Request) (string, map[string]string) {
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag := strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]))
		switch {
		case strings.HasPrefix(tag, "zh"):
			return "zh", zhText
		case strings.HasPrefix(tag, "en"):
			return "en", enText
		}
	}
	return "en", enText
}

var enText = map[string]string{
	"title":         "Connect %s to Hermesh",
	"verified":      "Verified app",
	"unverified":    "Hermesh could not verify this app. Connect only if you just started this from %s.",
	"returns":       "It will return to %s.",
	"loopback":      "This app returns to a program on your computer. Any program there could claim to be this app: connect only if you just started this yourself.",
	"account":       "Account",
	"server":        "Server",
	"can":           "Once you approve it on your Mac, it can:",
	"can1":          "See your machines, projects and sessions.",
	"can2":          "Start Claude Code or Codex sessions and talk to them. Those agents run with full permissions in the projects you choose.",
	"custodian":     "%s holds this connection's key: it can act as the app within what you approve, and sees the sessions the app reaches. To keep that on your own computers, run your own Hermesh server.",
	"nextApprove":   "Next, you approve it on your Mac with Touch ID.",
	"nextSetup":     "Hermesh isn't set up on a Mac yet. You can connect now and approve the app once it is.",
	"connect":       "Connect",
	"cancel":        "Cancel",
	"approveTitle":  "Approve on your Mac",
	"approveBody":   "Hermesh on your Mac is asking you to approve %s. Approve it there with Touch ID.",
	"waiting":       "Waiting for your Mac…",
	"later":         "Finish later",
	"connected":     "Connected",
	"connectedBody": "%s can now use Hermesh.",
	"oneMore":       "Connected. One more step",
	"setupBody":     "Install Hermesh for Mac, set it up with this account, and approve %s when it asks. The app starts working then; there is no need to reconnect.",
	"download":      "Get Hermesh for Mac",
	"offlineBody":   "Approve %s in Hermesh on your Mac the next time it is open. The app starts working then; there is no need to reconnect.",
	"continue":      "Continue to %s",
	"ended":         "This connection has ended",
	"errorTitle":    "Can't connect",
}

var zhText = map[string]string{
	"title":         "将 %s 连接到 Hermesh",
	"verified":      "已验证的应用",
	"unverified":    "Hermesh 无法验证这个应用。只有在你刚刚从 %s 发起连接时才继续。",
	"returns":       "完成后将返回 %s。",
	"loopback":      "这个应用会返回到你电脑上的某个程序，那里的任何程序都可能冒充它：只有在你刚刚亲自发起时才继续。",
	"account":       "账号",
	"server":        "服务器",
	"can":           "你在 Mac 上批准后，它可以：",
	"can1":          "查看你的机器、项目和会话。",
	"can2":          "启动 Claude Code 或 Codex 会话并与之对话。这些 agent 会在你选择的项目里以完全权限运行。",
	"custodian":     "%s 保管这个连接的密钥：它可以在你批准的范围内代表该应用操作，并能看到该应用触及的会话。若希望这些只留在你自己的电脑上，请使用自己的 Hermesh 服务器。",
	"nextApprove":   "下一步，在你的 Mac 上用 Touch ID 批准。",
	"nextSetup":     "你还没有在 Mac 上设置 Hermesh。现在可以先连接，设置好后再批准这个应用。",
	"connect":       "连接",
	"cancel":        "取消",
	"approveTitle":  "在 Mac 上批准",
	"approveBody":   "你 Mac 上的 Hermesh 正在请求你批准 %s，在那里用 Touch ID 批准即可。",
	"waiting":       "正在等待你的 Mac…",
	"later":         "稍后完成",
	"connected":     "已连接",
	"connectedBody": "%s 现在可以使用 Hermesh 了。",
	"oneMore":       "已连接，还差一步",
	"setupBody":     "安装 Hermesh for Mac，用这个账号完成设置，并在它询问时批准 %s。之后应用即可使用，无需重新连接。",
	"download":      "获取 Hermesh for Mac",
	"offlineBody":   "下次打开 Mac 上的 Hermesh 时批准 %s。之后应用即可使用，无需重新连接。",
	"continue":      "继续前往 %s",
	"ended":         "这个连接已结束",
	"errorTitle":    "无法连接",
}

var zhErrors = map[string]string{
	"This page needs the link the app opened.":                                                          "这个页面需要从应用打开的链接进入。",
	"Open the connection from the app again, in this browser.":                                          "请在这个浏览器里，从应用重新发起连接。",
	"This connection request has expired. Connect from the app again.":                                  "这个连接请求已过期，请从应用重新连接。",
	"This connection request belongs to another account. Connect from the app again.":                   "这个连接请求属于另一个账号，请从应用重新连接。",
	"The app could not be identified. Connect from the app again.":                                      "无法识别这个应用，请从应用重新连接。",
	"Something went wrong. Connect from the app again.":                                                 "出了点问题，请从应用重新连接。",
	"This request came from another site.":                                                              "这个请求来自其他网站。",
	"Sign in again, then connect from the app again.":                                                   "请重新登录，然后从应用重新连接。",
	"You have connected as many apps as Hermesh allows. Disconnect one in Hermesh, then connect again.": "已连接的应用数量已达上限。请先在 Hermesh 中断开一个，再重新连接。",
	"The app's request is missing client_id or redirect_uri.":                                           "应用的请求缺少 client_id 或 redirect_uri。",
	"The app is not registered here. Remove the connection in the app and add it again.":                "这个应用没有在此注册。请在应用里移除这个连接后重新添加。",
	"The app asked to return to an address it did not register.":                                        "应用要求返回到一个它没有登记的地址。",
	"Something went wrong. Try connecting again.":                                                       "出了点问题，请重新连接。",
}

var funcs = template.FuncMap{
	"f": func(format string, a ...any) string { return fmt.Sprintf(format, a...) },
}

const pageHead = `<!doctype html><html lang="{{.Lang}}"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1"><title>Hermesh</title>
<style>
:root{--ink:#1d1d1f;--muted:#6e6e73;--line:#e5e5ea;--bg:#f5f5f7;--card:#fff;--accent:#2f6fde;--warn:#b25000}
@media (prefers-color-scheme:dark){:root{--ink:#f5f5f7;--muted:#a1a1a6;--line:#38383a;--bg:#000;--card:#1c1c1e;--accent:#5b8ff0;--warn:#ff9f0a}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:15px/1.5 -apple-system,BlinkMacSystemFont,"Inter","PingFang SC",sans-serif}
main{max-width:440px;margin:8vh auto;padding:0 16px}.card{background:var(--card);border:1px solid var(--line);border-radius:14px;padding:28px}
h1{font-size:20px;font-weight:600;margin:0 0 8px}p{margin:8px 0}.muted{color:var(--muted);font-size:13px}.warn{color:var(--warn);font-size:13px}
ul{margin:8px 0 8px 18px;padding:0}li{margin:4px 0}.row{display:flex;gap:8px;margin-top:20px}
button,.btn{flex:1;font:inherit;font-weight:500;padding:9px 14px;border-radius:9px;border:1px solid var(--line);background:var(--card);color:var(--ink);cursor:pointer;text-align:center;text-decoration:none}
.primary{background:var(--accent);border-color:var(--accent);color:#fff}
.kv{display:flex;justify-content:space-between;border-top:1px solid var(--line);padding-top:10px;margin-top:14px;font-size:13px;color:var(--muted)}
</style></head><body><main><div class="card">`

const pageFoot = `</div></main></body></html>`

var consentPage = template.Must(template.New("consent").Funcs(funcs).Parse(pageHead + `
{{if not .Progress}}
<h1>{{f (index .T "title") .App}}</h1>
{{if .Verified}}<p class="muted">✓ {{index .T "verified"}}{{if .AppHost}} · {{.AppHost}}{{end}}</p>
{{else}}<p class="warn">{{f (index .T "unverified") .RedirectHost}}</p>{{end}}
<p class="muted">{{f (index .T "returns") .RedirectHost}}</p>
{{if .LoopbackOnly}}<p class="warn">{{index .T "loopback"}}</p>{{end}}
<p>{{index .T "can"}}</p>
<ul><li>{{index .T "can1"}}</li><li>{{index .T "can2"}}</li></ul>
<p class="muted">{{f (index .T "custodian") .Server}}</p>
<p class="muted">{{if .OwnerReady}}{{index .T "nextApprove"}}{{else}}{{index .T "nextSetup"}}{{end}}</p>
<form method="post" action="/oauth/consent"><input type="hidden" name="id" value="{{.ID}}"><input type="hidden" name="csrf" value="{{.CSRF}}">
<div class="row"><button name="action" value="cancel">{{index .T "cancel"}}</button><button class="primary" name="action" value="connect">{{index .T "connect"}}</button></div></form>
<div class="kv">{{if .Account}}<span>{{index .T "account"}}</span><span>{{.Account}}</span>{{else}}<span>{{index .T "server"}}</span><span>{{.Server}}</span>{{end}}</div>
{{else if eq .State "active"}}
<h1>{{index .T "connected"}}</h1><p>{{f (index .T "connectedBody") .App}}</p>
<div class="row"><a class="btn primary" id="go" href="{{.ContinueURL}}">{{f (index .T "continue") .App}}</a></div>
<script>setTimeout(function(){location.href=document.getElementById("go").href},800)</script>
{{else if and (eq .State "awaiting_approval") .Code}}
<h1>{{index .T "approveTitle"}}</h1><p>{{f (index .T "approveBody") .App}}</p>
<p class="muted">{{index .T "waiting"}}</p>
<div class="row"><a class="btn" href="{{.ContinueURL}}">{{index .T "later"}}</a></div>
{{else if eq .State "awaiting_approval"}}
<h1>{{index .T "oneMore"}}</h1><p>{{f (index .T "offlineBody") .App}}</p>
<div class="row"><a class="btn primary" href="{{.ContinueURL}}">{{f (index .T "continue") .App}}</a></div>
{{else if eq .State "awaiting_setup"}}
<h1>{{index .T "oneMore"}}</h1><p>{{f (index .T "setupBody") .App}}</p>
<div class="row"><a class="btn" href="{{.SetupURL}}" target="_blank" rel="noopener">{{index .T "download"}}</a><a class="btn primary" href="{{.ContinueURL}}">{{f (index .T "continue") .App}}</a></div>
{{else}}
<h1>{{index .T "ended"}}</h1><p>{{.Message}}</p>
{{end}}
{{if and .Progress (ne .State "active") (ne .State "revoked") (ne .State "expired")}}
<script>(function(){var s={{.State}},c={{.Code}};setInterval(function(){fetch("/oauth/consent/status?id="+encodeURIComponent({{.ID}}),{credentials:"same-origin"}).then(function(r){return r.json()}).then(function(j){if(j.state&&(j.state!==s||j.code!==c))location.reload()})},2000)})()</script>
{{end}}` + pageFoot))

var errorPageTmpl = template.Must(template.New("error").Funcs(funcs).Parse(pageHead + `
<h1>{{index .T "errorTitle"}}</h1><p>{{.Message}}</p>` + pageFoot))
