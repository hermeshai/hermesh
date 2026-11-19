package machine

import (
	"slices"
	"strings"

	"connectrpc.com/connect"

	"github.com/hermeshai/hermesh/internal/provider"
	"github.com/hermeshai/hermesh/internal/provider/fake"
)

// AIRecipientUnverified explains why a third-party coding process cannot receive Her's data.
const AIRecipientUnverified = "The coding agent chooses its own AI recipient. Hermesh cannot verify that recipient, so the task, follow-up, or answer was blocked."

func isLocalAgent(p provider.Provider) bool {
	_, ok := p.(fake.Provider)
	return ok
}

func onlyLocalAgents(providers map[string]provider.Provider) bool {
	for _, p := range providers {
		if !isLocalAgent(p) {
			return false
		}
	}
	return len(providers) != 0
}

// Check the signed grant before the execution host accepts content for the agent.
func (s *Sessions) checkHerCodingRecipient(p *Principal, agent string) error {
	if p.Grant.GetHerMachineId() != "" && !isLocalAgent(s.providers[agent]) {
		return cerr(connect.CodeFailedPrecondition, AIRecipientUnverified)
	}
	return nil
}

// Queued messages written before the gate can still be in the on-disk inbox.
// Old client records stored the canonical Her label from the signed grant.
func (c knownClient) isHer() bool {
	return c.Her || c.Label == "Her" || strings.HasPrefix(c.Label, "Her on ")
}

// firstDeliverableAsked skips legacy Her messages on an opaque adapter.
// The caller holds the session lock.
func (s *Sessions) firstDeliverableAsked(l *live) int {
	if len(l.asked) == 0 {
		return -1
	}
	if isLocalAgent(s.providers[l.row.Agent]) {
		return 0
	}
	clients := s.st.knownClients()
	return slices.IndexFunc(l.asked, func(m message) bool { return !clients[m.by].isHer() })
}
