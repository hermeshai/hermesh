package main

import (
	"embed"
	"encoding/json"
)

//go:embed locales
var locales embed.FS

// words are the panel's own text, in the language of Windows' display
// (English unless it is Chinese); what is signed reads as internal/preview
// tells it.
type words struct {
	Title, Approve, Deny, Several, Code, Hello, NotSigned, NoHello string
}

func loadWords(lang string) words {
	var w words
	b, err := locales.ReadFile("locales/" + lang + ".json")
	if err != nil {
		b, _ = locales.ReadFile("locales/en.json")
	}
	json.Unmarshal(b, &w)
	return w
}
