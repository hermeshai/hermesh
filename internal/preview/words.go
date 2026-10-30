package preview

import (
	"embed"
	"encoding/json"
	"fmt"
)

// The cards are written in English; a language's file maps each English
// text (a fmt format) to its own, which keeps the same verbs.
//
//go:embed locales
var locales embed.FS

type words map[string]string

// wordsFor is the language's words; English (or one it doesn't have) is none.
func wordsFor(lang string) words {
	b, err := locales.ReadFile("locales/" + lang + ".json")
	if err != nil {
		return nil
	}
	var w words
	json.Unmarshal(b, &w)
	return w
}

// f is format, in this language, with args.
func (w words) f(format string, args ...any) string {
	if t, ok := w[format]; ok {
		format = t
	}
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}
