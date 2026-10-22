package identity

import (
	"crypto/ecdh"
	"crypto/hpke"
	"errors"

	"google.golang.org/protobuf/proto"
)

// Notices for a trusted device's lock screen are sealed to its notice key
// (identity.v1.PushRegistration.notice_public_key) with HPKE, so the relay
// and the push service only carry ciphertext. The iPhone opens them with
// CryptoKit's HPKE using the same suite.
var noticeInfo = []byte("hermesh-notice-v1")

func noticeSuite() (hpke.KEM, hpke.KDF, hpke.AEAD) {
	return hpke.DHKEM(ecdh.X25519()), hpke.HKDFSHA256(), hpke.AES128GCM()
}

// SealNotice encrypts n to an X25519 notice key: the encapsulated key
// followed by the ciphertext.
func SealNotice(noticeKey []byte, n proto.Message) ([]byte, error) {
	kem, kdf, aead := noticeSuite()
	if len(noticeKey) != 32 {
		return nil, errors.New("a notice key is 32 bytes (X25519)")
	}
	pk, err := kem.NewPublicKey(noticeKey)
	if err != nil {
		return nil, err
	}
	body, err := proto.Marshal(n)
	if err != nil {
		return nil, err
	}
	return hpke.Seal(pk, kdf, aead, noticeInfo, body)
}

// OpenNotice decrypts what SealNotice made, with the notice key's private
// half (tests; the iPhone does this itself).
func OpenNotice(privateKey, sealed []byte, out proto.Message) error {
	kem, kdf, aead := noticeSuite()
	k, err := kem.NewPrivateKey(privateKey)
	if err != nil {
		return err
	}
	body, err := hpke.Open(k, kdf, aead, noticeInfo, sealed)
	if err != nil {
		return err
	}
	return proto.Unmarshal(body, out)
}
