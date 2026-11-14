package cli

import (
	"context"
	"testing"

	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/workstats"
)

// pages is a machine that answers PullWork with its pages in turn.
type pages struct {
	asked []*machinev1.PullWorkRequest
	pages []*machinev1.PullWorkResponse
}

func (p *pages) PullWork(_ context.Context, req *machinev1.PullWorkRequest) (*machinev1.PullWorkResponse, error) {
	p.asked = append(p.asked, req)
	page := p.pages[0]
	p.pages = p.pages[1:]
	return page, nil
}

const (
	idleStatus     = sessionv1.SessionStatus_SESSION_STATUS_IDLE
	runningStatus  = sessionv1.SessionStatus_SESSION_STATUS_RUNNING
	archivedStatus = sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED
)

// A range of two hours from hour 1000: what two machines counted, page by
// page, adds up by agent and by who sent the work; a session in both hours
// counts once in all, and in each hour's bucket; what is outside the range
// is not counted; a page under a new store replaces what came before.
func TestCountWork(t *testing.T) {
	ctx := context.Background()
	mac := &pages{pages: []*machinev1.PullWorkResponse{
		{Store: []byte("a"), Seq: 1, More: true, Keys: []*machinev1.WorkKey{{Key: 1, Client: "c_her", Agent: "claude"}},
			Sessions: []*machinev1.WorkSession{{Session: 9, Key: 1, Status: idleStatus}},
			Hours:    []*machinev1.WorkHour{{Hour: 1000, Presence: []*machinev1.WorkPresence{{Quarter: 0, Key: 1, Session: 9}}}}},
		// The machine counted anew: the first page goes.
		{Store: []byte("b"), Seq: 5, More: true, Keys: []*machinev1.WorkKey{{Key: 1, Client: "c_her", Agent: "claude"}, {Key: 2, Client: "c_grok", Agent: "codex"}},
			Sessions: []*machinev1.WorkSession{{Session: 1, Key: 1, Status: idleStatus}, {Session: 2, Key: 2, Status: runningStatus}}},
		{Store: []byte("b"), Seq: 6, Sessions: []*machinev1.WorkSession{{Session: 3, Key: 2, Status: idleStatus, Failed: true}},
			Hours: []*machinev1.WorkHour{
				{Hour: 999, Presence: []*machinev1.WorkPresence{{Quarter: 3, Key: 2, Session: 3}}}, // before the range
				{Hour: 1000, Presence: []*machinev1.WorkPresence{{Quarter: 1, Key: 1, Session: 1}, {Quarter: 2, Key: 2, Session: 2}},
					Measures: []*machinev1.WorkMeasure{{Quarter: 1, Key: 1, Turns: 2, InputTokens: 100, OutputTokens: 10, BusySeconds: 60},
						{Quarter: 2, Key: 1, Turns: 1, FailedTurns: 1, BusySeconds: 30}}}, // Her asked for a turn in Grok's session
				{Hour: 1001, Presence: []*machinev1.WorkPresence{{Quarter: 0, Key: 1, Session: 1}}},
			}},
	}}
	mini := &pages{pages: []*machinev1.PullWorkResponse{
		{Store: []byte("c"), Seq: 2, Keys: []*machinev1.WorkKey{{Key: 1, Client: "owner", Agent: "claude"}},
			Sessions: []*machinev1.WorkSession{{Session: 1, Key: 1, Status: archivedStatus}},
			Hours: []*machinev1.WorkHour{{Hour: 1001, Presence: []*machinev1.WorkPresence{{Quarter: 3, Key: 1, Session: 1}},
				Measures: []*machinev1.WorkMeasure{{Quarter: 3, Key: 1, Turns: 1, OutputTokens: 5}}}}},
	}}
	var got []*machineWork
	for _, m := range []*pages{mac, mini} {
		w, err := pullWork(ctx, m, 1000)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, w)
	}
	if a := mac.asked; len(a) != 3 || a[0].FromHour != 1000 || a[0].After != 0 || string(a[2].Store) != "b" || a[2].After != 5 {
		t.Fatalf("the pages asked for: %v", a)
	}
	whole := workstats.Bound{From: 4000, To: 4008}
	w := countWork(got, []workstats.Bound{{From: 4000, To: 4004}, {From: 4004, To: 4008}}, whole, map[string]string{"c_grok": "Grok"}, "c_her")

	if w.Sessions != 3 || w.Done != 2 || w.Turns != 4 || w.FailedTurns != 1 || w.InputTokens != 100 || w.OutputTokens != 15 || w.BusySeconds != 90 {
		t.Fatalf("totals: %+v", w.workCounts)
	}
	if len(w.Buckets) != 2 || w.Buckets[0] != 2 || w.Buckets[1] != 2 {
		t.Fatalf("buckets: %v", w.Buckets)
	}
	if len(w.ByAgent) != 2 || w.ByAgent[0].Agent != "claude" || w.ByAgent[0].Sessions != 2 || w.ByAgent[0].Done != 2 || w.ByAgent[0].Turns != 4 ||
		w.ByAgent[1].Agent != "codex" || w.ByAgent[1].Sessions != 1 || w.ByAgent[1].Done != 0 || w.ByAgent[1].Turns != 0 {
		t.Fatalf("by agent: %+v", w.ByAgent)
	}
	byClient := map[string]clientWorkJSON{}
	for _, c := range w.ByClient {
		byClient[c.Client] = c
	}
	her, grok, owner := byClient["c_her"], byClient["c_grok"], byClient["owner"]
	if len(byClient) != 3 || !her.You || her.Sessions != 1 || her.Turns != 3 || her.FailedTurns != 1 ||
		grok.You || grok.Name != "Grok" || grok.Sessions != 1 || grok.Turns != 0 || owner.Sessions != 1 || owner.Done != 1 || owner.Turns != 1 {
		t.Fatalf("by client: %+v", w.ByClient)
	}
}
