package owner

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/internal/workstats"
)

// Two machines that each have a project called "app": mac's by two agents
// for two clients, vps's by one. Rows of the whole range, of the span before
// it and of bucket 0.
func workParts() []workPart {
	keys := map[int64]machine.WorkKey{1: {Client: "c_her", Agent: "claude", Project: "app"}, 2: {Client: "c_bot", Agent: "codex", Project: "app"}, 3: {Client: "c_her", Agent: "claude", Project: "blog"}}
	rows := func(n uint32) (out []workstats.Row) {
		for _, b := range []int{workstats.Whole, workstats.Previous, 0} {
			for k := range int64(3) {
				out = append(out, workstats.Row{Bucket: b, K: k + 1, Sessions: n, Done: n - 1, Turns: 2 * n, FailedTurns: 1, InputTokens: 100 * uint64(n), OutputTokens: 10, BusySeconds: 60})
			}
		}
		return out
	}
	part := func(id string, n uint32) workPart {
		return workPart{rows(n), rows(n), func(r workstats.Row) (machine.WorkKey, string, bool) { return keys[r.K], id, true }}
	}
	return []workPart{part("mac", 2), part("vps", 5)}
}

func sumSessions(l []*localv1.SessionCount) (n uint32) {
	for _, x := range l {
		n += x.Sessions
	}
	return n
}

func sumTokens(l []*localv1.TurnCount) (n uint64) {
	for _, x := range l {
		n += x.InputTokens + x.OutputTokens
	}
	return n
}

// A filter keeps the rows of one client, agent, machine or one machine's
// project and leaves the answer's shape alone: what it keeps adds up to the
// matching groups of the unfiltered answer, in every list. A project of the
// same name on another machine is another project.
func TestGroupWorkFilter(t *testing.T) {
	all := []localv1.WorkDimension{localv1.WorkDimension_WORK_DIMENSION_CLIENT, localv1.WorkDimension_WORK_DIMENSION_AGENT, localv1.WorkDimension_WORK_DIMENSION_MACHINE, localv1.WorkDimension_WORK_DIMENSION_PROJECT}
	whole := groupWork(all, nil, workParts())
	if len(whole.RangeSessions) != 6 || len(whole.PreviousSessions) != 6 || len(whole.Sessions) != 6 || len(whole.RangeTurns) != 6 || len(whole.PreviousTurns) != 6 {
		t.Fatalf("by all four: %d range, %d previous, %d bucket groups", len(whole.RangeSessions), len(whole.PreviousSessions), len(whole.Sessions))
	}
	for _, c := range []struct {
		name   string
		f      *localv1.WorkGroup
		groups int
	}{
		{"a client", &localv1.WorkGroup{Client: "c_her"}, 4},
		{"an agent", &localv1.WorkGroup{Agent: "codex"}, 2},
		{"a machine", &localv1.WorkGroup{MachineId: "vps"}, 3},
		{"a machine's project", &localv1.WorkGroup{MachineId: "mac", ProjectId: "app"}, 2},
		{"all of them", &localv1.WorkGroup{Client: "c_her", Agent: "claude", MachineId: "vps", ProjectId: "blog"}, 1},
		{"nothing matches", &localv1.WorkGroup{Client: "c_her", Agent: "codex"}, 0},
	} {
		match := func(g *localv1.WorkGroup) bool {
			return (c.f.Client == "" || c.f.Client == g.Client) && (c.f.Agent == "" || c.f.Agent == g.Agent) && (c.f.MachineId == "" || c.f.MachineId == g.MachineId) && (c.f.ProjectId == "" || c.f.ProjectId == g.ProjectId)
		}
		var wantSessions, wantPrevious uint32
		var wantTokens uint64
		for _, x := range whole.RangeSessions {
			if match(x.Group) {
				wantSessions += x.Sessions
			}
		}
		for _, x := range whole.PreviousSessions {
			if match(x.Group) {
				wantPrevious += x.Sessions
			}
		}
		for _, x := range whole.RangeTurns {
			if match(x.Group) {
				wantTokens += x.InputTokens + x.OutputTokens
			}
		}
		// Ungrouped, as the page asks: one row a list.
		got := groupWork(nil, c.f, workParts())
		if n := sumSessions(got.RangeSessions); n != wantSessions || sumSessions(got.PreviousSessions) != wantPrevious || sumSessions(got.Sessions) != wantSessions || sumTokens(got.RangeTurns) != wantTokens || sumTokens(got.PreviousTurns) != wantTokens {
			t.Errorf("%s: %d sessions, %d before, %d tokens; want %d, %d, %d", c.name, n, sumSessions(got.PreviousSessions), sumTokens(got.RangeTurns), wantSessions, wantPrevious, wantTokens)
		}
		if c.groups > 0 && (len(got.RangeSessions) != 1 || len(got.RangeTurns) != 1 || got.RangeSessions[0].Group.MachineId != "") {
			t.Errorf("%s, ungrouped: %+v", c.name, got.RangeSessions)
		}
		if by := groupWork(all, c.f, workParts()); len(by.RangeSessions) != c.groups || len(by.PreviousTurns) != c.groups {
			t.Errorf("%s: %d groups, want %d", c.name, len(by.RangeSessions), c.groups)
		}
	}
	if n := sumSessions(groupWork(nil, &localv1.WorkGroup{MachineId: "mac", ProjectId: "app"}, workParts()).RangeSessions); n != 4 {
		t.Fatalf("mac's app holds vps's: %d sessions, want 4", n)
	}
}

// What a request asks the stores: no buckets for NONE, the span before it
// only when asked, and a project without its machine is refused.
func TestWorkAsk(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 3, 8, 14, 20, 0, 0, sh)
	req := func(f func(*localv1.QueryWorkRequest)) *localv1.QueryWorkRequest {
		r := &localv1.QueryWorkRequest{TimeZone: "Asia/Shanghai", FirstDay: "2026-03-02", Days: 7, Bucket: localv1.WorkBucket_WORK_BUCKET_DAY}
		f(r)
		return r
	}
	a, err := workAsk(req(func(*localv1.QueryWorkRequest) {}), now)
	if err != nil || len(a.Buckets) != 7 || a.Previous != (workstats.Bound{}) || a.TurnsOnly {
		t.Fatalf("by day: %+v, %v", a, err)
	}
	a, err = workAsk(req(func(r *localv1.QueryWorkRequest) {
		r.Bucket, r.Previous, r.TurnsOnly = localv1.WorkBucket_WORK_BUCKET_NONE, true, true
	}), now)
	want, _ := workstats.Earlier("Asia/Shanghai", "2026-03-02", 7, now)
	if err != nil || len(a.Buckets) != 0 || a.Whole.To-a.Whole.From != 7*96 || a.Previous != want || a.Previous.To-a.Previous.From != 6*96+58 || !a.TurnsOnly {
		t.Fatalf("totals with the span before: %+v, %v", a, err)
	}
	for name, r := range map[string]*localv1.QueryWorkRequest{
		"a project without its machine": req(func(r *localv1.QueryWorkRequest) { r.Filter = &localv1.WorkGroup{ProjectId: "app"} }),
		"no bucket":                     req(func(r *localv1.QueryWorkRequest) { r.Bucket = localv1.WorkBucket_WORK_BUCKET_UNSPECIFIED }),
		"400 days by hour":              req(func(r *localv1.QueryWorkRequest) { r.Bucket, r.Days = localv1.WorkBucket_WORK_BUCKET_HOUR, 400 }),
	} {
		if _, err := workAsk(r, now); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := workAsk(req(func(r *localv1.QueryWorkRequest) { r.Filter = &localv1.WorkGroup{MachineId: "mac", ProjectId: "app"} }), now); err != nil {
		t.Fatalf("a machine's project: %v", err)
	}
}

// benchStore is an owner device's copy of five busy machines over 130 days
// (what an iPhone keeps): each day a machine's four keys work in thirty
// quarters, `together` sessions a key at a time out of ten a day, so 20 x
// together sessions are at work for seven and a half hours a day. 78,000
// measure rows and together x 78,000 quarter rows; 30 days are 18,000 of
// each (docs/work-stats.md §7's measure).
func benchStore(b *testing.B, last time.Time, together int64) *store {
	b.Helper()
	st, err := openStore(filepath.Join(b.TempDir(), "owner.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { st.db.Close() })
	tx, err := st.db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	exec := func(q string, args ...any) {
		if _, err := tx.Exec(q, args...); err != nil {
			b.Fatal(err)
		}
	}
	day0 := workstats.Quarter(last) - 130*96
	for m := range int64(5) {
		exec(`INSERT INTO work_machines (m, machine_id, store, seq, synced_at, history) VALUES (?, ?, x'', 0, 0, 1)`, m+1, fmt.Sprintf("m_%d", m))
		for k := range int64(4) {
			exec(`INSERT INTO work_keys (m, k, client, agent, project_id) VALUES (?, ?, ?, ?, ?)`, m+1, k+1, fmt.Sprintf("c_%d", k%2), []string{"claude", "codex"}[k/2], fmt.Sprintf("p%d", k))
		}
		for d := range int64(130) {
			for k := range int64(4) {
				for i := range int64(10) {
					exec(`INSERT INTO work_sessions (m, n, k, status, failed) VALUES (?, ?, ?, 2, 0)`, m+1, d*100+k*10+i, k+1)
				}
			}
			for q := range int64(30) {
				for k := range int64(4) {
					exec(`INSERT INTO work_measures (q, m, k, turns, failed_turns, input_tokens, output_tokens, busy_seconds) VALUES (?, ?, ?, 3, 0, 90000, 4000, 600)`, day0+d*96+32+q, m+1, k+1)
					for i := range together {
						exec(`INSERT INTO work_quarters (q, m, k, n) VALUES (?, ?, ?, ?)`, day0+d*96+32+q, m+1, k+1, d*100+k*10+(q+i)%10)
					}
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	return st
}

// What the statistics ask of an owner device's copy (docs/overview-usage.md
// §6.1, §9): the page (30 days by day with the 30 before), the weeks of
// tokens (126 days, turns only) and the menus (30 days, totals only), each
// with the grouping in hermeshd that follows it.
func BenchmarkQueryWork(b *testing.B) {
	now := time.Date(2026, 11, 24, 14, 20, 0, 0, time.UTC)
	st := benchStore(b, now, 1)
	day := func(n int) string { return now.AddDate(0, 0, -n).Format(time.DateOnly) }
	all := []localv1.WorkDimension{localv1.WorkDimension_WORK_DIMENSION_CLIENT, localv1.WorkDimension_WORK_DIMENSION_AGENT, localv1.WorkDimension_WORK_DIMENSION_MACHINE, localv1.WorkDimension_WORK_DIMENSION_PROJECT}
	for _, c := range []struct {
		name string
		req  *localv1.QueryWorkRequest
		rows int
	}{
		{"page-30-days-with-previous", &localv1.QueryWorkRequest{TimeZone: "UTC", FirstDay: day(29), Days: 30, Bucket: localv1.WorkBucket_WORK_BUCKET_DAY, Previous: true, Filter: &localv1.WorkGroup{Agent: "claude"}}, 150},
		{"weeks-126-days-turns-only", &localv1.QueryWorkRequest{TimeZone: "UTC", FirstDay: day(125), Days: 126, Bucket: localv1.WorkBucket_WORK_BUCKET_DAY, TurnsOnly: true, Filter: &localv1.WorkGroup{MachineId: "m_1", ProjectId: "p2"}}, 150},
		{"menus-30-days-totals", &localv1.QueryWorkRequest{TimeZone: "UTC", FirstDay: day(29), Days: 30, Bucket: localv1.WorkBucket_WORK_BUCKET_NONE, GroupBy: all}, 400},
	} {
		b.Run(c.name, func(b *testing.B) {
			var out *localv1.QueryWorkResponse
			for b.Loop() {
				ask, err := workAsk(c.req, now)
				if err != nil {
					b.Fatal(err)
				}
				sessions, turns, keys, sources, err := st.countWork(context.Background(), ask)
				if err != nil {
					b.Fatal(err)
				}
				ids := map[int64]string{}
				for _, x := range sources {
					ids[x.m] = x.machineID
				}
				out = groupWork(c.req.GroupBy, c.req.Filter, []workPart{{sessions, turns, func(r workstats.Row) (machine.WorkKey, string, bool) { return keys[[2]int64{r.M, r.K}], ids[r.M], true }}})
			}
			n := len(out.Sessions) + len(out.RangeSessions) + len(out.PreviousSessions) + len(out.Turns) + len(out.RangeTurns) + len(out.PreviousTurns)
			if n == 0 || n > c.rows || len(out.RangeTurns) == 0 {
				b.Fatalf("%d rows in the answer, want 1 to %d", n, c.rows)
			}
			b.ReportMetric(float64(n), "rows")
		})
	}
}
