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

// HermeshCorePause closes the relay connection (the app went to the background).
//
//export HermeshCorePause
func HermeshCorePause() {
	if c := started(); c != nil {
		c.Pause()
	}
}

// HermeshCoreFree frees a string the core returned.
//
//export HermeshCoreFree
func HermeshCoreFree(s *C.char) { C.free(unsafe.Pointer(s)) }

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
