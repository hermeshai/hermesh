package main

import (
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	webview2 "github.com/jchv/go-webview2"

	"github.com/hermeshai/hermesh/internal/preview"
)

// TestWebPanel opens the panel in WebView2 and reads back what it drew.
func TestWebPanel(t *testing.T) {
	showsPanel(t)
}

// TestWebPanelAfterMultithreadedCOM: threads that other code left
// multithreaded for COM, as Windows Hello leaves its own, don't keep the
// panel from loading (WebView2 waited forever on one: a blank window).
func TestWebPanelAfterMultithreadedCOM(t *testing.T) {
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runtime.LockOSThread()
			defer runtime.UnlockOSThread() // back to the pool, multithreaded
			procRoInitialize.Call(1)
			time.Sleep(50 * time.Millisecond) // all at once: as many threads
		}()
	}
	wg.Wait()
	for range 3 {
		showsPanel(t)
	}
}

func showsPanel(t *testing.T) {
	t.Helper()
	p := newPanel()
	// WebView2's own processes hold its folder a while after the window goes.
	dir, err := os.MkdirTemp("", "hermesh-panel-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for range 20 {
			if os.RemoveAll(dir) == nil {
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
	})
	wp := &webPanel{w: p.w, dataDir: dir, fallback: p}
	drawn := make(chan string, 1)
	closed := make(chan struct{})
	cards := []preview.Card{{Title: "Allow “bot” to connect to your computers", Code: "K4TP 9WQ2 HM3V", Lines: []string{"Machine: vps-1"}}}
	err = wp.open(map[string]any{"kind": "confirm", "cards": cards}, func(v webview2.WebView, hwnd uintptr) {
		v.Bind("probe", func(s string) { drawn <- s })
		v.Init(`addEventListener("load", () => probe(document.querySelector("h1").textContent));`)
	}, func() { close(closed) })
	if err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-drawn:
		if s != cards[0].Title {
			t.Errorf("drawn %q, want the card's title", s)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the page never loaded")
	}
	wp.close()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("the window did not close")
	}
}
