package machine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/provider"
)

// DefaultQuestionTimeout is how long a question waits for an answer when
// the session does not say.
const DefaultQuestionTimeout = 30 * time.Minute

// What the agent is told when no answer comes. It must never get an empty
// answer: it would silently invent one.
const (
	declinedMessage   = "The user declined to answer. Do not wait for an answer; continue with your best judgment and state the assumption you made."
	noAnswerMessage   = "No one answered within %s. Do not wait for an answer; continue with your best judgment and state the assumption you made."
	noAnswerersPrompt = "No one can answer questions in this session. Continue with your best judgment and state the assumption you made."
)

// question is a question waiting for an answer.
type question struct {
	q     *sessionv1.Question
	ref   string // the provider's id
	ps    provider.Session
	since time.Time
}

// turnActive: a turn is under way (running, or paused on a question).
func turnActive(st sessionv1.SessionStatus) bool {
	return st == sessionv1.SessionStatus_SESSION_STATUS_RUNNING || st == sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT
}

// questionTimeout is the session's question timeout (0: nobody answers).
func (r *sessionRow) questionTimeout() time.Duration {
	if r.QuestionTimeout < 0 {
		return DefaultQuestionTimeout
	}
	return time.Duration(r.QuestionTimeout) * time.Second
}

// ask records the agent's question and pauses the turn on it.
func (s *Sessions) ask(l *live, ps provider.Session, pq *provider.Question) {
	now := time.Now()
	l.mu.Lock()
	l.questionSeq++
	q := &sessionv1.Question{QuestionId: fmt.Sprintf("q_%d_%d", l.turn, l.questionSeq), Turn: l.turn, AskedAt: timestamppb.New(now)}
	for _, it := range pq.Items {
		pi := &sessionv1.QuestionItem{Id: it.ID, Header: it.Header, Text: it.Text, MultiSelect: it.MultiSelect,
			AllowOther: it.AllowOther || len(it.Options) == 0, Secret: it.Secret, Optional: it.Optional}
		for _, o := range it.Options {
			pi.Options = append(pi.Options, &sessionv1.QuestionOption{Label: o.Label, Description: o.Description})
		}
		q.Items = append(q.Items, pi)
	}
	timeout := l.row.questionTimeout()
	if timeout > 0 {
		q.ExpiresAt = timestamppb.New(now.Add(timeout))
	}
	w := &question{q: q, ref: pq.ID, ps: ps, since: now}
	if timeout > 0 {
		l.questions = append(l.questions, w)
		l.row.Progress.PendingQuestion = l.questions[0].q
	}
	by, id := l.row.CreatedBy, l.row.ID
	l.mu.Unlock()
	s.emit(l, &sessionv1.SessionEvent{Kind: &sessionv1.SessionEvent_QuestionAsked{QuestionAsked: &sessionv1.QuestionAsked{Question: q}}})
	s.audit.Log(AuditRecord{Kind: "session.question", Subject: by, Session: id, Action: q.QuestionId, Decision: "ask", Detail: questionDetail(q)})
	if timeout == 0 {
		s.reply(l, w, provider.Reply{Declined: true, Message: noAnswerersPrompt}, sessionv1.QuestionResolution_QUESTION_RESOLUTION_TIMED_OUT, "hermesh", nil)
		return
	}
	l.mu.Lock()
	changed := s.setStatusLocked(l, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT)
	l.mu.Unlock()
	if changed {
		s.statusChanged(l, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT, "waiting for an answer")
	}
}

// reply sends a question's resolution to the agent; the turn goes on.
func (s *Sessions) reply(l *live, w *question, r provider.Reply, res sessionv1.QuestionResolution, by string, answers []*sessionv1.Answer) {
	now := time.Now()
	l.mu.Lock()
	for i, x := range l.questions {
		if x == w {
			l.questions = append(l.questions[:i], l.questions[i+1:]...)
			break
		}
	}
	l.row.Progress.PendingQuestion = nil
	if len(l.questions) > 0 {
		l.row.Progress.PendingQuestion = l.questions[0].q
	}
	// The wait does not count against the turn's time or liveness.
	l.turnStarted = l.turnStarted.Add(now.Sub(w.since))
	l.lastProgress = now
	var changed bool
	if len(l.questions) == 0 && l.row.Status == sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT {
		// Before the agent goes on: its turn may end right after.
		changed = s.setStatusLocked(l, sessionv1.SessionStatus_SESSION_STATUS_RUNNING)
	}
	sessionID := l.row.ID
	l.mu.Unlock()
	if changed {
		s.statusChanged(l, sessionv1.SessionStatus_SESSION_STATUS_RUNNING, "")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err := w.ps.Answer(ctx, w.ref, r)
	cancel()
	if err != nil {
		s.log.Warn("could not answer the agent's question", "session", sessionID, "question", w.q.QuestionId, "err", err)
	}
	s.emit(l, &sessionv1.SessionEvent{Kind: &sessionv1.SessionEvent_QuestionResolved{QuestionResolved: &sessionv1.QuestionResolved{
		QuestionId: w.q.QuestionId, Resolution: res, By: by, Answers: redactAnswers(w.q, answers)}}})
	s.audit.Log(AuditRecord{Kind: "session.question", Subject: by, Session: sessionID, Action: w.q.QuestionId,
		Decision: strings.ToLower(strings.TrimPrefix(res.String(), "QUESTION_RESOLUTION_")), Detail: answersDetail(answers)})
}

// dropQuestions forgets the questions of a turn that ended or stopped.
func (s *Sessions) dropQuestions(l *live) {
	l.mu.Lock()
	qs := l.questions
	l.questions = nil
	l.row.Progress.PendingQuestion = nil
	l.mu.Unlock()
	for _, w := range qs {
		s.emit(l, &sessionv1.SessionEvent{Kind: &sessionv1.SessionEvent_QuestionResolved{QuestionResolved: &sessionv1.QuestionResolved{
			QuestionId: w.q.QuestionId, Resolution: sessionv1.QuestionResolution_QUESTION_RESOLUTION_CANCELLED}}})
	}
}

// expireQuestions tells the agent to go on when nobody answered in time.
func (s *Sessions) expireQuestions(l *live, now time.Time) {
	l.mu.Lock()
	var due []*question
	for _, w := range l.questions {
		if e := w.q.ExpiresAt; e != nil && now.After(e.AsTime()) {
			due = append(due, w)
		}
	}
	timeout := l.row.questionTimeout()
	l.mu.Unlock()
	for _, w := range due {
		s.reply(l, w, provider.Reply{Declined: true, Message: fmt.Sprintf(noAnswerMessage, timeout)},
			sessionv1.QuestionResolution_QUESTION_RESOLUTION_TIMED_OUT, "hermesh", nil)
	}
}

// AnswerQuestion answers (or declines) the agent's pending question.
func (s *Sessions) AnswerQuestion(ctx context.Context, p *Principal, req *sessionv1.AnswerQuestionRequest) (*sessionv1.Session, error) {
	l, r, err := s.find(p, req.SessionId, false)
	if err != nil {
		return nil, err
	}
	// The owner, the session's creator, or whoever asked for the turn.
	if p.Kind != PrincipalOwner && !s.canWrite(p, r) && (l == nil || !l.askedBy(p.ID())) {
		return nil, cerr(connect.CodePermissionDenied, "only who asked for the turn may answer its question")
	}
	if l == nil {
		return nil, cerr(connect.CodeFailedPrecondition, "session has ended")
	}
	l.mu.Lock()
	var w *question
	for _, x := range l.questions {
		if req.QuestionId == "" || x.q.QuestionId == req.QuestionId {
			w = x
			break
		}
	}
	l.mu.Unlock()
	if w == nil {
		if req.QuestionId != "" {
			return nil, cerr(connect.CodeFailedPrecondition, "question %s is not waiting for an answer (answered, timed out, or its turn ended)", req.QuestionId)
		}
		return nil, cerr(connect.CodeFailedPrecondition, "the agent is not waiting for an answer")
	}
	if req.Decline {
		s.reply(l, w, provider.Reply{Declined: true, Message: declinedMessage}, sessionv1.QuestionResolution_QUESTION_RESOLUTION_DECLINED, p.ID(), nil)
	} else {
		answers, err := checkAnswers(w.q, req.Answers)
		if err != nil {
			return nil, err
		}
		s.reply(l, w, provider.Reply{Answers: providerAnswers(answers)}, sessionv1.QuestionResolution_QUESTION_RESOLUTION_ANSWERED, p.ID(), answers)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return s.toProto(l.row), nil
}

// answerWithText answers the pending question with a message's text (a
// message sent while the agent waits is its answer). false: no question.
func (s *Sessions) answerWithText(l *live, by, text string) bool {
	l.mu.Lock()
	if len(l.questions) == 0 {
		l.mu.Unlock()
		return false
	}
	w := l.questions[0]
	l.mu.Unlock()
	var answers []*sessionv1.Answer
	for _, it := range w.q.Items {
		a := &sessionv1.Answer{ItemId: it.Id, Text: text}
		for _, o := range it.Options { // the reply names an option
			if strings.EqualFold(strings.TrimSpace(text), o.Label) {
				a.Selected, a.Text = []string{o.Label}, ""
			}
		}
		answers = append(answers, a)
	}
	s.reply(l, w, provider.Reply{Answers: providerAnswers(answers)}, sessionv1.QuestionResolution_QUESTION_RESOLUTION_ANSWERED, by, answers)
	return true
}

// checkAnswers validates answers against the question: every item is
// answered, options are named as offered (any case), free text only where
// accepted.
func checkAnswers(q *sessionv1.Question, given []*sessionv1.Answer) ([]*sessionv1.Answer, error) {
	byID := map[string]*sessionv1.Answer{}
	for _, a := range given {
		byID[a.ItemId] = a
	}
	if len(q.Items) == 1 && len(given) == 1 && given[0].ItemId == "" {
		byID[q.Items[0].Id] = given[0]
	}
	var out []*sessionv1.Answer
	for _, it := range q.Items {
		a := byID[it.Id]
		if a == nil {
			for _, x := range given { // by header, as the CLI lets people name them
				if x.ItemId != "" && strings.EqualFold(x.ItemId, it.Header) {
					a = x
				}
			}
		}
		if a == nil || (len(a.Selected) == 0 && strings.TrimSpace(a.Text) == "") {
			if it.Optional {
				continue
			}
			return nil, cerr(connect.CodeInvalidArgument, "no answer for %q (%s)", it.Header, it.Id)
		}
		if len(a.Selected) > 1 && !it.MultiSelect {
			return nil, cerr(connect.CodeInvalidArgument, "%q takes one option", it.Header)
		}
		if a.Text != "" && !it.AllowOther {
			return nil, cerr(connect.CodeInvalidArgument, "%q takes one of its options, not free text", it.Header)
		}
		ans := &sessionv1.Answer{ItemId: it.Id, Text: strings.TrimSpace(a.Text)}
		for _, sel := range a.Selected {
			label := ""
			for _, o := range it.Options {
				if strings.EqualFold(strings.TrimSpace(sel), o.Label) {
					label = o.Label
				}
			}
			if label == "" {
				return nil, cerr(connect.CodeInvalidArgument, "%q has no option %q (options: %s)", it.Header, sel, optionLabels(it))
			}
			ans.Selected = append(ans.Selected, label)
		}
		out = append(out, ans)
	}
	return out, nil
}

func optionLabels(it *sessionv1.QuestionItem) string {
	var l []string
	for _, o := range it.Options {
		l = append(l, o.Label)
	}
	return strings.Join(l, ", ")
}

func providerAnswers(as []*sessionv1.Answer) []provider.Answer {
	var out []provider.Answer
	for _, a := range as {
		out = append(out, provider.Answer{ItemID: a.ItemId, Selected: a.Selected, Text: a.Text})
	}
	return out
}

// redactAnswers hides the answers to secret items.
func redactAnswers(q *sessionv1.Question, as []*sessionv1.Answer) []*sessionv1.Answer {
	secret := map[string]bool{}
	for _, it := range q.Items {
		secret[it.Id] = it.Secret
	}
	var out []*sessionv1.Answer
	for _, a := range as {
		if secret[a.ItemId] {
			a = &sessionv1.Answer{ItemId: a.ItemId, Text: "[secret]"}
		}
		out = append(out, a)
	}
	return out
}

func questionDetail(q *sessionv1.Question) string {
	var b strings.Builder
	for _, it := range q.Items {
		fmt.Fprintf(&b, " item=%s:%s", it.Id, sha(it.Text))
	}
	return strings.TrimSpace(b.String())
}

func answersDetail(as []*sessionv1.Answer) string {
	var b strings.Builder
	for _, a := range as {
		fmt.Fprintf(&b, " answer=%s:%s", a.ItemId, sha(strings.Join(a.Selected, "\x00")+"\x00"+a.Text))
	}
	return strings.TrimSpace(b.String())
}
