package mcpgw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Client registration: Client ID Metadata Documents (an https URL as
// client_id, fetched by the gateway) and, for older agent products,
// Dynamic Client Registration (RFC 7591). Both make public clients only.

const (
	maxCIMDBytes = 16 << 10
	minCIMDCache = 5 * time.Minute
	maxCIMDCache = 24 * time.Hour
)

// isCIMD reports whether a client_id is a metadata document URL.
func isCIMD(clientID string) bool { return strings.HasPrefix(clientID, "https://") }

// clientMetadata is the part of a client's metadata the gateway reads.
type clientMetadata struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	ClientURI               string   `json:"client_uri"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
}

// client returns the client a client_id names, fetching its metadata
// document when it is one and the cached copy is stale. nil: unknown.
func (g *Gateway) client(ctx context.Context, clientID string) (*Client, error) {
	c, err := g.db.GetClient(ctx, clientID)
	if err != nil {
		return nil, err
	}
	if !isCIMD(clientID) || c != nil && g.cfg.Now().Before(c.CacheUntil) {
		return c, nil
	}
	fresh, err := g.fetchCIMD(ctx, clientID)
	if err != nil {
		if c != nil && g.cfg.Now().Before(c.CacheUntil.Add(maxCIMDCache)) {
			g.log.Warn("mcp: refreshing a client metadata document failed; using the cached one", "client", clientID, "err", err)
			return c, nil
		}
		return nil, err
	}
	if c != nil {
		fresh.CreatedAt, fresh.LastUsed = c.CreatedAt, c.LastUsed
	}
	if err := g.db.PutClient(ctx, fresh); err != nil {
		return nil, err
	}
	return fresh, nil
}

// fetchCIMD fetches and checks a Client ID Metadata Document.
func (g *Gateway) fetchCIMD(ctx context.Context, clientID string) (*Client, error) {
	u, err := url.Parse(clientID)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || u.Path == "" || u.Path == "/" ||
		u.Port() != "" && u.Port() != "443" && !g.allowAnyPort() {
		return nil, fmt.Errorf("client_id %q is not a valid metadata document URL", clientID)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, clientID, nil)
	req.Header.Set("Accept", "application/json")
	res, err := g.cfg.CIMDClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch the client's metadata document: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch the client's metadata document: HTTP %d", res.StatusCode)
	}
	if mt, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type")); mt != "application/json" && !strings.HasSuffix(mt, "+json") {
		return nil, fmt.Errorf("the client's metadata document is %q, not JSON", mt)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxCIMDBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxCIMDBytes {
		return nil, errors.New("the client's metadata document is too large")
	}
	var m clientMetadata
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("the client's metadata document: %w", err)
	}
	if m.ClientID != clientID {
		return nil, errors.New("the client's metadata document names another client_id")
	}
	if m.ClientName == "" || len(m.RedirectURIs) == 0 {
		return nil, errors.New("the client's metadata document lacks client_name or redirect_uris")
	}
	if m.TokenEndpointAuthMethod != "" && m.TokenEndpointAuthMethod != "none" {
		return nil, fmt.Errorf("client authentication %q is not supported; public clients only", m.TokenEndpointAuthMethod)
	}
	for _, r := range m.RedirectURIs {
		if err := checkRedirectURI(r); err != nil {
			return nil, err
		}
	}
	now := g.cfg.Now()
	return &Client{ID: clientID, Source: "cimd", Name: clip(m.ClientName, 80), URI: m.ClientURI, RedirectURIs: m.RedirectURIs,
		Verified: g.verifiedHost(u.Hostname()), CacheUntil: now.Add(cacheFor(res.Header)), CreatedAt: now}, nil
}

// allowAnyPort: a client other than the default one (tests) may fetch
// documents from any port.
func (g *Gateway) allowAnyPort() bool { return !isSafeTransport(g.cfg.CIMDClient) }

func (g *Gateway) verifiedHost(host string) bool {
	host = strings.ToLower(host)
	for _, v := range g.cfg.VerifiedClients {
		if host == v || strings.HasSuffix(host, "."+v) {
			return true
		}
	}
	return false
}

// cacheFor is how long a document may be reused, from its caching headers,
// within [5 min, 24 h].
func cacheFor(h http.Header) time.Duration {
	d := minCIMDCache
	for _, part := range strings.Split(h.Get("Cache-Control"), ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		if strings.EqualFold(k, "max-age") {
			if n, err := strconv.Atoi(v); err == nil {
				d = time.Duration(n) * time.Second
			}
		}
	}
	return min(max(d, minCIMDCache), maxCIMDCache)
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// checkRedirectURI accepts https URLs and http ones on a loopback host.
func checkRedirectURI(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Fragment != "" {
		return fmt.Errorf("redirect URI %q is not valid", raw)
	}
	switch {
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && isLoopback(u.Hostname()):
		return nil
	}
	return fmt.Errorf("redirect URI %q must be https, or http on a loopback address", raw)
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// redirectMatches matches a requested redirect URI against a registered
// one: exactly, except that a loopback URI may use any port (RFC 8252
// §7.3; Claude Code and Codex listen on a port picked per login).
func redirectMatches(registered, requested string) bool {
	if registered == requested {
		return true
	}
	r, err1 := url.Parse(registered)
	q, err2 := url.Parse(requested)
	if err1 != nil || err2 != nil || r.Scheme != "http" || q.Scheme != "http" || !isLoopback(r.Hostname()) {
		return false
	}
	return r.Hostname() == q.Hostname() && r.Path == q.Path && r.RawQuery == q.RawQuery
}

func (c *Client) allowsRedirect(uri string) bool {
	return slices.ContainsFunc(c.RedirectURIs, func(r string) bool { return redirectMatches(r, uri) })
}

// loopbackOnly: every redirect URI is on this computer (any program there
// could claim to be the client).
func (c *Client) loopbackOnly() bool {
	for _, r := range c.RedirectURIs {
		if u, err := url.Parse(r); err != nil || !isLoopback(u.Hostname()) {
			return false
		}
	}
	return true
}

// handleRegister is Dynamic Client Registration (RFC 7591), public clients
// only.
func (g *Gateway) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !g.allowRegistration(clientIP(r)) {
		oauthError(w, http.StatusTooManyRequests, "invalid_request", "too many registrations; try again later")
		return
	}
	var m clientMetadata
	if err := json.NewDecoder(io.LimitReader(r.Body, maxCIMDBytes)).Decode(&m); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "the body is not client metadata JSON")
		return
	}
	if m.TokenEndpointAuthMethod != "" && m.TokenEndpointAuthMethod != "none" {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "only public clients (token_endpoint_auth_method none) can register")
		return
	}
	if len(m.RedirectURIs) == 0 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris is required")
		return
	}
	for _, u := range m.RedirectURIs {
		if err := checkRedirectURI(u); err != nil {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", err.Error())
			return
		}
	}
	for _, gt := range m.GrantTypes {
		if gt != "authorization_code" && gt != "refresh_token" {
			oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "grant type "+gt+" is not supported")
			return
		}
	}
	name := clip(m.ClientName, 80)
	if name == "" {
		name = "Unnamed app"
	}
	now := g.cfg.Now()
	c := &Client{ID: "dcr_" + randomToken(16), Source: "dcr", Name: name, URI: m.ClientURI, RedirectURIs: m.RedirectURIs, CreatedAt: now}
	if err := g.db.PutClient(r.Context(), c); err != nil {
		g.log.Error("mcp: register client", "err", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id": c.ID, "client_id_issued_at": now.Unix(), "client_name": c.Name, "redirect_uris": c.RedirectURIs,
		"token_endpoint_auth_method": "none", "grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
	})
}

// allowRegistration: at most 10 registrations per address per hour.
func (g *Gateway) allowRegistration(ip string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.cfg.Now()
	var recent []time.Time
	for _, t := range g.dcr[ip] {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	if len(recent) >= 10 {
		g.dcr[ip] = recent
		return false
	}
	g.dcr[ip] = append(recent, now)
	return true
}

// ---------------------------------------------------------------------------
// Fetching documents without reaching private networks.

// safeHTTPClient refuses to connect to loopback, private, link-local,
// shared (CGNAT), multicast and unspecified addresses, checks the address
// it actually connects to (no DNS rebinding), and follows no redirects.
func safeHTTPClient() *http.Client {
	d := &net.Dialer{Timeout: 5 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip, err := netip.ParseAddr(host)
		if err != nil {
			return err
		}
		if !publicAddr(ip) {
			return fmt.Errorf("refusing to fetch from %s: not a public address", ip)
		}
		return nil
	}}
	return &http.Client{
		Transport:     &safeTransport{&http.Transport{DialContext: d.DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirects are not followed") },
		Timeout:       5 * time.Second,
	}
}

type safeTransport struct{ *http.Transport }

func isSafeTransport(c *http.Client) bool {
	_, ok := c.Transport.(*safeTransport)
	return ok
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !cgnat.Contains(ip)
}
