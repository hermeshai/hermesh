package e2e

import (
	"context"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1/relayv1connect"
	"github.com/hermeshai/hermesh/internal/connaddr"
)

type deletionRelayInfo struct {
	relayv1connect.UnimplementedRelayPublicServiceHandler
	relay   relayv1connect.RelayPublicServiceClient
	console string
}

func (s deletionRelayInfo) GetInfo(ctx context.Context, req *relayv1.GetInfoRequest) (*relayv1.GetInfoResponse, error) {
	info, err := s.relay.GetInfo(ctx, req)
	if err == nil {
		info.Hosted, info.ConsoleUrl = true, s.console
	}
	return info, err
}

// Keep pairing, admission and foreground reconnect on the real test relay.
// Only its capability metadata and the hosted console page are fixtures.
func iphoneHostedDeletion(t *testing.T, w *ownerMac, device string) string {
	t.Helper()
	console := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet || req.URL.RequestURI() != "/account/delete" || req.Header.Get("Authorization") != "" {
			http.NotFound(rw, req)
			return
		}
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		rw.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(rw, "<h1>Fixture: Account deletion is unavailable</h1>")
	}))
	t.Cleanup(console.Close)
	cert := filepath.Join(t.TempDir(), "console.pem")
	err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: console.Certificate().Raw}), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if r := run(t, nil, time.Minute, "xcrun", "simctl", "keychain", device, "add-root-cert", cert); r.code != 0 {
		t.Fatalf("trust the local console certificate: %+v", r)
	}
	address, owner, err := connaddr.Parse(w.addr)
	if err != nil {
		t.Fatal(err)
	}
	upstream, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	relay := relayv1connect.NewRelayPublicServiceClient(http.DefaultClient, address)
	_, info := relayv1connect.NewRelayPublicServiceHandler(deletionRelayInfo{
		relay: relay, console: console.URL + "/overview?code=fixture#fragment",
	})
	mux := http.NewServeMux()
	mux.Handle(relayv1connect.RelayPublicServiceGetInfoProcedure, info)
	mux.Handle("/", httputil.NewSingleHostReverseProxy(upstream))
	proxy := httptest.NewServer(mux)
	t.Cleanup(proxy.Close)
	w.addr = connaddr.Format(proxy.URL, owner)
	return console.URL
}
