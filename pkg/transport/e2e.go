package transport

import (
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// E2E TLS between a client (agent or owner device) and a machine, inside a
// relay tunnel. Both sides present ephemeral self-signed certificates that
// embed their Hermesh credentials; trust comes only from the owner anchor.

// CredentialsFunc returns the credentials to present (they may change when
// a grant is renewed).
type CredentialsFunc func() *identityv1.Credentials

// VerifyPeer checks the peer's certificate against trust and returns the
// verified subject, which must own the TLS key.
func VerifyPeer(trust *identity.Trust, rawCerts [][]byte) (*identity.Subject, error) {
	pub, creds, err := identity.PeerCredentials(rawCerts)
	if err != nil {
		return nil, err
	}
	sub, err := trust.VerifyCredentials(creds, time.Now())
	if err != nil {
		return nil, err
	}
	if !identity.SubjectPublicKeyMatches(sub, pub) {
		return nil, errors.New("certificate key does not match credentials")
	}
	return sub, nil
}

// ServerHandshake runs the machine side of the E2E handshake. accept decides
// whether a verified subject may connect at all.
func ServerHandshake(ctx context.Context, raw net.Conn, key crypto.Signer, creds CredentialsFunc,
	trust *identity.Trust, accept func(*identity.Subject) error) (*tls.Conn, *identity.Subject, error) {
	cert, err := identity.TLSCertificate(key, creds())
	if err != nil {
		return nil, nil, err
	}
	var sub *identity.Subject
	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAnyClientCert,
		NextProtos:   []string{"h2"},
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			s, err := VerifyPeer(trust, rawCerts)
			if err != nil {
				return err
			}
			if s.Kind == identity.SubjectMachine {
				return errors.New("machines may not open sessions on machines")
			}
			if err := accept(s); err != nil {
				return err
			}
			sub = s
			return nil
		},
	}
	tc := tls.Server(raw, cfg)
	hctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := tc.HandshakeContext(hctx); err != nil {
		tc.Close()
		return nil, nil, fmt.Errorf("e2e handshake: %w", err)
	}
	return tc, sub, nil
}

// ClientHandshake runs the client side and checks that the peer is the
// expected machine of the expected owner.
func ClientHandshake(ctx context.Context, raw net.Conn, key crypto.Signer, creds CredentialsFunc,
	trust *identity.Trust, machineID string) (*tls.Conn, *identity.Subject, error) {
	cert, err := identity.TLSCertificate(key, creds())
	if err != nil {
		return nil, nil, err
	}
	var sub *identity.Subject
	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		Certificates:       []tls.Certificate{cert},
		NextProtos:         []string{"h2"},
		InsecureSkipVerify: true, // replaced by VerifyPeerCertificate below
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			s, err := VerifyPeer(trust, rawCerts)
			if err != nil {
				return err
			}
			if s.Kind != identity.SubjectMachine || s.ID != machineID {
				return fmt.Errorf("peer is %q, expected machine %q", s.ID, machineID)
			}
			sub = s
			return nil
		},
	}
	tc := tls.Client(raw, cfg)
	hctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := tc.HandshakeContext(hctx); err != nil {
		tc.Close()
		return nil, nil, fmt.Errorf("e2e handshake: %w", err)
	}
	return tc, sub, nil
}
