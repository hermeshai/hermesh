// Command hermesh-relay runs the self-hosted Hermesh relay.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/acme/autocert"

	"github.com/hermeshai/hermesh/internal/sqlitedb"
	"github.com/hermeshai/hermesh/pkg/mcpgw"
	"github.com/hermeshai/hermesh/pkg/metrics"
	"github.com/hermeshai/hermesh/pkg/relay"
	"github.com/hermeshai/hermesh/pkg/rlimit"
)

func main() {
	root := &cobra.Command{Use: "hermesh-relay", Short: "Hermesh relay (self-hosted, untrusted coordinator)", SilenceUsage: true}
	var dataDir, listen, domain, certFile, keyFile, debugAddr, publicURL, setupURL string
	var withMCP bool
	var apnsKey, apnsKeyID, apnsTeam, apnsTopic, pushSpool string
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Run the relay",
		RunE: func(cmd *cobra.Command, _ []string) error {
			log := slog.New(slog.NewTextHandler(os.Stderr, nil))
			log.Info("open file limit", "soft", rlimit.RaiseNoFile())
			if debugAddr != "" {
				go func() { log.Warn("debug server stopped", "err", metrics.Serve(debugAddr)) }()
			}
			var pusher relay.Pusher
			switch {
			case apnsKey != "":
				a, err := relay.NewAPNs(apnsKey, apnsKeyID, apnsTeam, apnsTopic)
				if err != nil {
					return fmt.Errorf("APNs: %w", err)
				}
				pusher = a
			case pushSpool != "":
				pusher = relay.Spool{Dir: pushSpool}
			}
			srv, err := relay.New(relay.Config{DataDir: dataDir, Logger: log, Pusher: pusher})
			if err != nil {
				return err
			}
			defer srv.Close()
			if code, err := srv.ClaimCode(); err != nil {
				return err
			} else if code != "" {
				fmt.Fprintf(os.Stderr, "\n  This relay is not claimed yet.\n  Claim code: %s\n  Enter it in Hermesh.app during setup.\n\n", code)
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			handler := srv.Handler()
			if withMCP {
				gw, err := startMCP(srv, log, dataDir, firstNonEmpty(publicURL, defaultPublicURL(domain, certFile, listen)), setupURL)
				if err != nil {
					return err
				}
				defer gw.Close()
				mux := http.NewServeMux()
				srv.Mount(mux)
				gw.Mount(mux)
				handler = mux
			}
			hs := &http.Server{Handler: handler, ReadHeaderTimeout: 15 * time.Second}
			go func() {
				<-ctx.Done()
				sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				hs.Shutdown(sctx)
			}()
			switch {
			case domain != "":
				m := &autocert.Manager{Prompt: autocert.AcceptTOS, HostPolicy: autocert.HostWhitelist(domain),
					Cache: autocert.DirCache(filepath.Join(dataDir, "acme"))}
				go http.ListenAndServe(":80", m.HTTPHandler(nil))
				hs.Addr = ":443"
				hs.TLSConfig = &tls.Config{GetCertificate: m.GetCertificate, MinVersion: tls.VersionTLS12}
				log.Info("relay listening", "addr", hs.Addr, "domain", domain, "relay", srv.RelayID())
				err = hs.ListenAndServeTLS("", "")
			case certFile != "":
				hs.Addr = listen
				log.Info("relay listening (tls)", "addr", listen, "relay", srv.RelayID())
				err = hs.ListenAndServeTLS(certFile, keyFile)
			default:
				hs.Addr = listen
				log.Info("relay listening (plain http; put a TLS proxy in front)", "addr", listen, "relay", srv.RelayID())
				err = hs.ListenAndServe()
			}
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		},
	}
	serve.Flags().StringVar(&dataDir, "data-dir", envOr("HERMESH_RELAY_DATA", "./relay-data"), "data directory")
	serve.Flags().StringVar(&listen, "listen", ":8080", "listen address (plain HTTP or with --tls-cert)")
	serve.Flags().StringVar(&domain, "domain", "", "serve HTTPS on :443 with an automatic Let's Encrypt certificate for this domain")
	serve.Flags().StringVar(&certFile, "tls-cert", "", "TLS certificate file")
	serve.Flags().StringVar(&keyFile, "tls-key", "", "TLS key file")
	serve.Flags().StringVar(&debugAddr, "debug-addr", "", "serve /metrics and /debug/pprof on this loopback address")
	serve.Flags().BoolVar(&withMCP, "mcp", false, "also serve MCP at <public URL>/mcp for remote agent products (Claude, ChatGPT, Codex, ...); this relay then holds the keys of the MCP connections you approve")
	serve.Flags().StringVar(&publicURL, "public-url", "", "the URL people reach this relay at (default: from --domain, else the listen address)")
	serve.Flags().StringVar(&apnsKey, "apns-key", os.Getenv("HERMESH_APNS_KEY"), "an APNs auth key (.p8) to notify iPhones with; the key must belong to the team that signs the Hermesh iPhone app")
	serve.Flags().StringVar(&apnsKeyID, "apns-key-id", os.Getenv("HERMESH_APNS_KEY_ID"), "the APNs key's id")
	serve.Flags().StringVar(&apnsTeam, "apns-team-id", os.Getenv("HERMESH_APNS_TEAM_ID"), "the Apple developer team id the APNs key belongs to")
	serve.Flags().StringVar(&apnsTopic, "apns-topic", "com.hermesh.ios", "the iPhone app's bundle id")
	serve.Flags().StringVar(&pushSpool, "push-spool", "", "write notifications to files in this directory instead of sending them (tests)")
	serve.Flags().MarkHidden("push-spool")
	serve.Flags().StringVar(&setupURL, "setup-url", "https://github.com/hermeshai/hermesh/releases/latest", "where MCP users are sent to get Hermesh for Mac")

	claim := &cobra.Command{
		Use:   "claim-code",
		Short: "Print the claim code of an unclaimed relay",
		RunE: func(cmd *cobra.Command, _ []string) error {
			srv, err := relay.New(relay.Config{DataDir: dataDir, Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))})
			if err != nil {
				return err
			}
			defer srv.Close()
			code, err := srv.ClaimCode()
			if err != nil {
				return err
			}
			if code == "" {
				fmt.Println("already claimed")
				return nil
			}
			fmt.Println(code)
			return nil
		},
	}
	claim.Flags().StringVar(&dataDir, "data-dir", envOr("HERMESH_RELAY_DATA", "./relay-data"), "data directory")
	root.AddCommand(serve, claim)
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// startMCP starts the MCP gateway of a self-hosted relay: its state in
// mcp.db and the key that seals the connections' keys in mcp-vault.key.
func startMCP(srv *relay.Server, log *slog.Logger, dataDir, publicURL, setupURL string) (*mcpgw.Gateway, error) {
	db, err := sqlitedb.Open(filepath.Join(dataDir, "mcp.db"), []string{mcpgw.Schema(false)})
	if err != nil {
		return nil, err
	}
	vault, err := mcpgw.KeyFileVault(filepath.Join(dataDir, "mcp-vault.key"), true)
	if err != nil {
		return nil, err
	}
	publicURL = strings.TrimSuffix(publicURL, "/")
	gw, err := mcpgw.New(mcpgw.Config{Issuer: publicURL, ResourceURL: publicURL + "/mcp", Relay: srv, Backend: &mcpgw.SQLBackend{DB: db},
		Vault: vault, Accounts: mcpgw.SingleOwner{Relay: srv}, SetupURL: setupURL, Logger: log})
	if err != nil {
		return nil, err
	}
	log.Info("mcp gateway", "url", publicURL+"/mcp")
	return gw, nil
}

// defaultPublicURL is the relay's URL as its flags give it.
func defaultPublicURL(domain, certFile, listen string) string {
	if domain != "" {
		return "https://" + domain
	}
	scheme := "http"
	if certFile != "" {
		scheme = "https"
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return scheme + "://" + listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
