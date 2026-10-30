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
	lang                                                                        string
	Title, Approve, Deny, Several, Code, Hello, NotSigned, NoHello              string
	CodeLabel, Match, OwnCaption, Along, Details, HideDetails, HelloAfter, Wait string
	OpenSignIn, OK                                                              string
}

func loadWords(lang string) words {
	w := words{lang: lang}
	b, err := locales.ReadFile("locales/" + lang + ".json")
	if err != nil {
		w.lang = "en"
		b, _ = locales.ReadFile("locales/en.json")
	}
	json.Unmarshal(b, &w)
	return w
}
