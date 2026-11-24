package runtime

import (
	"regexp"
	"strings"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
)

// Info describes a provider CLI discovered on this machine.
type Info struct {
	Name      string
	Available bool
	Binary    string
	Version   string
	LoggedIn  bool
	Problem   Problem
	Detail    string
	Models    []Model
	Own       Settings
	AllModels bool
	// ModelsState: ModelsKnown, ModelsUnsupported, ModelsAuthRequired, ModelsError, or "".
	ModelsState string
	ModelsAt    time.Time
	// Limits is what the agent said of its account's rate limits when asked
	// (limits.go); nil when it said none.
	Limits *Limits
}

const (
	ModelsKnown        = "known"
	ModelsUnsupported  = "unsupported"
	ModelsAuthRequired = "auth_required"
	ModelsError        = "error"
)

// Model is one model an agent offers.
type Model struct {
	ID, Name string
	Efforts  []string
}

// Settings are what a session runs with; empty ones follow the agent's own
// configuration.
type Settings struct {
	Model, Effort string
	Speed         identityv1.AgentSpeed
}

// MCPServer is a stdio MCP server Hermesh passes to the coding agent.
type MCPServer struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
}

var (
	mdSkip   = regexp.MustCompile("^(```|~~~|(-\\s*){3,}$|(\\*\\s*){3,}$|(_\\s*){3,}$)")
	mdBlock  = regexp.MustCompile(`^(#{1,6}\s+|>\s*|[-*+]\s+|\d+[.)]\s+|\[[ xX]\]\s+)+`)
	mdLink   = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	mdStrong = regexp.MustCompile(`(\*\*|~~)(\S(?:.*?\S)?)(\*\*|~~)`)
	mdEm     = regexp.MustCompile(`(^|[^\w*])\*(\S(?:[^*]*?\S)?)\*`)
	mdSpace  = regexp.MustCompile(`\s+`)
)

// CleanTitle makes text one short line for a list: its first line with
// words in it, without Markdown, wrapping quotes or a final period, cut to
// 40 characters.
func CleanTitle(s string) string {
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if mdSkip.MatchString(t) {
			continue
		}
		t = mdBlock.ReplaceAllString(t, "")
		t = mdLink.ReplaceAllString(t, "$1")
		t = mdStrong.ReplaceAllString(t, "$2")
		t = mdEm.ReplaceAllString(t, "$1$2")
		t = strings.ReplaceAll(t, "`", "")
		t = mdSpace.ReplaceAllString(strings.TrimSpace(t), " ")
		t = strings.TrimRight(t, "。.")
		t = strings.TrimSpace(strings.Trim(t, "\"'“”‘’「」"))
		t = strings.TrimSpace(strings.TrimRight(t, "。."))
		if t == "" {
			continue
		}
		if r := []rune(t); len(r) > 40 {
			return strings.TrimSpace(string(r[:40])) + "…"
		}
		return t
	}
	return ""
}
