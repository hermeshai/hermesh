package identity

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"math/big"
	"time"

	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
)

// credentialsOID is a private arc under 2.25 (the arcs are the first and
// last 32 bits, top bit cleared, of UUID 7f3c9a52-4d1e-4b8e-9a6f-2c5d8e1b4a70). These
// certificates are only ever exchanged between Hermesh peers.
var credentialsOID = asn1.ObjectIdentifier{2, 25, 0x7f3c9a52, 0x0e1b4a70}

// TLSCertificate builds an ephemeral self-signed certificate for key that
// embeds creds. The certificate is only a container for the public key and
// credentials; verification never relies on X.509 chains.
func TLSCertificate(key crypto.Signer, creds *identityv1.Credentials) (tls.Certificate, error) {
	ext, err := proto.Marshal(creds)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	tmpl := &x509.Certificate{
		SerialNumber:    serial,
		Subject:         pkix.Name{CommonName: "hermesh"},
		NotBefore:       time.Now().Add(-time.Hour),
		NotAfter:        time.Now().Add(24 * time.Hour),
		KeyUsage:        x509.KeyUsageDigitalSignature,
		ExtKeyUsage:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		ExtraExtensions: []pkix.Extension{{Id: credentialsOID, Value: ext}},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// PeerCredentials extracts the public key and embedded credentials from a
// raw peer certificate.
func PeerCredentials(rawCerts [][]byte) (*ecdsa.PublicKey, *identityv1.Credentials, error) {
	if len(rawCerts) != 1 {
		return nil, nil, errors.New("expected exactly one peer certificate")
	}
	cert, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return nil, nil, err
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, nil, errors.New("peer key is not ECDSA")
	}
	if _, err := ParsePublicKey(MarshalPublicKey(pub)); err != nil {
		return nil, nil, err
	}
	for _, e := range cert.Extensions {
		if e.Id.Equal(credentialsOID) {
			c := &identityv1.Credentials{}
			if err := proto.Unmarshal(e.Value, c); err != nil {
				return nil, nil, err
			}
			return pub, c, nil
		}
	}
	return nil, nil, errors.New("peer certificate carries no hermesh credentials")
}

// SubjectPublicKeyMatches reports whether the verified subject key equals
// the TLS key the peer proved possession of.
func SubjectPublicKeyMatches(s *Subject, tlsKey *ecdsa.PublicKey) bool {
	return s.PublicKey.Equal(tlsKey)
}
