// Package kernel implements the unified Hermesh Machine Session Orchestration Engine.
package kernel

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
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
	CurrentBinding      *sessionv1.ProviderSession
	LiveLease           *runtime.LeaseEntry
	NextSelection       *sessionv1.ModelSelection
	NextEffectiveMode   identityv1.RuntimeMode
	NextInteractionMode sessionv1.InteractionMode
	FreshContext        bool
	Capabilities        *sessionv1.ProviderCapabilities
}

type TransitionPlan struct {
	Action          TransitionAction
	ModelChanged    bool
	OptionsChanged  bool
	ModeChanged     bool
	HandoffStrategy string
}

// SelectionsEqual compares two ModelSelections canonically.
func SelectionsEqual(a, b *sessionv1.ModelSelection) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.InstanceId != b.InstanceId || a.Model != b.Model {
		return false
	}
	return OptionsEqual(a.Options, b.Options)
}

func OptionsEqual(a, b map[string]*sessionv1.OptionValue) bool {
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

func optionValueEqual(a, b *sessionv1.OptionValue) bool {
	if a == nil || b == nil {
		return a == b
	}
	switch xa := a.Kind.(type) {
	case *sessionv1.OptionValue_StringValue:
		xb, ok := b.Kind.(*sessionv1.OptionValue_StringValue)
		return ok && xa.StringValue == xb.StringValue
	case *sessionv1.OptionValue_BoolValue:
		xb, ok := b.Kind.(*sessionv1.OptionValue_BoolValue)
		return ok && xa.BoolValue == xb.BoolValue
	default:
		return false
	}
}

// DecideTransition is a pure deterministic policy function governing how a
// Session transitions between models, options, modes, and provider instances.
func DecideTransition(in TransitionInput) TransitionPlan {
	caps := in.Capabilities
	if caps == nil {
		caps = &sessionv1.ProviderCapabilities{}
	}

	if in.FreshContext {
		return TransitionPlan{Action: TransitionSwitchWithHandoff, HandoffStrategy: "fresh_context"}
	}

	if in.CurrentBinding == nil {
		return TransitionPlan{Action: TransitionSpawnAndResume}
	}

	if in.CurrentBinding.InstanceId != in.NextSelection.GetInstanceId() {
		return TransitionPlan{
			Action:          TransitionSwitchWithHandoff,
			ModelChanged:    true,
			HandoffStrategy: "cross_provider_summary",
		}
	}

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

	if caps.NativeResume && in.CurrentBinding.ResumeCursor != "" {
		return TransitionPlan{Action: TransitionSpawnAndResume}
	}
	return TransitionPlan{
		Action:          TransitionSwitchWithHandoff,
		HandoffStrategy: "unresumable_session_handoff",
	}
}

const defaultHandoffBudgetChars = 24000

// BuildContextHandoff compiles prior session items and file changes into a
// token-budgeted structured briefing when switching providers or recovering
// a non-resumable session.
func BuildContextHandoff(items []*sessionv1.TurnItem, checkpoints []*sessionv1.Checkpoint, maxChars int) string {
	return BuildContextHandoffWithDetails(items, checkpoints, nil, nil, maxChars)
}

// BuildContextHandoffWithDetails extends BuildContextHandoff with resolved
// RuntimeRequest answers and local attachment blob paths when available.
func BuildContextHandoffWithDetails(items []*sessionv1.TurnItem, checkpoints []*sessionv1.Checkpoint, requests []*sessionv1.RuntimeRequest, blobPath func(sha string) string, maxChars int) string {
	if len(items) == 0 {
		return ""
	}
	if maxChars <= 0 {
		maxChars = defaultHandoffBudgetChars
	}

	reqByID := map[string]*sessionv1.RuntimeRequest{}
	for _, r := range requests {
		if r != nil && r.RequestId != "" {
			reqByID[r.RequestId] = r
		}
	}

	var latestPlan string
	var entries []string
	for _, it := range items {
		switch it.Kind {
		case sessionv1.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE:
			var parts []string
			if strings.TrimSpace(it.Text) != "" {
				parts = append(parts, truncateMiddle(strings.TrimSpace(it.Text), 2500))
			}
			for _, a := range it.Attachments {
				if a == nil {
					continue
				}
				p := ""
				if blobPath != nil && a.BlobSha256 != "" {
					p = blobPath(a.BlobSha256)
				}
				if p != "" {
					parts = append(parts, fmt.Sprintf("[Attached file: %s (%s)]", a.Name, p))
				} else if a.Name != "" {
					parts = append(parts, fmt.Sprintf("[Attached file: %s]", a.Name))
				}
			}
			if len(parts) > 0 {
				entries = append(entries, "[User]: "+strings.Join(parts, "\n"))
			}
		case sessionv1.TurnItemKind_TURN_ITEM_KIND_MESSAGE_RECEIVED:
			if strings.TrimSpace(it.Text) != "" {
				entries = append(entries, fmt.Sprintf("[Message from %s]: %s", it.MailAddress, truncateMiddle(strings.TrimSpace(it.Text), 2000)))
			}
		case sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE:
			if strings.TrimSpace(it.Text) != "" {
				entries = append(entries, fmt.Sprintf("[Assistant]: %s", truncateMiddle(strings.TrimSpace(it.Text), 3000)))
			}
		case sessionv1.TurnItemKind_TURN_ITEM_KIND_PROPOSED_PLAN:
			latestPlan = it.Text
		case sessionv1.TurnItemKind_TURN_ITEM_KIND_FILE_CHANGE:
			if it.FilePath != "" {
				entries = append(entries, fmt.Sprintf("[Edited File]: %s (+%d -%d)", it.FilePath, it.Additions, it.Deletions))
			}
		case sessionv1.TurnItemKind_TURN_ITEM_KIND_COMMAND_EXECUTION:
			if it.Command != "" {
				line := fmt.Sprintf("[Ran Command]: %s (exit %d)", it.Command, it.ExitCode)
				if it.ExitCode != 0 && strings.TrimSpace(it.Output) != "" {
					line += "\n" + truncateMiddle(strings.TrimSpace(it.Output), 800)
				}
				entries = append(entries, line)
			}
		case sessionv1.TurnItemKind_TURN_ITEM_KIND_RUNTIME_REQUEST:
			if req := reqByID[it.RequestId]; req != nil &&
				req.Kind == sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT &&
				req.QuestionResolution == sessionv1.QuestionResolution_QUESTION_RESOLUTION_ANSWERED {
				if qa := formatAnsweredRequest(req); qa != "" {
					entries = append(entries, qa)
				}
			}
		}
	}
	if len(entries) == 0 && latestPlan == "" {
		return ""
	}

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
	b.WriteString("[Hermesh] The earlier conversation is summarized below. Continue seamlessly without repeating work already completed.\n\n")
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
	return strings.ToValidUTF8(b.String(), "")
}

func formatAnsweredRequest(req *sessionv1.RuntimeRequest) string {
	var lines []string
	qs := req.GetUserInput().GetQuestions()
	qByID := map[string]string{}
	for _, q := range qs {
		label := cmp.Or(strings.TrimSpace(q.Text), strings.TrimSpace(q.Header), q.Id)
		qByID[q.Id] = label
		if a := req.ResolvedAnswers[q.Id]; a != nil {
			vals := slices.Clone(a.SelectedOptions)
			if a.CustomText != "" {
				vals = append(vals, a.CustomText)
			}
			if len(vals) > 0 {
				lines = append(lines, fmt.Sprintf("[Q&A]: %s -> %s", truncateMiddle(label, 300), truncateMiddle(strings.Join(vals, ", "), 500)))
			}
		}
	}
	for qid, a := range req.ResolvedAnswers {
		if _, seen := qByID[qid]; seen || a == nil {
			continue
		}
		vals := slices.Clone(a.SelectedOptions)
		if a.CustomText != "" {
			vals = append(vals, a.CustomText)
		}
		if len(vals) > 0 {
			label := qid
			if len(qs) == 1 && len(req.ResolvedAnswers) == 1 {
				label = cmp.Or(strings.TrimSpace(qs[0].Text), strings.TrimSpace(qs[0].Header), qid)
			}
			lines = append(lines, fmt.Sprintf("[Q&A]: %s -> %s", truncateMiddle(label, 300), truncateMiddle(strings.Join(vals, ", "), 500)))
		}
	}
	return strings.Join(lines, "\n")
}

func truncateMiddle(s string, maxLen int) string {
	s = strings.ToValidUTF8(s, "")
	if len(s) <= maxLen || maxLen < 64 {
		return s
	}
	half := (maxLen - 24) / 2
	head := half
	for head > 0 && !utf8.RuneStart(s[head]) {
		head--
	}
	tail := len(s) - half
	for tail < len(s) && !utf8.RuneStart(s[tail]) {
		tail++
	}
	return s[:head] + "\n...[truncated]...\n" + s[tail:]
}
