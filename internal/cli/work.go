package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/peer"
	"github.com/hermeshai/hermesh/internal/workstats"
)

// `hermesh work` (docs/her-team.md §6.3): what the coding agents did over
// the last days, from each reachable machine's work counts (PullWork,
// docs/work-stats.md), added up here as an owner device's QueryWork does.

// maxWorkDays a `work` range may cover.
const maxWorkDays = 31

func (a *app) workCmd() *cobra.Command {
	var machineFlag, tz string
	var days int
	cmd := &cobra.Command{
		Use:   "work",
		Short: "What the coding agents on the owner's machines did over the last days: sessions, turns, tokens and time, by agent and by who sent the work",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if days < 1 || days > maxWorkDays {
				return &exitError{code: exitUsage, kind: "usage", err: fmt.Errorf("--days must be 1 to %d", maxWorkDays)}
			}
			loc, err := time.LoadLocation(tz)
			if err != nil {
				return &exitError{code: exitUsage, kind: "usage", err: fmt.Errorf("--time-zone %q: not a time zone (an IANA name such as Asia/Shanghai)", tz)}
			}
			return a.work(cmd, machineFlag, loc, days, time.Now())
		},
	}
	cmd.Flags().IntVar(&days, "days", 1, "how many days, today the last of them (1: today)")
	cmd.Flags().StringVar(&tz, "time-zone", "Local", "the time zone the days begin at midnight in (an IANA name; default this computer's)")
	cmd.Flags().StringVar(&machineFlag, "machine", "", "machine name or id (default: all)")
	return cmd
}

// workJSON is the document's `work`.
type workJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
	Days int    `json:"days"`
	workCounts
	// Buckets are the sessions at work in each hour (one day) or day.
	Buckets  []uint32         `json:"buckets"`
	ByAgent  []agentWorkJSON  `json:"by_agent"`
	ByClient []clientWorkJSON `json:"by_client"`
}

type workCounts struct {
	Sessions     uint32 `json:"sessions"`
	Done         uint32 `json:"done"`
	Turns        uint32 `json:"turns"`
	FailedTurns  uint32 `json:"failed_turns"`
	InputTokens  uint64 `json:"input_tokens"`
	OutputTokens uint64 `json:"output_tokens"`
	BusySeconds  uint64 `json:"busy_seconds"`
}

type agentWorkJSON struct {
	Agent string `json:"agent"`
	workCounts
}

type clientWorkJSON struct {
	// Client is the agent's client id, or "owner".
	Client string `json:"client"`
	Name   string `json:"name"`
	You    bool   `json:"you"`
	workCounts
}

func (a *app) work(cmd *cobra.Command, machineFlag string, loc *time.Location, days int, now time.Time) error {
	ctx := cmd.Context()
	first := now.In(loc).AddDate(0, 0, 1-days).Format(time.DateOnly)
	buckets, whole, err := workstats.Buckets(loc.String(), first, days, days == 1)
	if err != nil {
		return &exitError{code: exitUsage, kind: "usage", err: err}
	}
	c, machines, err := a.connect(ctx, warmTargets(machineFlag))
	if err != nil {
		return err
	}
	defer c.Close()
	machines, err = targetMachines(machines, machineFlag)
	if err != nil {
		return err
	}
	var mu sync.Mutex
	var got []*machineWork
	errs := c.FanOut(ctx, ids(machines), func(ctx context.Context, n *peer.Machine) error {
		w, err := pullWork(ctx, n.Info, workstats.Hour(whole.From))
		if err == nil {
			mu.Lock()
			got = append(got, w)
			mu.Unlock()
		}
		return err
	})
	names := map[string]string{}
	if ms, err := a.messages(c); err == nil {
		if list, err := a.contacts(ctx, ms); err == nil {
			for _, x := range list {
				names[x.Address] = x.Name
			}
		}
	}
	w := countWork(got, buckets, whole, names, a.g.ClientId)
	w.From, w.To, w.Days = workstats.QuarterStart(whole.From).In(loc).Format(time.RFC3339), workstats.QuarterStart(whole.To).In(loc).Format(time.RFC3339), days
	offline := offlineOf(machines, errs)
	out := cmd.OutOrStdout()
	if a.output == "json" {
		return a.report(out, nil, field{"work", w}, field{"offline", offline})
	}
	printWork(out, w)
	for _, m := range offline {
		fmt.Fprintf(out, "offline: %s (%s); its work is not counted\n", m.Machine, offlineText(m))
	}
	return nil
}

// workPuller is a machine's PullWork.
type workPuller interface {
	PullWork(context.Context, *machinev1.PullWorkRequest) (*machinev1.PullWorkResponse, error)
}

// machineWork is one machine's counts from fromHour on, as its pages left
// them: each hour as last sent, keys and sessions by their numbers.
type machineWork struct {
	keys     map[uint32]*machinev1.WorkKey
	sessions map[uint32]*machinev1.WorkSession
	hours    map[int64]*machinev1.WorkHour
}

func pullWork(ctx context.Context, n workPuller, fromHour int64) (*machineWork, error) {
	w := &machineWork{}
	req := &machinev1.PullWorkRequest{FromHour: fromHour}
	for {
		page, err := n.PullWork(ctx, req)
		if err != nil {
			return nil, err
		}
		w.apply(page, req.Store)
		if !page.More {
			return w, nil
		}
		req = &machinev1.PullWorkRequest{Store: page.Store, After: page.Seq}
	}
}

// apply takes one page; a store other than held means the machine counted
// anew, and what was held goes.
func (w *machineWork) apply(page *machinev1.PullWorkResponse, held []byte) {
	if w.keys == nil || !bytes.Equal(page.Store, held) {
		w.keys, w.sessions, w.hours = map[uint32]*machinev1.WorkKey{}, map[uint32]*machinev1.WorkSession{}, map[int64]*machinev1.WorkHour{}
	}
	for _, k := range page.Keys {
		w.keys[k.Key] = k
	}
	for _, s := range page.Sessions {
		w.sessions[s.Session] = s
	}
	for _, h := range page.Hours {
		w.hours[h.Hour] = h
	}
}

// countWork adds up the machines' counts over whole and each bucket:
// sessions at work (each once) by who started them, turns by who asked
// for them (docs/work-stats.md §7). names are the clients' names; me is
// the client running the command.
func countWork(ms []*machineWork, buckets []workstats.Bound, whole workstats.Bound, names map[string]string, me string) workJSON {
	type sess struct {
		m *machineWork
		n uint32
	}
	out := workJSON{Buckets: make([]uint32, len(buckets)), ByAgent: []agentWorkJSON{}, ByClient: []clientWorkJSON{}}
	inBucket := make([]map[sess]bool, len(buckets))
	for i := range inBucket {
		inBucket[i] = map[sess]bool{}
	}
	all := map[sess]bool{}
	agents, clients := map[string]*workCounts{}, map[string]*workCounts{}
	add := func(m map[string]*workCounts, key string) *workCounts {
		if m[key] == nil {
			m[key] = &workCounts{}
		}
		return m[key]
	}
	for _, m := range ms {
		for _, h := range m.hours {
			for _, p := range h.Presence {
				q := h.Hour*4 + int64(p.Quarter)
				if q < whole.From || q >= whole.To {
					continue
				}
				s := sess{m, p.Session}
				if i, ok := bucketOf(buckets, q); ok && !inBucket[i][s] {
					inBucket[i][s] = true
					out.Buckets[i]++
				}
				if all[s] {
					continue
				}
				all[s] = true
				done := sessionDone(m.sessions[p.Session])
				k := m.keys[p.Key]
				for _, c := range []*workCounts{&out.workCounts, add(agents, k.GetAgent()), add(clients, k.GetClient())} {
					c.Sessions++
					if done {
						c.Done++
					}
				}
			}
			for _, x := range h.Measures {
				q := h.Hour*4 + int64(x.Quarter)
				if q < whole.From || q >= whole.To {
					continue
				}
				k := m.keys[x.Key]
				for _, c := range []*workCounts{&out.workCounts, add(agents, k.GetAgent()), add(clients, k.GetClient())} {
					c.Turns += x.Turns
					c.FailedTurns += x.FailedTurns
					c.InputTokens += x.InputTokens
					c.OutputTokens += x.OutputTokens
					c.BusySeconds += uint64(x.BusySeconds)
				}
			}
		}
	}
	for agent, c := range agents {
		out.ByAgent = append(out.ByAgent, agentWorkJSON{Agent: agent, workCounts: *c})
	}
	for client, c := range clients {
		out.ByClient = append(out.ByClient, clientWorkJSON{Client: client, Name: names[client], You: client == me, workCounts: *c})
	}
	slices.SortFunc(out.ByAgent, func(x, y agentWorkJSON) int { return busier(x.workCounts, y.workCounts, x.Agent, y.Agent) })
	slices.SortFunc(out.ByClient, func(x, y clientWorkJSON) int { return busier(x.workCounts, y.workCounts, x.Client, y.Client) })
	return out
}

// busier orders the most sessions first, then the most turns, then by name.
func busier(x, y workCounts, a, b string) int {
	switch {
	case x.Sessions != y.Sessions:
		return int(y.Sessions) - int(x.Sessions)
	case x.Turns != y.Turns:
		return int(y.Turns) - int(x.Turns)
	}
	return strings.Compare(a, b)
}

// bucketOf is the bucket quarter q is in.
func bucketOf(buckets []workstats.Bound, q int64) (int, bool) {
	i, found := slices.BinarySearchFunc(buckets, q, func(b workstats.Bound, q int64) int {
		switch {
		case q < b.From:
			return 1
		case q >= b.To:
			return -1
		}
		return 0
	})
	return i, found
}

// sessionDone: idle after a good turn, or archived, now.
func sessionDone(s *machinev1.WorkSession) bool {
	return s.GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_ARCHIVED || s.GetStatus() == sessionv1.SessionStatus_SESSION_STATUS_IDLE && !s.GetFailed()
}

func printWork(w io.Writer, x workJSON) {
	fmt.Fprintf(w, "%s to %s: %d sessions, %d done, %d turns (%d failed), %d tokens in, %d out, %s busy\n",
		x.From, x.To, x.Sessions, x.Done, x.Turns, x.FailedTurns, x.InputTokens, x.OutputTokens, time.Duration(x.BusySeconds)*time.Second)
	if len(x.ByAgent) == 0 {
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tSESSIONS\tDONE\tTURNS\tFAILED")
	for _, a := range x.ByAgent {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\n", a.Agent, a.Sessions, a.Done, a.Turns, a.FailedTurns)
	}
	fmt.Fprintln(tw, "\nSENT BY\tSESSIONS\tDONE\tTURNS\tFAILED")
	for _, c := range x.ByClient {
		name := firstNonEmpty(c.Name, c.Client)
		if c.You {
			name += " (you)"
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\n", name, c.Sessions, c.Done, c.Turns, c.FailedTurns)
	}
	tw.Flush()
}
