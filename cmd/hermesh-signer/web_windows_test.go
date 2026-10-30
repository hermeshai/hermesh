package main

import (
	"testing"
	"time"

	webview2 "github.com/jchv/go-webview2"

	"github.com/hermeshai/hermesh/internal/preview"
)

// TestWebPanel opens the panel in WebView2 and reads back what it drew.
func TestWebPanel(t *testing.T) {
	p := newPanel()
	wp := &webPanel{w: p.w, dataDir: t.TempDir(), fallback: p}
	drawn := make(chan string, 1)
	closed := make(chan struct{})
	cards := []preview.Card{{Title: "Allow “bot” to connect to your computers", Code: "K4TP 9WQ2 HM3V", Lines: []string{"Machine: vps-1"}}}
	err := wp.open(map[string]any{"kind": "confirm", "cards": cards}, func(v webview2.WebView, hwnd uintptr) {
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
