package machine

import (
	"connectrpc.com/connect"

	"github.com/hermeshai/hermesh/internal/provider"
	"github.com/hermeshai/hermesh/internal/provider/fake"
)

// AIRecipientUnverified explains why a third-party coding process cannot receive Her's data.
const AIRecipientUnverified = "The coding agent chooses its own AI recipient. Her cannot verify that recipient, so the task, follow-up, or answer was blocked."

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
