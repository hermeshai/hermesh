// Command hermesh-mobile is the owner role built as a C library for the
// iPhone app (apps/ios/scripts/build-core.sh: go build -buildmode=c-archive).
// The app calls these functions; everything else goes through LocalService
// on the loopback port Start returns.
package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
	_ "time/tzdata" // an iPhone has no zoneinfo to read: the days and hours of QueryWork need it
	"unsafe"

	"github.com/hermeshai/hermesh/internal/mobile"
)

var (
	mu   sync.Mutex
	core *mobile.Core
)

// HermeshCoreStart starts the owner role with its data in dataDir (once per
// process; later calls return the same port and token). It returns JSON,
// {"port":…,"token":…} or {"error":…}, to free with HermeshCoreFree.
//
//export HermeshCoreStart
func HermeshCoreStart(dataDir *C.char) *C.char {
	mu.Lock()
	defer mu.Unlock()
	if core == nil {
		dir := C.GoString(dataDir)
		c, err := mobile.Start(dir, logger(dir))
		if err != nil {
			return result(map[string]any{"error": err.Error()})
		}
		core = c
	}
	return result(map[string]any{"port": core.Port(), "token": core.Token()})
}

// HermeshCoreResume keeps the relay connection up (the app came to the front).
//
//export HermeshCoreResume
func HermeshCoreResume() {
	if c := started(); c != nil {
		c.Resume()
	}
}

// HermeshCorePause stops following the machines, keeping the relay connection
// to try again first (the app went to the background).
//
//export HermeshCorePause
func HermeshCorePause() {
	if c := started(); c != nil {
		c.Pause()
	}
}

// HermeshCoreNetworkChanged connects again over the network now in use.
//
//export HermeshCoreNetworkChanged
func HermeshCoreNetworkChanged() {
	if c := started(); c != nil {
		c.Reconnect()
	}
}

// HermeshCorePathChanged connects again when the device's addresses are not
// those last seen: the system reported a change of the network that left
// its interfaces as they were.
//
//export HermeshCorePathChanged
func HermeshCorePathChanged() {
	if c := started(); c != nil {
		c.PathChanged()
	}
}

// HermeshCoreSetCellular tells the core whether the device currently has only
// a cellular network interface (1) or also Wi-Fi/wired (0).
//
//export HermeshCoreSetCellular
func HermeshCoreSetCellular(cellular C.int) {
	if c := started(); c != nil {
		c.SetCellularOnly(cellular != 0)
	}
}

// HermeshCoreFree frees a string the core returned.
//
//export HermeshCoreFree
func HermeshCoreFree(s *C.char) { C.free(unsafe.Pointer(s)) }

// HermeshCoreFreeBytes frees a binary buffer returned by FFI RPC calls.
//
//export HermeshCoreFreeBytes
func HermeshCoreFreeBytes(p unsafe.Pointer) { C.free(p) }

// HermeshCoreUnary executes a LocalService unary RPC directly in memory using
// binary Protobuf (zero TCP loopback, zero JSON/Base64). Returns 0 on success
// and -1 on error (populating errCode and errMsg).
//
//export HermeshCoreUnary
func HermeshCoreUnary(method *C.char, reqPtr unsafe.Pointer, reqLen C.int, timeoutMs C.int,
	outPtr *unsafe.Pointer, outLen *C.int, errCode **C.char, errMsg **C.char) C.int {
	c := started()
	if c == nil {
		if errCode != nil {
			*errCode = C.CString("unavailable")
		}
		if errMsg != nil {
			*errMsg = C.CString("Hermesh core is not running")
		}
		return -1
	}
	var req []byte
	if reqPtr != nil && reqLen > 0 {
		req = C.GoBytes(reqPtr, reqLen)
	}
	res, code, msg := c.InvokeUnary(C.GoString(method), req, time.Duration(timeoutMs)*time.Millisecond)
	if code != "" {
		if errCode != nil {
			*errCode = C.CString(code)
		}
		if errMsg != nil {
			*errMsg = C.CString(msg)
		}
		return -1
	}
	if outLen != nil {
		*outLen = C.int(len(res))
	}
	if outPtr != nil && len(res) > 0 {
		*outPtr = C.CBytes(res)
	}
	return 0
}

// HermeshCoreStreamOpen opens an in-process binary Protobuf LocalService
// server stream and returns a positive stream handle (0 if core not started).
//
//export HermeshCoreStreamOpen
func HermeshCoreStreamOpen(method *C.char, reqPtr unsafe.Pointer, reqLen C.int) C.longlong {
	c := started()
	if c == nil {
		return 0
	}
	var req []byte
	if reqPtr != nil && reqLen > 0 {
		req = C.GoBytes(reqPtr, reqLen)
	}
	return C.longlong(c.OpenStream(C.GoString(method), req))
}

// HermeshCoreStreamNext blocks until the next binary Protobuf frame arrives on
// streamID. Returns 1 for a frame (written to outPtr/outLen), 0 for clean EOF,
// or -1 for an error (written to errCode/errMsg).
//
//export HermeshCoreStreamNext
func HermeshCoreStreamNext(streamID C.longlong, outPtr *unsafe.Pointer, outLen *C.int,
	errCode **C.char, errMsg **C.char) C.int {
	c := started()
	if c == nil {
		if errCode != nil {
			*errCode = C.CString("unavailable")
		}
		if errMsg != nil {
			*errMsg = C.CString("Hermesh core is not running")
		}
		return -1
	}
	frame, done, code, msg := c.NextStreamFrame(int64(streamID))
	if code != "" {
		if errCode != nil {
			*errCode = C.CString(code)
		}
		if errMsg != nil {
			*errMsg = C.CString(msg)
		}
		return -1
	}
	if done {
		return 0
	}
	if outLen != nil {
		*outLen = C.int(len(frame))
	}
	if outPtr != nil && len(frame) > 0 {
		*outPtr = C.CBytes(frame)
	}
	return 1
}

// HermeshCoreStreamClose cancels and releases an open FFI stream.
//
//export HermeshCoreStreamClose
func HermeshCoreStreamClose(streamID C.longlong) {
	if c := started(); c != nil {
		c.CloseStream(int64(streamID))
	}
}

func started() *mobile.Core {
	mu.Lock()
	defer mu.Unlock()
	return core
}

func result(v map[string]any) *C.char {
	b, _ := json.Marshal(v)
	return C.CString(string(b))
}

// logger writes to <dataDir>/logs/core.log (kept under 5 MB) and stderr.
func logger(dataDir string) *slog.Logger {
	var out io.Writer = os.Stderr
	dir := filepath.Join(dataDir, "logs")
	os.MkdirAll(dir, 0o700)
	path := filepath.Join(dir, "core.log")
	if st, err := os.Stat(path); err == nil && st.Size() > 5<<20 {
		os.Rename(path, path+".1")
	}
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		out = io.MultiWriter(os.Stderr, f)
	}
	return slog.New(slog.NewTextHandler(out, nil))
}

func main() {}
