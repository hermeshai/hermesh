package transport

import (
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"github.com/hermeshai/hermesh/pkg/identity"
)

// End-to-end QUIC + HTTP/3 transport between an owner device and a machine:
// uses the exact same Owner Root Chain mTLS credentials as e2e.go (TLS 1.3),
// replacing TCP+TLS+H2 with 1-RTT UDP QUIC + independent non-HoL HTTP/3
// streams, native RFC 9002 SmoothedRTT, and QUIC Connection Migration.

// H3Client is an http.Client bound to a single established E2E QUIC connection.
type H3Client struct {
	*http.Client
	tr      *http3.Transport
	cc      *http3.ClientConn
	qconn   *quic.Conn
	qtr     *quic.Transport // optional owned packet transport (e.g. hole-punched dial)
	pc      net.PacketConn  // optional owned UDP socket paired with qtr
	retired atomic.Bool
}

type h3SchemeNormalizer struct {
	cc *http3.ClientConn
}

func (n h3SchemeNormalizer) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL != nil && req.URL.Scheme != "https" {
		clone := req.Clone(req.Context())
		u := *req.URL
		u.Scheme = "https"
		clone.URL = &u
		req = clone
	}
	return n.cc.RoundTrip(req)
}

func quicConfig() *quic.Config {
	return &quic.Config{
		HandshakeIdleTimeout: 5 * time.Second,
		MaxIdleTimeout:       15 * time.Second,
		KeepAlivePeriod:      5 * time.Second,
		MaxIncomingStreams:   4096,
	}
}

// QUICServerConfig builds the TLS 1.3 mTLS and QUIC configs for a machine's
// direct QUIC/HTTP3 listener. Peer verification is pinned to trust.
func QUICServerConfig(key crypto.Signer, creds CredentialsFunc, trust *identity.Trust,
	accept func(*identity.Subject) error) (*tls.Config, *quic.Config) {
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		ClientAuth: tls.RequireAnyClientCert,
		NextProtos: []string{http3.NextProtoH3},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert, err := identity.TLSCertificate(key, creds())
			if err != nil {
				return nil, err
			}
			return &cert, nil
		},
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			s, err := VerifyPeer(trust, rawCerts)
			if err != nil {
				return err
			}
			if s.Kind == identity.SubjectMachine {
				return errors.New("machines may not open sessions on machines")
			}
			if accept != nil {
				return accept(s)
			}
			return nil
		},
	}
	return tlsCfg, quicConfig()
}

// QUICEndpoint wraps a bound UDP socket, quic.Transport, and quic.Listener so
// a machine can both accept QUIC connections and send UDP hole-punch packets
// from the exact same local IP/port.
type QUICEndpoint struct {
	pc net.PacketConn
	tr *quic.Transport
	ln *quic.Listener
}

func (e *QUICEndpoint) Addr() net.Addr                                 { return e.pc.LocalAddr() }
func (e *QUICEndpoint) Accept(ctx context.Context) (*quic.Conn, error) { return e.ln.Accept(ctx) }
func (e *QUICEndpoint) WritePunch(dst *net.UDPAddr) error {
	_, err := e.tr.WriteTo([]byte("HMP1"), dst)
	return err
}
func (e *QUICEndpoint) Close() error {
	var err error
	if e.ln != nil {
		err = e.ln.Close()
	}
	if e.tr != nil {
		if cerr := e.tr.Close(); err == nil {
			err = cerr
		}
	}
	if e.pc != nil {
		if cerr := e.pc.Close(); err == nil {
			err = cerr
		}
	}
	return err
}

// ListenQUICEndpoint binds network/addr (e.g. "udp4", ":0" or "udp6", "[2408:...]:0")
// and starts a QUIC/HTTP3 listener that also supports WritePunch.
func ListenQUICEndpoint(network, addr string, key crypto.Signer, creds CredentialsFunc, trust *identity.Trust,
	accept func(*identity.Subject) error) (*QUICEndpoint, error) {
	pc, err := net.ListenPacket(network, addr)
	if err != nil {
		return nil, err
	}
	tlsCfg, qCfg := QUICServerConfig(key, creds, trust, accept)
	tr := &quic.Transport{Conn: pc}
	ln, err := tr.Listen(tlsCfg, qCfg)
	if err != nil {
		_ = tr.Close()
		_ = pc.Close()
		return nil, err
	}
	return &QUICEndpoint{pc: pc, tr: tr, ln: ln}, nil
}

// ListenQUIC listens for direct E2E QUIC/HTTP3 connections on UDP addr.
func ListenQUIC(addr string, key crypto.Signer, creds CredentialsFunc, trust *identity.Trust,
	accept func(*identity.Subject) error) (*quic.Listener, error) {
	tlsCfg, qCfg := QUICServerConfig(key, creds, trust, accept)
	return quic.ListenAddr(addr, tlsCfg, qCfg)
}

// VerifiedQUICSubject extracts and verifies the peer Subject from an
// already-established QUIC connection's TLS certificates.
func VerifiedQUICSubject(trust *identity.Trust, conn *quic.Conn) (*identity.Subject, error) {
	certs := conn.ConnectionState().TLS.PeerCertificates
	if len(certs) == 0 {
		return nil, errors.New("missing peer certificate")
	}
	raw := make([][]byte, len(certs))
	for i, c := range certs {
		raw[i] = c.Raw
	}
	return VerifyPeer(trust, raw)
}

// ServeH3 serves HTTP/3 on an established QUIC connection until it closes or
// ctx is cancelled.
func ServeH3(ctx context.Context, conn *quic.Conn, h http.Handler) {
	stop := context.AfterFunc(ctx, func() {
		_ = conn.CloseWithError(0, "shutdown")
	})
	defer stop()
	srv := &http3.Server{Handler: h}
	_ = srv.ServeQUICConn(conn)
}

func quicClientTLS(key crypto.Signer, creds CredentialsFunc, trust *identity.Trust, machineID string, subOut **identity.Subject) (*tls.Config, error) {
	cert, err := identity.TLSCertificate(key, creds())
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		Certificates:       []tls.Certificate{cert},
		NextProtos:         []string{http3.NextProtoH3},
		InsecureSkipVerify: true, // replaced by VerifyPeerCertificate below
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			s, err := VerifyPeer(trust, rawCerts)
			if err != nil {
				return err
			}
			if s.Kind != identity.SubjectMachine || s.ID != machineID {
				return fmt.Errorf("peer is %q, expected machine %q", s.ID, machineID)
			}
			*subOut = s
			return nil
		},
	}, nil
}

// DialQUIC opens a direct E2E QUIC/HTTP3 tunnel to addr and verifies the peer
// is machineID under trust.
func DialQUIC(ctx context.Context, addr string, key crypto.Signer, creds CredentialsFunc,
	trust *identity.Trust, machineID string) (*H3Client, *identity.Subject, error) {
	var sub *identity.Subject
	tlsCfg, err := quicClientTLS(key, creds, trust, machineID, &sub)
	if err != nil {
		return nil, nil, err
	}
	qconn, err := quic.DialAddr(ctx, addr, tlsCfg, quicConfig())
	if err != nil {
		return nil, nil, fmt.Errorf("quic dial: %w", err)
	}
	tr := &http3.Transport{DisableCompression: true}
	cc := tr.NewClientConn(qconn)
	return &H3Client{
		Client: &http.Client{Transport: h3SchemeNormalizer{cc: cc}},
		tr:     tr,
		cc:     cc,
		qconn:  qconn,
	}, sub, nil
}

// DialQUICOnTransport dials addr over an existing bound quic.Transport (used
// for UDP hole punching so outbound punch packets and the QUIC handshake share
// the exact same local port). Call AttachPacketTransport on the winning client
// so Close releases the transport and underlying UDP socket.
func DialQUICOnTransport(ctx context.Context, qtr *quic.Transport, addr string, key crypto.Signer, creds CredentialsFunc,
	trust *identity.Trust, machineID string) (*H3Client, *identity.Subject, error) {
	raddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("quic resolve: %w", err)
	}
	var sub *identity.Subject
	tlsCfg, err := quicClientTLS(key, creds, trust, machineID, &sub)
	if err != nil {
		return nil, nil, err
	}
	qconn, err := qtr.Dial(ctx, raddr, tlsCfg, quicConfig())
	if err != nil {
		return nil, nil, fmt.Errorf("quic dial: %w", err)
	}
	tr := &http3.Transport{DisableCompression: true}
	cc := tr.NewClientConn(qconn)
	return &H3Client{
		Client: &http.Client{Transport: h3SchemeNormalizer{cc: cc}},
		tr:     tr,
		cc:     cc,
		qconn:  qconn,
	}, sub, nil
}

// AttachPacketTransport transfers ownership of qtr and pc to c so Close frees
// them once the QUIC connection ends.
func (c *H3Client) AttachPacketTransport(qtr *quic.Transport, pc net.PacketConn) {
	c.qtr = qtr
	c.pc = pc
}

// Alive reports whether the underlying QUIC connection is still open and taking new requests.
func (c *H3Client) Alive() bool {
	return c != nil && !c.retired.Load() && c.qconn != nil && c.qconn.Context().Err() == nil
}

// MarkRetired stops new requests from using c immediately while leaving active
// streams open until Retire or Close completes.
func (c *H3Client) MarkRetired() {
	if c != nil {
		c.retired.Store(true)
	}
}

// Retire waits until ctx ends or the underlying QUIC connection closes, then closes c.
func (c *H3Client) Retire(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.retired.Store(true)
	if c.qconn != nil {
		select {
		case <-ctx.Done():
		case <-c.qconn.Context().Done():
		}
	}
	return c.Close()
}

// RTT returns the QUIC stack's live RFC 9002 smoothed RTT.
func (c *H3Client) RTT() time.Duration {
	if c == nil || c.qconn == nil {
		return 0
	}
	return c.qconn.ConnectionStats().SmoothedRTT
}

// Close closes the HTTP/3 client and underlying QUIC connection.
func (c *H3Client) Close() error {
	if c == nil {
		return nil
	}
	var err error
	if c.cc != nil {
		err = c.cc.CloseWithError(0, "close")
	}
	if c.qconn != nil {
		if qerr := c.qconn.CloseWithError(0, "close"); err == nil {
			err = qerr
		}
	}
	if c.tr != nil {
		_ = c.tr.Close()
	}
	if c.qtr != nil {
		_ = c.qtr.Close()
	}
	if c.pc != nil {
		_ = c.pc.Close()
	}
	return err
}
