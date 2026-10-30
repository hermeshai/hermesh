package main

import (
	"crypto/sha1"
	"encoding/binary"
	"fmt"
)

// guid is a COM interface id, laid out as Windows lays out a GUID.
type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

func (g guid) String() string {
	return fmt.Sprintf("%08x-%04x-%04x-%x-%x", g.Data1, g.Data2, g.Data3, g.Data4[:2], g.Data4[2:])
}

// winrtIID is the IID of a parameterized WinRT interface, from its
// signature ("pinterface({…};…)"): a version 5 UUID of the signature in
// WinRT's namespace, as the Windows Runtime type system defines it.
func winrtIID(signature string) guid {
	ns := []byte{0x11, 0xf4, 0x7a, 0xd5, 0x7b, 0x73, 0x42, 0xc0, 0xab, 0xae, 0x87, 0x8b, 0x1e, 0x16, 0xad, 0xee}
	h := sha1.Sum(append(ns, signature...))
	b := h[:16]
	b[6] = b[6]&0x0f | 0x50
	b[8] = b[8]&0x3f | 0x80
	return guid{Data1: binary.BigEndian.Uint32(b[0:4]), Data2: binary.BigEndian.Uint16(b[4:6]), Data3: binary.BigEndian.Uint16(b[6:8]), Data4: [8]byte(b[8:16])}
}

// asyncOperation is the signature of IAsyncOperation<T>, T's given.
func asyncOperation(t string) string {
	return "pinterface({9fc2b0bb-e446-44e2-aa61-9cab8f636af2};" + t + ")"
}
