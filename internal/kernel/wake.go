package kernel

import (
	"context"
	"errors"

	"google.golang.org/protobuf/proto"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/kernel/store"
	"github.com/hermeshai/hermesh/internal/runtime"
)

// Wake follows a session's provider again when it is about to work on its own: Her, woken in a
// conversation of hers by news of the work she sent (docs/her-conversations.md §5.3). A session
// whose provider session is live, or that has none to resume, is left as it is; what the provider
// then does becomes the session's continuation run, as out-of-turn work does.
func (e *Engine) Wake(ctx context.Context, sessionID string) error {
	unlock := e.lockSession(sessionID)
	defer unlock()
	sess, err := e.st.Session(sessionID)
	if err != nil {
		return err
	}
	if sess.Status == sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED || sess.ActiveProviderSessionId == "" {
		return nil
	}
	if e.sup.Get(sess.ActiveProviderSessionId) != nil {
		return nil
	}
	var ps *sessionv1.ProviderSession
	pss, _ := e.st.ProviderSessions(sessionID)
	for _, p := range pss {
		if p.ProviderSessionId == sess.ActiveProviderSessionId {
			ps = p
		}
	}
	if ps == nil || ps.ResumeCursor == "" {
		return nil
	}
	instCfg, drv, err := e.resolveInstanceConfig(OwnerPrincipal("owner", "Owner"), sess.ProjectId, ps.InstanceId)
	if err != nil {
		return err
	}
	if drv == nil {
		return errors.New("no driver for the session's provider")
	}
	runs, _ := e.st.Runs(sessionID)
	if len(runs) == 0 {
		return nil
	}
	last := runs[len(runs)-1]
	workdir, err := e.resolveWorkdir(sess)
	if err != nil {
		return err
	}
	drvSess, err := drv.StartSession(e.bgCtx, e.buildStartSpec(ctx, sess, workdir, instCfg, last, ps.ResumeCursor))
	if err != nil {
		return err
	}
	events, _ := e.st.Mutate(func(tx *store.Tx) error {
		p, err := tx.GetProviderSession(ps.ProviderSessionId)
		if err != nil {
			return err
		}
		p.ProcessAlive = true
		return tx.PutProviderSession(p)
	})
	e.publish(events, nil)
	sel := last.GetModelSelection()
	if sel == nil {
		sel = sess.GetModelSelection()
	}
	lease := &runtime.LeaseEntry{
		ProviderSessionID: ps.ProviderSessionId,
		SessionID:         sessionID,
		InstanceID:        instCfg.InstanceId,
		DriverKind:        instCfg.Driver,
		Selection:         proto.Clone(sel).(*sessionv1.ModelSelection),
		RuntimeMode:       last.RuntimeMode,
		InteractionMode:   last.InteractionMode,
		Session:           drvSess,
	}
	e.sup.Register(lease)
	e.startLeasePump(lease, workdir)
	return nil
}

// SessionOfNative is the session whose provider session of driver has the native id ref (Her's
// conversation id, for a her session); "" when there is none.
func (e *Engine) SessionOfNative(driver, ref string) string {
	if ref == "" {
		return ""
	}
	sessions, err := e.st.ListSessions("", true, 0)
	if err != nil {
		return ""
	}
	for _, s := range sessions {
		pss, _ := e.st.ProviderSessions(s.SessionId)
		for _, ps := range pss {
			if ps.Driver == driver && (ps.GetNativeSessionId() == ref || ps.ResumeCursor == ref) {
				return s.SessionId
			}
		}
	}
	return ""
}
