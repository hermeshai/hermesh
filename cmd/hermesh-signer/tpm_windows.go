package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/hermeshai/hermesh/pkg/identity"
)

// tpmKeys are root keys in the TPM, through the Microsoft Platform Crypto
// Provider: never exportable, and usable only with a random PIN that the
// signer keeps in dir, which only administrators and the system can read.
// A program of the user's that is not elevated can ask the TPM for the key
// by its name, but without the PIN it cannot sign, and the TPM locks out
// guessing (docs/windows.md §7.3).
type tpmKeys struct{ dir string }

// keyRecord is what opens one root key: its name in the provider and its PIN.
type keyRecord struct {
	Name string `json:"name"`
	PIN  string `json:"pin"`
}

var (
	ncrypt                          = windows.NewLazySystemDLL("ncrypt.dll")
	procOpenStorageProvider         = ncrypt.NewProc("NCryptOpenStorageProvider")
	procCreatePersistedKey          = ncrypt.NewProc("NCryptCreatePersistedKey")
	procOpenKey                     = ncrypt.NewProc("NCryptOpenKey")
	procSetProperty                 = ncrypt.NewProc("NCryptSetProperty")
	procFinalizeKey                 = ncrypt.NewProc("NCryptFinalizeKey")
	procExportKey                   = ncrypt.NewProc("NCryptExportKey")
	procSignHash                    = ncrypt.NewProc("NCryptSignHash")
	procDeleteKey                   = ncrypt.NewProc("NCryptDeleteKey")
	procFreeObject                  = ncrypt.NewProc("NCryptFreeObject")
	platformProvider, _             = windows.UTF16PtrFromString("Microsoft Platform Crypto Provider")
	algP256, _                      = windows.UTF16PtrFromString("ECDSA_P256")
	propPIN, _                      = windows.UTF16PtrFromString("SmartCardPin")
	blobECCPublic, _                = windows.UTF16PtrFromString("ECCPUBLICBLOB")
	ncryptSilent            uintptr = 0x40
)

// status turns an NCrypt SECURITY_STATUS into an error.
func status(call string, r uintptr) error {
	if r == 0 {
		return nil
	}
	return fmt.Errorf("%s: %#x", call, uint32(r))
}

func openProvider() (uintptr, error) {
	var prov uintptr
	if err := status("NCryptOpenStorageProvider", call(procOpenStorageProvider, uintptr(unsafe.Pointer(&prov)), uintptr(unsafe.Pointer(platformProvider)), 0)); err != nil {
		return 0, fmt.Errorf("%w (%v)", errNoTPM, err)
	}
	return prov, nil
}

func call(p *windows.LazyProc, args ...uintptr) uintptr {
	r, _, _ := p.Call(args...)
	return r
}

func free(h uintptr) { call(procFreeObject, h) }

func setPIN(key uintptr, pin string) error {
	p, err := windows.UTF16FromString(pin)
	if err != nil {
		return err
	}
	return status("NCryptSetProperty", call(procSetProperty, key, uintptr(unsafe.Pointer(propPIN)), uintptr(unsafe.Pointer(&p[0])), uintptr(2*len(p)), ncryptSilent))
}

func (k tpmKeys) Create(context.Context) (*ecdsa.PublicKey, error) {
	prov, err := openProvider()
	if err != nil {
		return nil, err
	}
	defer free(prov)
	pinBytes := make([]byte, 32)
	rand.Read(pinBytes)
	rec := keyRecord{Name: "Hermesh " + identity.RandomCode(16), PIN: hex.EncodeToString(pinBytes)}
	name, _ := windows.UTF16PtrFromString(rec.Name)
	var key uintptr
	if err := status("NCryptCreatePersistedKey", call(procCreatePersistedKey, prov, uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(algP256)), uintptr(unsafe.Pointer(name)), 0, 0)); err != nil {
		return nil, err
	}
	defer free(key)
	if err := setPIN(key, rec.PIN); err != nil {
		return nil, err
	}
	if err := status("NCryptFinalizeKey", call(procFinalizeKey, key, ncryptSilent)); err != nil {
		return nil, err
	}
	pub, err := exportPublic(key)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(rec)
	if err := os.WriteFile(k.record(identity.KeyID(pub)), b, 0o600); err != nil {
		return nil, err
	}
	return pub, nil
}

// exportPublic reads a key's public half (a BCRYPT_ECCKEY_BLOB: magic, size, X, Y).
func exportPublic(key uintptr) (*ecdsa.PublicKey, error) {
	buf := make([]byte, 8+64)
	var n uint32
	if err := status("NCryptExportKey", call(procExportKey, key, 0, uintptr(unsafe.Pointer(blobECCPublic)), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&n)), 0)); err != nil {
		return nil, err
	}
	if n != 72 || binary.LittleEndian.Uint32(buf[4:8]) != 32 {
		return nil, fmt.Errorf("the TPM gave a public key of %d bytes", n)
	}
	pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(buf[8:40]), Y: new(big.Int).SetBytes(buf[40:72])}
	if !pub.Curve.IsOnCurve(pub.X, pub.Y) {
		return nil, errors.New("the TPM gave a public key off the curve")
	}
	return pub, nil
}

func (k tpmKeys) record(keyID string) string { return filepath.Join(k.dir, keyID+".json") }

// open opens the root key keyID with its PIN set; free it after use.
func (k tpmKeys) open(keyID string) (uintptr, error) {
	b, err := os.ReadFile(k.record(keyID))
	if err != nil {
		return 0, fmt.Errorf("this PC has no root key %s", keyID)
	}
	var rec keyRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return 0, err
	}
	prov, err := openProvider()
	if err != nil {
		return 0, err
	}
	defer free(prov)
	name, _ := windows.UTF16PtrFromString(rec.Name)
	var key uintptr
	if err := status("NCryptOpenKey", call(procOpenKey, prov, uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(name)), 0, ncryptSilent)); err != nil {
		return 0, err
	}
	if err := setPIN(key, rec.PIN); err != nil {
		free(key)
		return 0, err
	}
	return key, nil
}

func (k tpmKeys) Sign(_ context.Context, keyID string, messages [][]byte) ([][]byte, error) {
	key, err := k.open(keyID)
	if err != nil {
		return nil, err
	}
	defer free(key)
	sigs := make([][]byte, len(messages))
	for i, m := range messages {
		h := sha256.Sum256(m)
		raw := make([]byte, 64)
		var n uint32
		if err := status("NCryptSignHash", call(procSignHash, key, 0, uintptr(unsafe.Pointer(&h[0])), 32, uintptr(unsafe.Pointer(&raw[0])), 64, uintptr(unsafe.Pointer(&n)), ncryptSilent)); err != nil {
			return nil, err
		}
		if n != 64 {
			return nil, fmt.Errorf("the TPM gave a signature of %d bytes", n)
		}
		// r and s, as everything that verifies reads them (ASN.1).
		if sigs[i], err = asn1.Marshal(struct{ R, S *big.Int }{new(big.Int).SetBytes(raw[:32]), new(big.Int).SetBytes(raw[32:])}); err != nil {
			return nil, err
		}
	}
	return sigs, nil
}

func (k tpmKeys) Delete(keyID string) error {
	key, err := k.open(keyID)
	if err == nil {
		if err = status("NCryptDeleteKey", call(procDeleteKey, key, 0)); err != nil { // it frees key when it deletes
			free(key)
		}
	}
	os.Remove(k.record(keyID))
	return err
}
