package identity

import (
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hpke"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strconv"

	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
)

// Encrypted delta mailbox envelopes (zero-knowledge relay):
// A machine compresses a state delta, encrypts it once with a fresh random
// AES-256-GCM DEK (AAD bound to owner_id, machine_id, stream_id, machine_seq),
// wraps the DEK per trusted device with HPKE (X25519 + HKDF-SHA256 + AES-128-GCM),
// and signs the DeltaEnvelope with its machine key so the relay can neither
// read nor forge or replay entries across streams or sequences.

const maxDeltaPlaintext = 4 << 20 // 4 MiB decompressed safety bound

// SyncRecipient is a verified trusted device that can unwrap a delta DEK.
type SyncRecipient struct {
	DeviceKeyID string
	PublicKey   []byte // 32-byte X25519
}

func deltaAAD(ownerID, machineID, streamID string, seq uint64) []byte {
	return []byte("hermesh-delta-v1\x00" + ownerID + "\x00" + machineID + "\x00" + streamID + "\x00" + strconv.FormatUint(seq, 10))
}

func deltaKeyInfo(ownerID, streamID string) []byte {
	return []byte("hermesh-delta-key-v1\x00" + ownerID + "\x00" + streamID)
}

// NewSyncKey creates a device's X25519 sync key pair.
func NewSyncKey() (private, public []byte, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return k.Bytes(), k.PublicKey().Bytes(), nil
}

// SyncPublicKey returns the X25519 public half of private.
func SyncPublicKey(private []byte) ([]byte, error) {
	k, err := ecdh.X25519().NewPrivateKey(private)
	if err != nil {
		return nil, err
	}
	return k.PublicKey().Bytes(), nil
}

// SealDelta compresses, encrypts, wraps for each recipient, and signs a delta.
func SealDelta(signer Signer, ownerID, machineID, streamID string, machineSeq uint64, plaintext []byte, recipients []SyncRecipient) (*identityv1.Signed, error) {
	if len(recipients) == 0 {
		return nil, errors.New("delta has no recipients")
	}
	if len(plaintext) > maxDeltaPlaintext {
		return nil, fmt.Errorf("delta plaintext exceeds %d bytes", maxDeltaPlaintext)
	}
	var gzBuf bytes.Buffer
	gz := gzip.NewWriter(&gzBuf)
	if _, err := gz.Write(plaintext); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}

	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return nil, err
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	aad := deltaAAD(ownerID, machineID, streamID, machineSeq)
	ciphertext := gcm.Seal(nil, nonce, gzBuf.Bytes(), aad)

	kem, kdf, aead := noticeSuite()
	info := deltaKeyInfo(ownerID, streamID)
	wrapped := make([]*identityv1.WrappedKey, 0, len(recipients))
	for _, r := range recipients {
		if r.DeviceKeyID == "" || len(r.PublicKey) != 32 {
			continue
		}
		pk, err := kem.NewPublicKey(r.PublicKey)
		if err != nil {
			continue
		}
		sealedDEK, err := hpke.Seal(pk, kdf, aead, info, dek)
		if err != nil {
			return nil, err
		}
		wrapped = append(wrapped, &identityv1.WrappedKey{
			DeviceKeyId: r.DeviceKeyID,
			HpkeKey:     sealedDEK,
		})
	}
	if len(wrapped) == 0 {
		return nil, errors.New("delta has no valid recipients")
	}

	env := &identityv1.DeltaEnvelope{
		OwnerId:    ownerID,
		MachineId:  machineID,
		StreamId:   streamID,
		MachineSeq: machineSeq,
		Nonce:      nonce,
		Ciphertext: ciphertext,
		Keys:       wrapped,
		CreatedAt:  timestamppb.Now(),
	}
	return Sign(env, signer)
}

// OpenDelta verifies the machine's signature against trust + machineCert,
// unwraps the DEK for deviceKeyID, decrypts and decompresses the payload.
func OpenDelta(trust *Trust, deviceKeyID string, syncPrivateKey []byte, signed, machineCert *identityv1.Signed) (*identityv1.DeltaEnvelope, []byte, error) {
	mc, machinePub, err := trust.VerifyMachineCert(machineCert)
	if err != nil {
		return nil, nil, fmt.Errorf("delta machine cert: %w", err)
	}
	env := &identityv1.DeltaEnvelope{}
	if err := VerifyWith(signed, machinePub, env); err != nil {
		return nil, nil, fmt.Errorf("delta signature: %w", err)
	}
	if env.OwnerId != trust.ownerID || env.MachineId != mc.MachineId {
		return nil, nil, errors.New("delta owner or machine mismatch")
	}
	var sealedDEK []byte
	for _, wk := range env.Keys {
		if wk.DeviceKeyId == deviceKeyID {
			sealedDEK = wk.HpkeKey
			break
		}
	}
	if len(sealedDEK) == 0 {
		return nil, nil, errors.New("delta not sealed for this device")
	}
	kem, kdf, aead := noticeSuite()
	priv, err := kem.NewPrivateKey(syncPrivateKey)
	if err != nil {
		return nil, nil, err
	}
	dek, err := hpke.Open(priv, kdf, aead, deltaKeyInfo(env.OwnerId, env.StreamId), sealedDEK)
	if err != nil {
		return nil, nil, fmt.Errorf("delta unwrap key: %w", err)
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	if len(env.Nonce) != gcm.NonceSize() {
		return nil, nil, errors.New("delta invalid nonce size")
	}
	aad := deltaAAD(env.OwnerId, env.MachineId, env.StreamId, env.MachineSeq)
	gzBytes, err := gcm.Open(nil, env.Nonce, env.Ciphertext, aad)
	if err != nil {
		return nil, nil, fmt.Errorf("delta decrypt: %w", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(gzBytes))
	if err != nil {
		return nil, nil, fmt.Errorf("delta gzip: %w", err)
	}
	defer gz.Close()
	plain, err := io.ReadAll(io.LimitReader(gz, maxDeltaPlaintext+1))
	if err != nil {
		return nil, nil, fmt.Errorf("delta decompress: %w", err)
	}
	if len(plain) > maxDeltaPlaintext {
		return nil, nil, errors.New("delta decompressed payload exceeds limit")
	}
	return env, plain, nil
}
