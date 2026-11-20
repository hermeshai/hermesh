package machine

import (
	"slices"
	"strings"

	"connectrpc.com/connect"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/provider"
	"github.com/hermeshai/hermesh/internal/provider/fake"
	"github.com/hermeshai/hermesh/internal/shellenv"
)

// AIRecipientUnverified explains why a third-party coding process cannot receive data.
const AIRecipientUnverified = "Hermesh cannot identify the coding agent’s configured AI service on this host. No coding content was sent."

const (
	herCoding   = sessionv1.CodingConsentScope_CODING_CONSENT_SCOPE_HER
	phoneCoding = sessionv1.CodingConsentScope_CODING_CONSENT_SCOPE_MOBILE_ANSWER
)

func isLocalAgent(p provider.Provider) bool {
	_, ok := p.(fake.Provider)
	return ok
}

type codingKey struct {
	agent string
	scope sessionv1.CodingConsentScope
}

type codingApproval struct {
	route    string
	version  string
	approved bool
}

func (s *Sessions) codingRoute(agent, dir string, env []string) provider.CodingRoute {
	r, ok := s.providers[agent].(provider.CodingRouter)
	if !ok {
		return provider.CodingRoute{}
	}
	if env == nil {
		env = shellenv.Env("")
	}
	return r.CodingRoute(dir, env)
}

func (s *Sessions) codingDir(r *sessionRow) string {
	if r.Worktree != "" {
		return r.Worktree
	}
	p, _ := s.st.Project(r.ProjectID)
	if p != nil {
		return p.Path
	}
	return ""
}

func (s *Sessions) codingConsent(p *Principal, req *sessionv1.CodingConsentRequest) (*sessionv1.CodingConsentResponse, error) {
	if p.Kind != PrincipalOwner {
		return nil, cerr(connect.CodePermissionDenied, "only an owner device manages coding consent")
	}
	scope := req.Scope
	if scope != herCoding && scope != phoneCoding || req.Revoke && req.ApproveVersion != "" {
		return nil, cerr(connect.CodeInvalidArgument, "choose a coding consent scope and operation")
	}
	agent, dir := req.Agent, ""
	var active provider.CodingRoute
	var running bool
	if req.SessionId != "" {
		l, r, err := s.find(p, req.SessionId, false)
		if err != nil {
			return nil, err
		}
		agent, dir = r.Agent, s.codingDir(r)
		if l != nil {
			l.mu.Lock()
			active, running = l.codingRoute, l.sess != nil
			l.mu.Unlock()
		}
	} else if !req.Revoke {
		proj, err := s.st.Project(req.ProjectId)
		if err != nil || proj == nil {
			return nil, cerr(connect.CodeNotFound, "select a project on this execution host")
		}
		dir = proj.Path
	}
	if agent == "" {
		return nil, cerr(connect.CodeInvalidArgument, "choose a coding agent")
	}
	out := &sessionv1.CodingConsentResponse{MachineId: s.machineID}
	if req.Revoke {
		s.codingMu.Lock()
		delete(s.coding, codingKey{agent, scope})
		s.codingMu.Unlock()
		return out, nil
	}
	if isLocalAgent(s.providers[agent]) {
		out.Local, out.Approved = true, true
		return out, nil
	}
	r := s.codingRoute(agent, dir, nil)
	if r.Fingerprint == "" || running && active.Fingerprint != r.Fingerprint {
		s.codingMu.Lock()
		_, _ = s.checkCodingLocked(agent, r, scope, "")
		s.codingMu.Unlock()
		if r.Fingerprint == "" || active.Fingerprint == "" {
			return nil, cerr(connect.CodeFailedPrecondition, AIRecipientUnverified)
		}
		return nil, cerr(connect.CodeFailedPrecondition, "the coding route changed; restart its session to use the new route")
	}
	out.Recipient, out.Route = r.Recipient, r.Description
	if scope == herCoding {
		out.DataCategories = "Task text, follow-up messages, answers, attachments and relevant workspace and session context"
		out.Purpose = "Run and continue coding tasks delegated by Her"
	} else {
		out.DataCategories = "Typed answers and selected options, with relevant coding session context"
		out.Purpose = "Answer coding agent questions from iPhone"
	}
	key := codingKey{agent, scope}
	s.codingMu.Lock()
	defer s.codingMu.Unlock()
	if s.coding == nil {
		s.coding = make(map[codingKey]codingApproval)
	}
	a := s.coding[key]
	if a.route != r.Fingerprint {
		a = codingApproval{route: r.Fingerprint}
		delete(s.coding, key)
	}
	if req.ApproveVersion != "" {
		if a.version == "" || a.version != req.ApproveVersion {
			return nil, cerr(connect.CodeFailedPrecondition, "coding recipient or approval changed; review the disclosure again")
		}
		a.approved = true
	} else if a.version == "" {
		a.version = newUUID()
	}
	s.coding[key] = a
	out.Version, out.Approved = a.version, a.approved
	return out, nil
}

func (s *Sessions) checkCodingLocked(agent string, r provider.CodingRoute, scope sessionv1.CodingConsentScope, version string) (string, error) {
	key := codingKey{agent, scope}
	a := s.coding[key]
	if a.route != r.Fingerprint {
		delete(s.coding, key)
		a = codingApproval{}
	}
	if r.Fingerprint == "" {
		return "", cerr(connect.CodeFailedPrecondition, AIRecipientUnverified)
	}
	if !a.approved || version != "" && a.version != version {
		return "", cerr(connect.CodeFailedPrecondition, "coding_consent_required: approve %s on the execution host for this use before sending", r.Recipient)
	}
	return a.version, nil
}

func (s *Sessions) checkHerCodingRecipient(p *Principal, r *sessionRow, version string, preflight bool) (string, error) {
	if p.Grant.GetHerMachineId() == "" || isLocalAgent(s.providers[r.Agent]) {
		return "", nil
	}
	route := s.codingRoute(r.Agent, s.codingDir(r), nil)
	s.codingMu.Lock()
	defer s.codingMu.Unlock()
	if !preflight && version == "" && route.Fingerprint != "" {
		return "", cerr(connect.CodeFailedPrecondition, "coding_consent_required: check the coding recipient before sending")
	}
	return s.checkCodingLocked(r.Agent, route, herCoding, version)
}

// Queued messages written before the gate can still be in the on-disk inbox.
func (c knownClient) isHer() bool {
	return c.Her || c.Label == "Her" || strings.HasPrefix(c.Label, "Her on ")
}

// guardCoding serializes a protected input with revocation and checks the route
// of an already running process as well as the host's current route.
func (s *Sessions) guardCoding(agent, dir string, active provider.CodingRoute, scope sessionv1.CodingConsentScope, version string, send func() error) error {
	r := s.codingRoute(agent, dir, nil)
	s.codingMu.Lock()
	defer s.codingMu.Unlock()
	if _, err := s.checkCodingLocked(agent, r, scope, version); err != nil {
		return err
	}
	if active.Fingerprint != "" && active.Fingerprint != r.Fingerprint {
		return cerr(connect.CodeFailedPrecondition, "the coding agent's route changed; restart the agent and review its disclosure")
	}
	return send()
}

func (s *Sessions) codingInput(row *sessionRow, active provider.CodingRoute, m message, send func() error) error {
	if m.codingVersion == "" {
		return send()
	}
	if active.Fingerprint == "" {
		return cerr(connect.CodeFailedPrecondition, AIRecipientUnverified)
	}
	return s.guardCoding(row.Agent, s.codingDir(row), active, herCoding, m.codingVersion, send)
}

// firstDeliverableAsked skips legacy messages and work whose approval expired.
func (s *Sessions) firstDeliverableAsked(l *live) int {
	if len(l.asked) == 0 {
		return -1
	}
	if isLocalAgent(s.providers[l.row.Agent]) {
		return 0
	}
	clients := s.st.knownClients()
	return slices.IndexFunc(l.asked, func(m message) bool {
		if m.codingVersion != "" {
			return s.guardCoding(l.row.Agent, s.codingDir(l.row), provider.CodingRoute{}, herCoding, m.codingVersion, func() error { return nil }) == nil
		}
		return !clients[m.by].isHer()
	})
}

// phoneCanAnswer carries a compatibility hint for clients that predate disclosure.
func (s *Sessions) phoneCanAnswer(l *live, ps provider.Session) bool {
	if isLocalAgent(s.providers[l.row.Agent]) && fake.IsLocalSession(ps) {
		return true
	}
	if l.codingRoute.Fingerprint == "" {
		return false
	}
	return s.guardCoding(l.row.Agent, s.codingDir(l.row), l.codingRoute, phoneCoding, "", func() error { return nil }) == nil
}

func firstCodingVersion(versions []string) string {
	if len(versions) == 0 {
		return ""
	}
	return versions[0]
}
