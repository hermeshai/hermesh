package machine

import (
	"context"

	"github.com/hermeshai/hermesh/internal/provider"
)

// gate returns the tool gate for a session. A grant fully trusts its
// client, so every tool call is allowed; each one is audited.
func (s *Sessions) gate(l *live) provider.Gate {
	l.mu.Lock()
	id, by := l.row.ID, l.row.CreatedBy
	l.mu.Unlock()
	return func(_ context.Context, a provider.ToolAction) provider.Decision {
		s.audit.Log(AuditRecord{Kind: "tool", Subject: by, Session: id, Action: string(a.Kind) + " " + a.Name + ": " + a.Summary, Decision: "allow"})
		return provider.Decision{Allow: true}
	}
}
