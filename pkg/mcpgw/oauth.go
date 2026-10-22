package mcpgw

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The authorization server: metadata (RFC 8414), the authorization
// endpoint, the token endpoint (code exchange and refresh with rotation),
// and revocation (RFC 7009). See docs/mcp-gateway.md §5.

func (g *Gateway) handleASMetadata(w http.ResponseWriter, _ *http.Request) {
	is := g.issuer.String()
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                         is,
		"authorization_endpoint":                         is + "/oauth/authorize",
		"token_endpoint":                                 is + "/oauth/token",
		"registration_endpoint":                          is + "/oauth/register",
		"revocation_endpoint":                            is + "/oauth/revoke",
		"response_types_supported":                       []string{"code"},
		"response_modes_supported":                       []string{"query"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"revocation_endpoint_auth_methods_supported":     []string{"none"},
		"client_id_metadata_document_supported":          true,
		"authorization_response_iss_parameter_supported": true,
	})
}

func (g *Gateway) handlePRM(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 g.cfg.ResourceURL,
		"authorization_servers":    []string{g.issuer.String()},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "Hermesh",
	})
}

// sameResource compares a resource parameter with ours: scheme and host
// case-insensitively, a trailing slash tolerated.
func (g *Gateway) sameResource(v string) bool {
	u, err := url.Parse(v)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Scheme, g.resource.Scheme) && strings.EqualFold(u.Host, g.resource.Host) &&
		strings.TrimSuffix(u.Path, "/") == strings.TrimSuffix(g.resource.Path, "/") && u.RawQuery == "" && u.Fragment == ""
}

// handleAuthorize starts an authorization: it checks the request, records
// it, and sends the browser to the consent page (through sign-in).
func (g *Gateway) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ctx := r.Context()
	// Until the client and its redirect URI are known good, errors are
	// shown here: never redirected anywhere.
	clientID, redirectURI := q.Get("client_id"), q.Get("redirect_uri")
	if clientID == "" || redirectURI == "" {
		g.errorPage(w, r, http.StatusBadRequest, "The app's request is missing client_id or redirect_uri.")
		return
	}
	c, err := g.client(ctx, clientID)
	if err != nil {
		g.errorPage(w, r, http.StatusBadRequest, "The app could not be identified: "+err.Error())
		return
	}
	if c == nil {
		g.errorPage(w, r, http.StatusBadRequest, "The app is not registered here. Remove the connection in the app and add it again.")
		return
	}
	if !c.allowsRedirect(redirectURI) {
		g.errorPage(w, r, http.StatusBadRequest, "The app asked to return to an address it did not register.")
		return
	}
	state := q.Get("state")
	fail := func(code, desc string) {
		redirectWith(w, r, redirectURI, url.Values{"error": {code}, "error_description": {desc}, "state": {state}, "iss": {g.issuer.String()}})
	}
	switch {
	case q.Get("response_type") != "code":
		fail("unsupported_response_type", "only response_type=code")
		return
	case q.Get("code_challenge_method") != "S256":
		fail("invalid_request", "PKCE with code_challenge_method=S256 is required")
		return
	case len(q.Get("code_challenge")) < 43 || len(q.Get("code_challenge")) > 128:
		fail("invalid_request", "code_challenge must be 43 to 128 characters")
		return
	case q.Get("resource") != "" && !g.sameResource(q.Get("resource")):
		fail("invalid_target", "resource must be "+g.cfg.ResourceURL)
		return
	}
	id := randomToken(16)
	now := g.cfg.Now()
	a := &Authorization{IDHash: hashSecret("authz", id), ClientID: c.ID, RedirectURI: redirectURI, State: state,
		CodeChallenge: q.Get("code_challenge"), Resource: g.cfg.ResourceURL, CreatedAt: now, ExpiresAt: now.Add(authorizationTTL)}
	if err := g.db.PutAuthorization(ctx, a); err != nil {
		g.log.Error("mcp: record authorization", "err", err)
		g.errorPage(w, r, http.StatusInternalServerError, "Something went wrong. Try connecting again.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: g.authzCookie(), Value: id, Path: "/", HttpOnly: true, Secure: g.secureCookies(),
		SameSite: http.SameSiteLaxMode, MaxAge: int(authorizationTTL / time.Second)})
	next := "/oauth/consent?id=" + url.QueryEscape(id)
	if _, ok := g.cfg.Accounts.Browser(r); !ok {
		http.Redirect(w, r, g.cfg.Accounts.SignInURL(next), http.StatusFound)
		return
	}
	http.Redirect(w, r, next, http.StatusFound)
}

func redirectWith(w http.ResponseWriter, r *http.Request, base string, v url.Values) {
	u, _ := url.Parse(base)
	q := u.Query()
	for k, vs := range v {
		if len(vs) > 0 && vs[0] != "" {
			q.Set(k, vs[0])
		}
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// handleToken exchanges codes and refresh tokens.
func (g *Gateway) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "the body must be application/x-www-form-urlencoded")
		return
	}
	if _, ok := r.PostForm["client_secret"]; ok {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "public clients only: no client_secret")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		g.exchangeCode(w, r)
	case "refresh_token":
		g.refresh(w, r)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "authorization_code or refresh_token")
	}
}

func (g *Gateway) exchangeCode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := r.PostForm
	now := g.cfg.Now()
	c, first, err := g.db.UseCode(ctx, hashSecret("code", f.Get("code")), now)
	if err != nil {
		g.log.Error("mcp: use code", "err", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	if c == nil {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "unknown or expired code")
		return
	}
	family := codeFamily(c.Hash)
	if !first {
		// A code used twice was stolen or replayed: what it gave is revoked.
		g.db.RevokeTokens(ctx, "", family, now)
		g.log.Warn("mcp: authorization code reused; its tokens are revoked", "connection", c.ConnectionID)
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the code was already used")
		return
	}
	switch {
	case now.After(c.ExpiresAt):
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the code expired")
		return
	case f.Get("client_id") != c.ClientID:
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the code was issued to another client")
		return
	case f.Get("redirect_uri") != c.RedirectURI:
		oauthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri does not match the authorization request")
		return
	case f.Get("resource") != "" && !g.sameResource(f.Get("resource")):
		oauthError(w, http.StatusBadRequest, "invalid_target", "resource must be "+g.cfg.ResourceURL)
		return
	case !pkceMatches(f.Get("code_verifier"), c.CodeChallenge):
		oauthError(w, http.StatusBadRequest, "invalid_grant", "code_verifier does not match the code_challenge")
		return
	}
	conn, err := g.db.GetConnection(ctx, c.ConnectionID)
	if err != nil || conn == nil || !conn.Live() {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the connection has ended")
		return
	}
	g.issueTokens(w, r, conn, c.ClientID, family)
}

// codeFamily names the tokens one code gave, so that a replayed code
// revokes them.
func codeFamily(codeHash []byte) string { return base64.RawURLEncoding.EncodeToString(codeHash[:12]) }

// pkceMatches checks an S256 code verifier.
func pkceMatches(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	h := sha256.Sum256([]byte(verifier))
	return subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(h[:])), []byte(challenge)) == 1
}

func (g *Gateway) refresh(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := r.PostForm
	now := g.cfg.Now()
	t, err := g.db.GetToken(ctx, hashSecret("token", f.Get("refresh_token")))
	if err != nil {
		g.log.Error("mcp: refresh", "err", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	if t != nil && t.Kind == KindRefresh && !t.RevokedAt.IsZero() && !t.RotatedAt.IsZero() {
		// A rotated token that is presented again after its retry: stolen.
		g.db.RevokeTokens(ctx, "", t.FamilyID, now)
		g.log.Warn("mcp: refresh token reused; its family is revoked", "connection", t.ConnectionID)
	}
	if t == nil || t.Kind != KindRefresh || !t.RevokedAt.IsZero() || now.After(t.ExpiresAt) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "unknown, expired or revoked refresh token")
		return
	}
	if f.Get("client_id") != t.ClientID {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the refresh token was issued to another client")
		return
	}
	if f.Get("resource") != "" && !g.sameResource(f.Get("resource")) {
		oauthError(w, http.StatusBadRequest, "invalid_target", "resource must be "+g.cfg.ResourceURL)
		return
	}
	conn, err := g.db.GetConnection(ctx, t.ConnectionID)
	if err != nil || conn == nil || !conn.Live() {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the connection has ended; connect again")
		return
	}
	rotated, err := g.db.RotateToken(ctx, t.Hash, now)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	if !rotated {
		// Used before. Right after its rotation it is a client retrying a
		// response it lost: honoured once more, and what the lost response
		// carried no longer works. Later, it was stolen: the family ends.
		if now.Sub(t.RotatedAt) > refreshRetryWindow {
			g.db.RevokeTokens(ctx, "", t.FamilyID, now)
			g.log.Warn("mcp: refresh token reused; its family is revoked", "connection", t.ConnectionID)
			oauthError(w, http.StatusBadRequest, "invalid_grant", "the refresh token was already used")
			return
		}
		g.db.RevokeTokens(ctx, "", t.FamilyID, now)
	}
	g.issueTokens(w, r, conn, t.ClientID, t.FamilyID)
}

// issueTokens answers a token request with a new access and refresh token.
func (g *Gateway) issueTokens(w http.ResponseWriter, r *http.Request, conn *Connection, clientID, family string) {
	ctx := r.Context()
	now := g.cfg.Now()
	accessExp, refreshExp := now.Add(accessTTL), now.Add(refreshIdle)
	if exp, ok := g.grantExpiry(conn); ok && conn.State == StateActive {
		accessExp, refreshExp = minTime(accessExp, exp), minTime(refreshExp, exp)
	}
	access, refresh := "hm_at_"+randomToken(32), "hm_rt_"+randomToken(32)
	for _, t := range []*Token{
		{Hash: hashSecret("token", access), Kind: KindAccess, ConnectionID: conn.ID, ClientID: clientID, FamilyID: family, CreatedAt: now, ExpiresAt: accessExp},
		{Hash: hashSecret("token", refresh), Kind: KindRefresh, ConnectionID: conn.ID, ClientID: clientID, FamilyID: family, CreatedAt: now, ExpiresAt: refreshExp},
	} {
		if err := g.db.PutToken(ctx, t); err != nil {
			g.log.Error("mcp: store token", "err", err)
			oauthError(w, http.StatusInternalServerError, "server_error", "")
			return
		}
	}
	// The app is still there: a connection waiting for approval lives on.
	g.touch(ctx, conn)
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(accessExp.Sub(now) / time.Second),
		"refresh_token": refresh,
	})
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// handleRevoke is RFC 7009. Revoking a refresh token ends the connection
// (the agent product disconnected); revoking an access token revokes it.
func (g *Gateway) handleRevoke(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "the body must be application/x-www-form-urlencoded")
		return
	}
	ctx := r.Context()
	t, err := g.db.GetToken(ctx, hashSecret("token", r.PostForm.Get("token")))
	if err == nil && t != nil && t.ClientID == r.PostForm.Get("client_id") {
		now := g.cfg.Now()
		if t.Kind == KindRefresh {
			g.endConnection(ctx, t.ConnectionID, StateRevoked, "disconnected by the app")
		} else {
			g.db.RevokeTokens(ctx, "", t.FamilyID, now)
		}
	}
	// Unknown tokens get the same answer (RFC 7009 §2.2).
	w.WriteHeader(http.StatusOK)
}

// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	e.Encode(v)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(b.Bytes())
}

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	body := map[string]string{"error": code}
	if desc != "" {
		body["error_description"] = desc
	}
	writeJSON(w, status, body)
}
