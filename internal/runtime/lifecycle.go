package runtime

import (
	"regexp"
	"strings"
	"time"
)

// TurnStepPhase represents the current phase of an out-of-turn or streaming
// agent step for protocols without an explicit RPC response boundary.
type TurnStepPhase uint8

const (
	StepIdle TurnStepPhase = iota
	StepExecutingTools
	StepAwaitingPostToolModel
	StepStreamingReply
	StepModelResponded
)

// TurnStepConfig configures fallback timeouts for TurnStepTracker when zero
// open tools and zero active child/wakeup probes remain.
type TurnStepConfig struct {
	Debounce          time.Duration
	StreamingQuiet    time.Duration
	PostToolModelWait time.Duration
}

func (c TurnStepConfig) debounce() time.Duration {
	if c.Debounce > 0 {
		return c.Debounce
	}
	return 1500 * time.Millisecond
}

func (c TurnStepConfig) streamingQuiet() time.Duration {
	if c.StreamingQuiet > 0 {
		return c.StreamingQuiet
	}
	return 90 * time.Second
}

func (c TurnStepConfig) postToolModelWait() time.Duration {
	if c.PostToolModelWait > 0 {
		return c.PostToolModelWait
	}
	return 3 * time.Minute
}

// TurnStepTracker tracks the LLM tool-use state machine for out-of-turn agent
// activity so a turn is never prematurely closed right after a tool completes
// while the model is still thinking or preparing its final report.
type TurnStepTracker struct {
	phase    TurnStepPhase
	sawReply bool
}

func (t *TurnStepTracker) Active() bool         { return t.phase != StepIdle }
func (t *TurnStepTracker) Phase() TurnStepPhase { return t.phase }
func (t *TurnStepTracker) SawReply() bool       { return t.sawReply }

func (t *TurnStepTracker) Reset() {
	t.phase = StepIdle
	t.sawReply = false
}

// NoteReplyChunk records assistant text, reasoning, or plan output.
func (t *TurnStepTracker) NoteReplyChunk() {
	t.sawReply = true
	t.phase = StepStreamingReply
}

// NoteToolActive records a foreground tool call starting or still running.
func (t *TurnStepTracker) NoteToolActive() {
	t.phase = StepExecutingTools
}

// NoteToolCompleted records a foreground tool call finishing. An LLM agent loop
// never legally ends on a raw tool result without a subsequent model step, so
// any earlier pre-tool usage state is cleared and the tracker waits for the
// post-tool model inference step.
func (t *TurnStepTracker) NoteToolCompleted() {
	t.phase = StepAwaitingPostToolModel
}

// NoteUsageOrEnd records an end-of-step usage report or explicit idle signal.
func (t *TurnStepTracker) NoteUsageOrEnd() {
	if t.phase == StepIdle {
		return
	}
	t.phase = StepModelResponded
}

// NextSettleDelay returns the timer duration before the current out-of-turn
// segment may settle, or shouldArm=false when settlement must remain blocked
// (e.g. while foreground tools or server requests are still open).
func (t *TurnStepTracker) NextSettleDelay(hasOpenToolsOrReqs bool, cfg TurnStepConfig) (time.Duration, bool) {
	if t.phase == StepIdle || hasOpenToolsOrReqs {
		return 0, false
	}
	switch t.phase {
	case StepExecutingTools, StepAwaitingPostToolModel:
		return cfg.postToolModelWait(), true
	case StepStreamingReply:
		return cfg.streamingQuiet(), true
	case StepModelResponded:
		return cfg.debounce(), true
	default:
		return 0, false
	}
}

var (
	daemonPkgServerRE = regexp.MustCompile(`(?i)(?:^|[;&|]\s*|\benv\s+\S+=\S+\s+)(?:npm|pnpm|yarn|bun|deno)\s+(?:--\S+\s+)*(?:run\s+)?(?:dev|serve|preview|start)(?:\s|$)`)
	daemonFrameworkRE = regexp.MustCompile(`(?i)(?:^|[;&|]\s*)(?:npx\s+)?(?:vite(?:\s+dev|\s+preview)?|next\s+dev|nuxt\s+dev|astro\s+dev|webpack\s+serve|ng\s+serve|flask\s+run|uvicorn\b[^\n;]*--reload|rails\s+(?:s|server)|php\s+-S\s+\S+|python3?\s+-m\s+http\.server|ngrok\s+http|cloudflared\s+tunnel|nodemon|cargo\s+watch|air)(?:\s|$)`)
	daemonWatchFlagRE = regexp.MustCompile(`(?i)\b(?:tsc|vitest|jest|webpack|rollup|esbuild|tailwind(?:css)?)\b[^\n;]*(?:\s--watch\b|\s-w\b)`)
	daemonTailRE      = regexp.MustCompile(`(?i)(?:^|[;&|]\s*)tail\s+(?:-[A-Za-z0-9]*f\b|--follow\b)`)
	daemonComposeUpRE = regexp.MustCompile(`(?i)(?:^|[;&|]\s*)docker(?:\s+compose|-compose)\s+up\b`)
	composeDetachRE   = regexp.MustCompile(`(?i)(?:\s-d\b|\s--detach\b|\s--abort-on-container-exit\b|\s--exit-code-from\b)`)
	oneShotTestBuild  = regexp.MustCompile(`(?i)\b(?:start-server-and-test|xcodebuild|go\s+test|cargo\s+test|pytest)\b`)
)

// IsDaemonCommand conservatively identifies long-running server/watcher shell
// commands for providers whose background-bash tool lacks an explicit is_daemon
// boolean (such as Claude Code or generic ACP Bash(run_in_background=true)).
// Finite build, test, or script commands return false so they remain Awaited.
func IsDaemonCommand(command, title string) bool {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return false
	}
	if oneShotTestBuild.MatchString(cmd) {
		return false
	}
	if daemonPkgServerRE.MatchString(cmd) ||
		daemonFrameworkRE.MatchString(cmd) ||
		daemonWatchFlagRE.MatchString(cmd) ||
		daemonTailRE.MatchString(cmd) {
		return true
	}
	if daemonComposeUpRE.MatchString(cmd) && !composeDetachRE.MatchString(cmd) {
		return true
	}
	lowerTitle := strings.ToLower(strings.TrimSpace(title))
	if strings.Contains(lowerTitle, "dev server") || strings.Contains(lowerTitle, "watch server") {
		return true
	}
	return false
}
