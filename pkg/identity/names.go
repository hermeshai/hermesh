package identity

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Names under an agent (docs/agent-names.md §4): the bots or chats sharing
// one agent, and the conversations of one computer, are told apart by a
// name in their addresses (inbox.v1.Address.session).

// MaxNameLen is how many letters and digits a name has at most.
const MaxNameLen = 40

// NormalizeName makes the one form of a name: Unicode NFKC, lowercase,
// letters and digits of any script kept and everything else a single "-".
// "Tech Demos", "tech_demos" and "tech-demos" are all "tech-demos".
func NormalizeName(s string) (string, error) {
	var b strings.Builder
	dash := false
	for _, r := range norm.NFKC.String(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(unicode.ToLower(r))
			dash = false
			continue
		}
		dash = true
	}
	n := b.String()
	switch {
	case n == "":
		return "", errors.New("a name has a letter or a digit")
	case utf8.RuneCountInString(n) > MaxNameLen:
		return "", errors.New("a name is at most 40 letters and digits")
	}
	return n, nil
}
