package machine

import (
	"context"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/provider"
)

type catalogRecorder struct {
	recorder
	catalogs []provider.ModelCatalog
}

func (r *catalogRecorder) ModelCatalogs() []provider.ModelCatalog { return r.catalogs }

func TestModelCatalogVisibility(t *testing.T) {
	r := &catalogRecorder{}
	h := newHarnessWith(t, DefaultLimits(), map[string]provider.Provider{"antigravity": r})
	start := func(p *Principal) string {
		t.Helper()
		s, err := h.s.Start(context.Background(), p, &sessionv1.StartSessionRequest{ProjectId: "demo", Agent: "antigravity", Prompt: "say ok"})
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "session idle", func() bool {
			v, err := h.s.Get(p, s.SessionId)
			return err == nil && v.Status == idle
		})
		return s.SessionId
	}
	one := start(h.p)
	other := &Principal{Kind: PrincipalClient, ClientID: "other", Grant: h.p.Grant}
	two := start(other)
	if spec := r.spec(0); spec.ModelScope != "demo/"+one {
		t.Fatalf("scope not passed: %q", spec.ModelScope)
	}
	r.catalogs = []provider.ModelCatalog{
		{Scope: "isolated", State: "session_required", Source: "acp_initialize"},
		{Scope: "demo/" + one, State: "known", Source: "acp_session", Models: []provider.Model{{ID: "one"}}, DefaultModel: "one", ObservedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)},
		{Scope: "demo/" + two, State: "known", Source: "acp_session", Models: []provider.Model{{ID: "two"}}},
	}
	for _, tc := range []struct {
		name string
		p    *Principal
		want int
	}{
		{"owner", &Principal{Kind: PrincipalOwner}, 3},
		{"own", h.p, 2},
		{"all", &Principal{Kind: PrincipalClient, ClientID: "third", Grant: &identityv1.Grant{Agents: []string{"antigravity"}, ProjectIds: []string{"demo"}, TaskVisibility: identityv1.TaskVisibility_TASK_VISIBILITY_ALL}}, 3},
		{"other-project", &Principal{Kind: PrincipalClient, ClientID: "third", Grant: &identityv1.Grant{Agents: []string{"antigravity"}, ProjectIds: []string{"elsewhere"}, TaskVisibility: identityv1.TaskVisibility_TASK_VISIBILITY_ALL}}, 1},
		{"other-agent", &Principal{Kind: PrincipalClient, ClientID: "third", Grant: &identityv1.Grant{Agents: []string{"codex"}, ProjectIds: []string{"*"}, TaskVisibility: identityv1.TaskVisibility_TASK_VISIBILITY_ALL}}, 0},
		{"anonymous", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var a *machinev1.AgentStatus
			for _, candidate := range h.n.statusFor(tc.p).Agents {
				if candidate.Name == "antigravity" {
					a = candidate
				}
			}
			if a == nil || len(a.ModelCatalogs) != tc.want {
				t.Fatalf("catalogs: %+v", a)
			}
			if len(a.Models) != 0 || a.Own != nil {
				t.Fatal("session observation became machine-wide model/default")
			}
		})
	}
}
