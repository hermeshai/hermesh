// Package connaddr parses connection addresses such as
// "relay.example.com/o_7k3m9x2q": a relay URL plus the owner fingerprint
// that agents and machines pin as their trust anchor.
package connaddr

import (
	"fmt"
	"net/url"
	"strings"
)

// ParseAny accepts a connection address or a bare hosted-center URL; the
// owner id is "" for the latter (it is learned, and checked with a
// verification code, while pairing).
func ParseAny(s string) (relayURL, ownerID string, err error) {
	if r, o, err := Parse(s); err == nil {
		return r, o, nil
	}
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", "", err
	}
	if u.Scheme != "https" && u.Scheme != "http" || u.Host == "" {
		return "", "", fmt.Errorf("not a center URL or connection address: %q", s)
	}
	u.RawQuery, u.Fragment = "", ""
	return strings.TrimSuffix(u.String(), "/"), "", nil
}

func Parse(s string) (relayURL, ownerID string, err error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", "", err
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", "", fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	path := strings.Trim(u.Path, "/")
	i := strings.LastIndex(path, "/")
	owner := path[i+1:]
	if !strings.HasPrefix(owner, "o_") || len(owner) != 26 {
		return "", "", fmt.Errorf("connection address must end with the owner id (o_…): %q", s)
	}
	u.Path = "/" + path[:max(i, 0)]
	u.RawQuery, u.Fragment = "", ""
	return strings.TrimSuffix(u.String(), "/"), owner, nil
}

// Format renders the address; https is implied.
func Format(relayURL, ownerID string) string {
	return strings.TrimPrefix(strings.TrimSuffix(relayURL, "/"), "https://") + "/" + ownerID
}
