// Package kernel implements the Hermesh v2 Machine Thread Orchestration Engine.
package kernel

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	threadv2 "github.com/hermeshai/hermesh/gen/go/hermesh/thread/v2"
	"github.com/hermeshai/hermesh/internal/runtime"
)

type TransitionAction string

const (
	TransitionReuseInPlace      TransitionAction = "reuse_in_place"
	TransitionSwitchInSession   TransitionAction = "switch_in_session"
	TransitionSpawnAndResume    TransitionAction = "spawn_and_resume"
	TransitionSwitchWithHandoff TransitionAction = "switch_with_handoff"
)

type TransitionInput struct {
	CurrentBinding      *threadv2.ProviderThread
	LiveLease           *runtime.LeaseEntry
	NextSelection       *threadv2.ModelSelection
	NextEffectiveMode   identityv1.RuntimeMode
	NextInteractionMode threadv2.InteractionMode
	FreshContext        bool
	Capabilities        *threadv2.ProviderCapabilities
}

type TransitionPlan struct {
	Action          TransitionAction
	ModelChanged    bool
	OptionsChanged  bool
	ModeChanged     bool
	HandoffStrategy string
}

// SelectionsEqual compares two ModelSelections canonically.
func SelectionsEqual(a, b *threadv2.ModelSelection) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.InstanceId != b.InstanceId || a.Model != b.Model {
		return false
	}
	return OptionsEqual(a.Options, b.Options)
}

func OptionsEqual(a, b map[string]*threadv2.OptionValue) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || !optionValueEqual(va, vb) {
			return false
		}
	}
	return true
}

func optionValueEqual(a, b *threadv2.OptionValue) bool {
	if a == nil || b == nil {
		return a == b
	}
	switch xa := a.Kind.(type) {
	case *threadv2.OptionValue_StringValue:
		xb, ok := b.Kind.(*threadv2.OptionValue_StringValue)
		return ok && xa.StringValue == xb.StringValue
	case *threadv2.OptionValue_BoolValue:
		xb, ok := b.Kind.(*threadv2.OptionValue_BoolValue)
		return ok && xa.BoolValue == xb.BoolValue
	default:
		return false
	}
}

// DecideTransition is a pure deterministic policy function governing how a
// Thread transitions between models, options, modes, and provider instances.
func DecideTransition(in TransitionInput) TransitionPlan {
	caps := in.Capabilities
	if caps == nil {
		caps = &threadv2.ProviderCapabilities{}
	}

	// 1. Fresh context explicitly requested -> brand new provider thread (no handoff).
	if in.FreshContext {
		return TransitionPlan{Action: TransitionSwitchWithHandoff, HandoffStrategy: "fresh_context"}
	}

	// 2. First turn on a brand new thread -> spawn initial provider thread.
	if in.CurrentBinding == nil {
		return TransitionPlan{Action: TransitionSpawnAndResume}
	}

	// 3. Switching to a different provider instance -> cross-provider handoff.
	if in.CurrentBinding.InstanceId != in.NextSelection.GetInstanceId() {
		return TransitionPlan{
			Action:          TransitionSwitchWithHandoff,
			ModelChanged:    true,
			HandoffStrategy: "cross_provider_summary",
		}
	}

	// 4. Same provider instance, check if live OS session is alive.
	if in.LiveLease != nil && in.LiveLease.Session != nil && in.LiveLease.Session.Alive() {
		prevSel := in.LiveLease.Selection
		modelChanged := prevSel != nil && prevSel.Model != in.NextSelection.GetModel()
		optsChanged := prevSel != nil && !OptionsEqual(prevSel.Options, in.NextSelection.GetOptions())
		modeChanged := in.LiveLease.RuntimeMode != in.NextEffectiveMode || in.LiveLease.InteractionMode != in.NextInteractionMode

		if !modelChanged && !optsChanged && !modeChanged {
			return TransitionPlan{Action: TransitionReuseInPlace}
		}

		canHotModel := !modelChanged || caps.InSessionModelSwitch
		canHotOpts := !optsChanged || caps.InSessionOptionSwitch
		canHotMode := !modeChanged || caps.InSessionModeSwitch
		if canHotModel && canHotOpts && canHotMode {
			return TransitionPlan{
				Action:         TransitionSwitchInSession,
				ModelChanged:   modelChanged,
				OptionsChanged: optsChanged,
				ModeChanged:    modeChanged,
			}
		}
		// Must cycle the live process; resume natively if supported, else handoff.
		if caps.NativeResume && in.CurrentBinding.ResumeCursor != "" {
			return TransitionPlan{
				Action:         TransitionSpawnAndResume,
				ModelChanged:   modelChanged,
				OptionsChanged: optsChanged,
				ModeChanged:    modeChanged,
			}
		}
		return TransitionPlan{
			Action:          TransitionSwitchWithHandoff,
			ModelChanged:    modelChanged,
			OptionsChanged:  optsChanged,
			ModeChanged:     modeChanged,
			HandoffStrategy: "restart_handoff_summary",
		}
	}

	// 5. Process is not currently running (reaped by IdleTTL or after daemon restart).
	if caps.NativeResume && in.CurrentBinding.ResumeCursor != "" {
		return TransitionPlan{Action: TransitionSpawnAndResume}
	}
	return TransitionPlan{
		Action:          TransitionSwitchWithHandoff,
		HandoffStrategy: "unresumable_session_handoff",
	}
}

const defaultHandoffBudgetChars = 24000

// BuildContextHandoff compiles prior thread items and file changes into a
// token-budgeted structured briefing when switching providers or recovering
// a non-resumable session.
func BuildContextHandoff(items []*threadv2.TurnItem, checkpoints []*threadv2.Checkpoint, maxChars int) string {
	if len(items) == 0 {
		return ""
	}
	if maxChars <= 0 {
		maxChars = defaultHandoffBudgetChars
	}

	var latestPlan string
	var entries []string
	for _, it := range items {
		switch it.Kind {
		case threadv2.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE:
			if strings.TrimSpace(it.Text) != "" {
				entries = append(entries, fmt.Sprintf("[User]: %s", truncateMiddle(strings.TrimSpace(it.Text), 2500)))
			}
		case threadv2.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE:
			if strings.TrimSpace(it.Text) != "" {
				entries = append(entries, fmt.Sprintf("[Assistant]: %s", truncateMiddle(strings.TrimSpace(it.Text), 3000)))
			}
		case threadv2.TurnItemKind_TURN_ITEM_KIND_PROPOSED_PLAN:
			latestPlan = it.Text
		case threadv2.TurnItemKind_TURN_ITEM_KIND_FILE_CHANGE:
			if it.FilePath != "" {
				entries = append(entries, fmt.Sprintf("[Edited File]: %s (+%d -%d)", it.FilePath, it.Additions, it.Deletions))
			}
		case threadv2.TurnItemKind_TURN_ITEM_KIND_COMMAND_EXECUTION:
			if it.Command != "" {
				entries = append(entries, fmt.Sprintf("[Ran Command]: %s (exit %d)", it.Command, it.ExitCode))
			}
		}
	}
	if len(entries) == 0 && latestPlan == "" {
		return ""
	}

	// Keep the newest entries within budget.
	var kept []string
	used := 0
	for i := len(entries) - 1; i >= 0; i-- {
		if used+len(entries[i])+2 > maxChars*3/4 && len(kept) >= 4 {
			kept = append(kept, fmt.Sprintf("... (%d earlier items omitted for context budget) ...", i+1))
			break
		}
		kept = append(kept, entries[i])
		used += len(entries[i]) + 2
	}
	slices.Reverse(kept)

	changedSet := map[string]bool{}
	for _, cp := range checkpoints {
		for _, f := range cp.Files {
			changedSet[f.Path] = true
		}
	}
	changedFiles := slices.Sorted(maps.Keys(changedSet))

	var b strings.Builder
	b.WriteString("<context_handoff>\n")
	b.WriteString("This thread is continuing from a previous agent session. Use the context below seamlessly without repeating work already completed.\n\n")
	if len(changedFiles) > 0 {
		b.WriteString("Modified files in workspace:\n- ")
		b.WriteString(strings.Join(changedFiles, "\n- "))
		b.WriteString("\n\n")
	}
	if latestPlan != "" {
		b.WriteString("Latest approved/proposed plan:\n")
		b.WriteString(truncateMiddle(latestPlan, maxChars/4))
		b.WriteString("\n\n")
	}
	b.WriteString("Conversation & action history:\n")
	b.WriteString(strings.Join(kept, "\n"))
	b.WriteString("\n</context_handoff>")
	return b.String()
}

func truncateMiddle(s string, maxLen int) string {
	if len(s) <= maxLen || maxLen < 64 {
		return s
	}
	half := (maxLen - 24) / 2
	return s[:half] + "\n...[truncated]...\n" + s[len(s)-half:]
}
