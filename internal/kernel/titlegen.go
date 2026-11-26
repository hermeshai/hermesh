package kernel

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/kernel/store"
	"github.com/hermeshai/hermesh/internal/runtime"
)

type titleGenMode string

const (
	titleGenModeInitial    titleGenMode = "initial"
	titleGenModeRefine     titleGenMode = "refine"
	titleGenModeRegenerate titleGenMode = "regenerate"

	maxConcurrentTitleGens = 2
	primaryTitleGenTimeout = 8 * time.Second
	singleTitleGenTimeout  = 20 * time.Second
	fastRetryElapsedMax    = 4 * time.Second
	titleNoticeAlignWait   = 1500 * time.Millisecond
)

type titleRefineSnapshot struct {
	cp         *sessionv1.Checkpoint
	resultText string
}

func isTitleGenEnabled(v any) (runtime.TitleGenerator, bool) {
	if v == nil {
		return nil, false
	}
	tg, ok := v.(runtime.TitleGenerator)
	if !ok {
		return nil, false
	}
	if gate, ok := v.(runtime.TitleGeneratorEnabled); ok && !gate.TitleGeneratorEnabled() {
		return nil, false
	}
	return tg, true
}

func (e *Engine) hasTitleGenerator() bool {
	if _, ok := isTitleGenEnabled(e.cfg.PrimaryTitleGenerator); ok {
		return true
	}
	for _, drv := range e.cfg.Drivers {
		if _, ok := isTitleGenEnabled(drv); ok {
			return true
		}
	}
	return false
}

func (e *Engine) cancelTitleGen(sessionID string) {
	e.mu.Lock()
	cancel := e.titleCancels[sessionID]
	delete(e.titleCancels, sessionID)
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (e *Engine) waitTitleGen(sessionID string, maxWait time.Duration) {
	e.mu.Lock()
	done := e.titleDoneChs[sessionID]
	e.mu.Unlock()
	if done == nil {
		return
	}
	timer := time.NewTimer(maxWait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	case <-e.bgCtx.Done():
	}
}

func (e *Engine) spawnTitleGen(sessionID string, seq uint64, mode titleGenMode, snap *titleRefineSnapshot) {
	if !e.hasTitleGenerator() || e.bgCtx.Err() != nil {
		return
	}
	ctx, cancel := context.WithCancel(e.bgCtx)
	done := make(chan struct{})

	e.mu.Lock()
	if prev := e.titleCancels[sessionID]; prev != nil {
		prev()
	}
	e.titleCancels[sessionID] = cancel
	e.titleDoneChs[sessionID] = done
	e.mu.Unlock()

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer func() {
			cancel()
			close(done)
			e.mu.Lock()
			if e.titleDoneChs[sessionID] == done {
				delete(e.titleDoneChs, sessionID)
				delete(e.titleCancels, sessionID)
			}
			e.mu.Unlock()
		}()
		e.runTitleGen(ctx, sessionID, seq, mode, snap)
	}()
}

// maybeRefineTitleBeforeTurnEnd aligns Pass 1 / Pass 2 title generation with
// Turn 1 completion so lock-screen push notices and <session_news> carry the
// refined title whenever generation finishes within titleNoticeAlignWait.
func (e *Engine) maybeRefineTitleBeforeTurnEnd(sessionID, runID string, cp *sessionv1.Checkpoint, resultText string) {
	r, err := e.st.Run(runID)
	if err != nil || r.Ordinal != 1 {
		return
	}
	// If Pass 1 is still in flight on Turn 1, give it a brief window first.
	e.waitTitleGen(sessionID, titleNoticeAlignWait)

	sess, err := e.st.Session(sessionID)
	if err != nil || sess.TitleRenamed || sess.TitleState != sessionv1.TitleState_TITLE_STATE_PROVISIONAL {
		return
	}
	if !e.hasTitleGenerator() {
		return
	}

	var seq uint64
	unlock := e.lockSession(sessionID)
	events, mErr := e.st.Mutate(func(tx *store.Tx) error {
		sNow, err := tx.GetSession(sessionID)
		if err != nil {
			return err
		}
		if sNow.TitleRenamed || sNow.TitleState != sessionv1.TitleState_TITLE_STATE_PROVISIONAL {
			return nil
		}
		sNow.TitleSeq++
		sNow.TitleGenerating = true
		seq = sNow.TitleSeq
		return putSessionSynced(tx, sNow)
	})
	unlock()
	if mErr != nil || seq == 0 {
		return
	}
	e.publish(events, nil)
	e.spawnTitleGen(sessionID, seq, titleGenModeRefine, &titleRefineSnapshot{
		cp:         cp,
		resultText: resultText,
	})
	e.waitTitleGen(sessionID, titleNoticeAlignWait)
}

func (e *Engine) runTitleGen(ctx context.Context, sessionID string, expectedSeq uint64, mode titleGenMode, snap *titleRefineSnapshot) {
	select {
	case e.titleSem <- struct{}{}:
		defer func() { <-e.titleSem }()
	case <-ctx.Done():
		return
	}

	req, sessSnap, ok := e.buildTitleGenRequest(sessionID, expectedSeq, mode, snap)
	if !ok {
		e.clearTitleGenerating(sessionID, expectedSeq, mode)
		return
	}

	maxAttempts := 1
	if mode == titleGenModeInitial {
		maxAttempts = 2
	}
	var (
		res     runtime.TitleGenResult
		lastErr error
	)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
		attemptStart := time.Now()
		res, lastErr = e.invokeTitleGenerators(ctx, sessSnap, req)
		if lastErr == nil {
			break
		}
		// Only retry immediately on fast transient failures; if the first attempt
		// already spent seconds cold-starting or timed out, yield now and let
		// Turn 1 completion refine via TITLE_STATE_PROVISIONAL.
		if time.Since(attemptStart) >= fastRetryElapsedMax {
			break
		}
	}
	if ctx.Err() != nil {
		return
	}
	if lastErr != nil {
		e.log.Debug("session title generation skipped after error", "session", sessionID, "mode", mode, "err", lastErr)
		e.clearTitleGenerating(sessionID, expectedSeq, mode)
		return
	}

	var newTitle string
	unlock := e.lockSession(sessionID)
	events, err := e.st.Mutate(func(tx *store.Tx) error {
		sNow, err := tx.GetSession(sessionID)
		if err != nil {
			return err
		}
		if sNow.TitleSeq != expectedSeq || sNow.TitleRenamed || sNow.TitleState == sessionv1.TitleState_TITLE_STATE_LOCKED {
			return nil
		}
		sNow.TitleGenerating = false
		trimmed := strings.TrimSpace(res.Title)
		if trimmed != "" && !runtime.IsPlaceholderSeedTitle(trimmed) {
			sNow.Title = trimmed
			newTitle = trimmed
		}
		if mode == titleGenModeInitial && res.Provisional {
			sNow.TitleState = sessionv1.TitleState_TITLE_STATE_PROVISIONAL
		} else {
			sNow.TitleState = sessionv1.TitleState_TITLE_STATE_FINAL
		}
		if err := putSessionSynced(tx, sNow); err != nil {
			return err
		}
		return tx.AppendEvent(&sessionv1.SessionEvent{
			SessionId: sessionID,
			Type:      "session.updated",
			Session:   sNow,
		})
	})
	unlock()
	if err != nil {
		return
	}
	e.publish(events, nil)

	if newTitle != "" {
		if sess, sErr := e.st.Session(sessionID); sErr == nil && sess.ActiveProviderSessionId != "" {
			if lease := e.sup.Get(sess.ActiveProviderSessionId); lease != nil {
				if titler, ok := lease.Session.(runtime.Titler); ok {
					tctx, cancel := context.WithTimeout(ctx, 3*time.Second)
					_ = titler.SetTitle(tctx, newTitle)
					cancel()
				}
			}
		}
	}
}

func (e *Engine) clearTitleGenerating(sessionID string, expectedSeq uint64, mode titleGenMode) {
	unlock := e.lockSession(sessionID)
	events, err := e.st.Mutate(func(tx *store.Tx) error {
		sNow, err := tx.GetSession(sessionID)
		if err != nil {
			return err
		}
		if sNow.TitleSeq != expectedSeq || !sNow.TitleGenerating {
			return nil
		}
		sNow.TitleGenerating = false
		if mode == titleGenModeInitial && sNow.TitleState == sessionv1.TitleState_TITLE_STATE_SEED {
			// Keep PROVISIONAL so Turn 1 completion automatically retries once
			// after cold-start contention subsides instead of locking a raw seed.
			sNow.TitleState = sessionv1.TitleState_TITLE_STATE_PROVISIONAL
		} else if mode == titleGenModeRefine && sNow.TitleState == sessionv1.TitleState_TITLE_STATE_PROVISIONAL {
			sNow.TitleState = sessionv1.TitleState_TITLE_STATE_FINAL
		}
		if err := putSessionSynced(tx, sNow); err != nil {
			return err
		}
		return tx.AppendEvent(&sessionv1.SessionEvent{
			SessionId: sessionID,
			Type:      "session.updated",
			Session:   sNow,
		})
	})
	unlock()
	if err == nil {
		e.publish(events, nil)
	}
}

func (e *Engine) hintSmallModelForInstance(inst *sessionv1.ProviderInstanceConfig) string {
	if inst == nil || e.cfg.CachedAgentModels == nil {
		return ""
	}
	key := cmp.Or(inst.AcpFlavor, inst.InstanceId, inst.Driver)
	models := e.cfg.CachedAgentModels(key)
	if len(models) == 0 && inst.Driver != "" && inst.Driver != key {
		models = e.cfg.CachedAgentModels(inst.Driver)
	}
	if len(models) == 0 {
		return ""
	}
	id, _ := runtime.PickSmallModelID(models)
	if inst.Driver == "claude" && id != "" && !strings.Contains(strings.ToLower(id), "haiku") {
		return "haiku"
	}
	return id
}

func (e *Engine) invokeTitleGenerators(ctx context.Context, sess *sessionv1.Session, req runtime.TitleGenRequest) (runtime.TitleGenResult, error) {
	var lastErr error
	if primary, ok := isTitleGenEnabled(e.cfg.PrimaryTitleGenerator); ok {
		pctx, cancel := context.WithTimeout(ctx, primaryTitleGenTimeout)
		res, err := primary.GenerateTitle(pctx, nil, req)
		cancel()
		if err == nil {
			return res, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return runtime.TitleGenResult{}, ctx.Err()
		}
	}

	type candidate struct {
		drv runtime.TitleGenerator
		cfg *sessionv1.ProviderInstanceConfig
	}
	var list []candidate
	seen := map[string]bool{}

	addInstance := func(inst *sessionv1.ProviderInstanceConfig) bool {
		if inst == nil || !inst.Enabled || seen[inst.InstanceId] {
			return false
		}
		tg, ok := isTitleGenEnabled(e.cfg.Drivers[inst.Driver])
		if !ok {
			return false
		}
		seen[inst.InstanceId] = true
		list = append(list, candidate{drv: tg, cfg: inst})
		return true
	}

	allInstances, _ := e.st.ProviderInstances()
	primaryID := cmp.Or(sess.GetModelSelection().GetInstanceId(), sess.Agent)
	primaryAdded := false
	if primaryID != "" {
		found := false
		for _, inst := range allInstances {
			if inst.InstanceId == primaryID {
				primaryAdded = addInstance(inst)
				found = true
				break
			}
		}
		if !found {
			if tg, ok := isTitleGenEnabled(e.cfg.Drivers[primaryID]); ok {
				seen[primaryID] = true
				list = append(list, candidate{drv: tg, cfg: &sessionv1.ProviderInstanceConfig{InstanceId: primaryID, Driver: primaryID, Enabled: true}})
				primaryAdded = true
			}
		}
	}
	// Keep CLI fallback strictly on the session's own selected provider unless
	// that provider has no TitleGenerator implementation at all.
	if !primaryAdded {
		for _, inst := range allInstances {
			addInstance(inst)
		}
		for name, drv := range e.cfg.Drivers {
			if seen[name] {
				continue
			}
			if tg, ok := isTitleGenEnabled(drv); ok {
				seen[name] = true
				list = append(list, candidate{drv: tg, cfg: &sessionv1.ProviderInstanceConfig{InstanceId: name, Driver: name, Enabled: true}})
			}
		}
	}

	for _, c := range list {
		creq := req
		if creq.ModelHint == "" {
			creq.ModelHint = e.hintSmallModelForInstance(c.cfg)
		}
		cctx, cancel := context.WithTimeout(ctx, singleTitleGenTimeout)
		res, err := c.drv.GenerateTitle(cctx, c.cfg, creq)
		cancel()
		if err == nil {
			return res, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return runtime.TitleGenResult{}, ctx.Err()
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no title generator available")
	}
	return runtime.TitleGenResult{}, lastErr
}

func (e *Engine) buildTitleGenRequest(sessionID string, expectedSeq uint64, mode titleGenMode, snap *titleRefineSnapshot) (runtime.TitleGenRequest, *sessionv1.Session, bool) {
	sess, err := e.st.Session(sessionID)
	if err != nil || sess.TitleSeq != expectedSeq || sess.TitleRenamed || sess.TitleState == sessionv1.TitleState_TITLE_STATE_LOCKED {
		return runtime.TitleGenRequest{}, nil, false
	}
	locale := runtime.ResolveLocale(sess.TitleLocale)
	langName, lengthRule := runtime.LocalePromptSpec(locale)

	items, _ := e.st.TurnItems(sessionID)
	var userMsgs []string
	var firstAtts []*sessionv1.AttachmentRef
	var lastAssistant string
	var itemChangedFiles []string

	forkIdx := 0
	if sess.GetLineage().GetRelationship() == "fork" && sess.GetLineage().GetForkedFromRunOrdinal() > 0 {
		runs, _ := e.st.Runs(sessionID)
		inheritedRuns := map[string]bool{}
		for _, r := range runs {
			if r.Ordinal > 0 && r.Ordinal <= sess.GetLineage().GetForkedFromRunOrdinal() {
				inheritedRuns[r.RunId] = true
			}
		}
		for i, it := range items {
			if !inheritedRuns[it.RunId] && (it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE || it.Kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_MESSAGE_RECEIVED) {
				forkIdx = i
				break
			}
		}
	}

	relevantItems := items[forkIdx:]
	if len(relevantItems) == 0 {
		relevantItems = items
	}

	for _, it := range relevantItems {
		switch it.Kind {
		case sessionv1.TurnItemKind_TURN_ITEM_KIND_USER_MESSAGE, sessionv1.TurnItemKind_TURN_ITEM_KIND_MESSAGE_RECEIVED:
			if t := strings.TrimSpace(it.Text); t != "" {
				userMsgs = append(userMsgs, t)
			}
			if len(firstAtts) == 0 && len(it.Attachments) > 0 {
				firstAtts = it.Attachments
			}
		case sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE:
			if t := strings.TrimSpace(it.Text); t != "" {
				lastAssistant = t
			}
		case sessionv1.TurnItemKind_TURN_ITEM_KIND_FILE_CHANGE:
			if p := strings.TrimSpace(it.FilePath); p != "" && !slices.Contains(itemChangedFiles, p) {
				itemChangedFiles = append(itemChangedFiles, fmt.Sprintf("%s (+%d -%d)", p, it.Additions, it.Deletions))
			}
		}
	}
	if snap != nil && strings.TrimSpace(snap.resultText) != "" {
		lastAssistant = strings.TrimSpace(snap.resultText)
	}
	if len(userMsgs) == 0 && len(firstAtts) == 0 {
		if runs, rErr := e.st.Runs(sessionID); rErr == nil {
			for _, r := range runs {
				if strings.TrimSpace(r.Prompt) != "" {
					userMsgs = append(userMsgs, strings.TrimSpace(r.Prompt))
				}
				if len(firstAtts) == 0 && len(r.Attachments) > 0 {
					firstAtts = r.Attachments
				}
			}
		}
	}
	if len(userMsgs) == 0 && len(firstAtts) == 0 {
		return runtime.TitleGenRequest{}, nil, false
	}

	var touchedLines []string
	var latestCp *sessionv1.Checkpoint
	if snap != nil && snap.cp != nil {
		latestCp = snap.cp
	} else if cps, cErr := e.st.Checkpoints(sessionID); cErr == nil && len(cps) > 0 {
		latestCp = cps[len(cps)-1]
	}
	if latestCp != nil && len(latestCp.Files) > 0 {
		files := slices.Clone(latestCp.Files)
		slices.SortFunc(files, func(a, b *sessionv1.CheckpointFile) int {
			return cmp.Compare(int(b.Additions+b.Deletions), int(a.Additions+a.Deletions))
		})
		for i := 0; i < len(files) && i < 5; i++ {
			f := files[i]
			touchedLines = append(touchedLines, fmt.Sprintf("- %s (+%d -%d)", f.Path, f.Additions, f.Deletions))
		}
	} else {
		for i := 0; i < len(itemChangedFiles) && i < 5; i++ {
			touchedLines = append(touchedLines, "- "+itemChangedFiles[i])
		}
	}

	var nearby []string
	if all, lErr := e.st.ListSessions("", false, 8); lErr == nil {
		for _, other := range all {
			if other.SessionId == sessionID {
				continue
			}
			t := strings.TrimSpace(other.Title)
			if t == "" || runtime.IsPlaceholderSeedTitle(t) || slices.Contains(nearby, t) {
				continue
			}
			nearby = append(nearby, t)
			if len(nearby) >= 4 {
				break
			}
		}
	}

	var parentTitle string
	if sess.GetLineage().GetRelationship() == "fork" && sess.GetLineage().GetParentSessionId() != "" {
		if parent, pErr := e.st.Session(sess.GetLineage().GetParentSessionId()); pErr == nil {
			parentTitle = strings.TrimSpace(parent.Title)
		}
	}

	var b strings.Builder
	b.WriteString("Task title for sidebar. JSON only: {\"title\":string,\"provisional\":boolean}\n")
	fmt.Fprintf(&b, "Target locale: %s\nMode: %s\n", locale, mode)
	fmt.Fprintf(&b, "Rules: Strictly %s (%s), %s, no quotes/trailing punctuation, keep code/file/CLI identifiers intact; name subject+outcome, skip workflow chatter.", langName, locale, lengthRule)
	if mode == titleGenModeInitial {
		b.WriteString(" provisional=true ONLY when subject is unknown (e.g. \"fix this\" + screenshot/URL); else false.\n")
	} else {
		b.WriteString(" provisional=false.\n")
	}
	if parentTitle != "" {
		fmt.Fprintf(&b, "Forked from: %q (reflect new direction; omit \"fork\"/\"分支\").\n", parentTitle)
	}
	if (mode == titleGenModeRefine || mode == titleGenModeRegenerate) && strings.TrimSpace(sess.Title) != "" {
		fmt.Fprintf(&b, "Previous title: %q\n", sess.Title)
	}
	if len(nearby) > 0 {
		b.WriteString("Nearby active titles:\n")
		for _, nt := range nearby {
			fmt.Fprintf(&b, "- %s\n", nt)
		}
	}
	if len(touchedLines) > 0 {
		b.WriteString("\nTouched files:\n")
		b.WriteString(strings.Join(touchedLines, "\n") + "\n")
	}

	if mode == titleGenModeInitial {
		msg := ""
		if len(userMsgs) > 0 {
			msg = runtime.SliceHeadTail(userMsgs[0], 360)
		}
		b.WriteString("\nUser message:\n" + msg + "\n")
	} else {
		b.WriteString("\nConversation:\n")
		if len(userMsgs) > 0 {
			b.WriteString("USER (initial): " + runtime.SliceHeadTail(userMsgs[0], 300) + "\n")
			start := max(1, len(userMsgs)-2)
			for i := start; i < len(userMsgs); i++ {
				b.WriteString("USER (later): " + runtime.SliceHeadTail(userMsgs[i], 200) + "\n")
			}
		}
		if lastAssistant != "" {
			b.WriteString("ASSISTANT (latest): " + runtime.SliceHeadTail(lastAssistant, 240) + "\n")
		}
	}

	needImage := mode == titleGenModeInitial && len(firstAtts) > 0 && (len(userMsgs) == 0 || runtime.NeedsAttachmentForTitle(userMsgs[0]))
	if len(firstAtts) > 0 && (len(userMsgs) == 0 || needImage) {
		b.WriteString("\nAttachments:\n")
		for i, a := range firstAtts {
			if i >= 2 {
				break
			}
			fmt.Fprintf(&b, "- %s (%s)\n", a.Name, a.MimeType)
		}
	}

	var images []runtime.TurnAttachment
	if needImage {
		for _, a := range e.loadTurnAttachments(firstAtts) {
			if strings.HasPrefix(strings.ToLower(a.MimeType), "image/") {
				images = append(images, a)
				break
			}
		}
	}

	return runtime.TitleGenRequest{
		Prompt: b.String(),
		Images: images,
	}, proto.Clone(sess).(*sessionv1.Session), true
}
