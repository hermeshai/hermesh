package connaddr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// OfficialCenter is the hosted Hermesh center, used when neither the command
// line nor HERMESH_CENTER names one.
const OfficialCenter = "https://hermesh.io"

// Center picks the center URL or connection address to pair with: the
// argument, else $HERMESH_CENTER, else the official center.
func Center(arg string) string {
	if arg != "" {
		return arg
	}
	if env := strings.TrimSpace(os.Getenv("HERMESH_CENTER")); env != "" {
		return env
	}
	return OfficialCenter
}

// DiscoveryPath is where a hosted center says where its devices connect
// (machines, agents and signers), which can differ from the console's own
// address: {"connect_url": "https://connect.example.com"}.
const DiscoveryPath = "/.well-known/hermesh.json"

// Discover returns the connect URL the center at base publishes, or base
// itself when it publishes none (a self-hosted relay). The center is not
// trusted with this any more than with routing: verification codes and the
// pinned owner id still decide who is trusted.
func Discover(ctx context.Context, hc *http.Client, base string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(base, "/")+DiscoveryPath, nil)
	if err != nil {
		return "", err
	}
	res, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("reach %s: %w", base, err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return base, nil
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s%s: %s", base, DiscoveryPath, res.Status)
	}
	var d struct {
		ConnectURL string `json:"connect_url"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&d); err != nil {
		return "", fmt.Errorf("%s%s: %w", base, DiscoveryPath, err)
	}
	if d.ConnectURL == "" {
		return base, nil
	}
	u, err := url.Parse(d.ConnectURL)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" ||
		!(u.Scheme == "https" || u.Scheme == "http" && strings.HasPrefix(base, "http://")) {
		return "", fmt.Errorf("%s%s: bad connect_url %q", base, DiscoveryPath, d.ConnectURL)
	}
	return u.Scheme + "://" + u.Host, nil
}
