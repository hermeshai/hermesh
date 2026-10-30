package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Every language has every word, with the same %-verbs.
func TestWords(t *testing.T) {
	en := loadWords("en")
	for _, lang := range []string{"en", "zh-CN"} {
		b, err := locales.ReadFile("locales/" + lang + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]string
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatal(err)
		}
		w := reflect.ValueOf(loadWords(lang))
		fields := 0
		for i := 0; i < w.NumField(); i++ {
			if !w.Type().Field(i).IsExported() {
				continue
			}
			fields++
			name := w.Type().Field(i).Name
			got, want := w.Field(i).String(), reflect.ValueOf(en).Field(i).String()
			if got == "" || strings.Count(got, "%") != strings.Count(want, "%") {
				t.Errorf("%s: %s = %q", lang, name, got)
			}
		}
		if len(raw) != fields {
			t.Errorf("%s: %d words, want %d", lang, len(raw), fields)
		}
	}
}
