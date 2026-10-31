package e2e

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// TestOwnMacWithoutRelay: what concerns the trusted device's own Mac goes
// to its machine in process, never through the relay. With the relay down,
// the app still sees this Mac's coding agents, projects and sessions, adds
// a project, pauses it, reads a session, follows its changes and answers
// its agent's question, each at once.
func TestOwnMacWithoutRelay(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	fake := filepath.Join(binaries(t), "fakeacp")
	w := newOwnerMac(t, []string{"HERMESH_GEMINI_BIN=" + fake, "FAKE_ACP_STATE=" + filepath.Join(t.TempDir(), "fakeacp")},
		&localv1.GrantSpec{Agents: []string{"gemini"}})
	s, r := w.startSession(2*time.Minute, "--agent", "gemini", "--wait", "--prompt", "ask 选哪个颜色|红,蓝")
	if s.SessionID == "" {
		t.Fatalf("start: %+v", r)
	}
	app := w.app.client
	question := func() *sessionv1.Question {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		res, err := app.ListSessions(ctx, &localv1.ListSessionsRequest{})
		if err != nil {
			return nil
		}
		for _, x := range res.Sessions {
			if x.SessionId == s.SessionID {
				return x.Progress.GetPendingQuestion()
			}
		}
		return nil
	}
	eventually(t, time.Minute, "the question", func() bool { return question() != nil })

	w.relay.stop()

	// Each call answers at once: nothing waits on the relay.
	call := func(what string, f func(ctx context.Context) error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := f(ctx); err != nil {
			t.Fatalf("%s without the relay: %v", what, err)
		}
	}
	var machineID string
	call("list machines", func(ctx context.Context) error {
		res, err := app.ListMachines(ctx, &localv1.ListMachinesRequest{})
		if err != nil {
			return err
		}
		if len(res.Machines) != 1 || !res.Machines[0].Local {
			t.Fatalf("machines: %+v", res.Machines)
		}
		machineID = res.Machines[0].MachineId
		if !slices.ContainsFunc(res.Machines[0].Status.GetAgents(), func(a *machinev1.AgentStatus) bool { return a.Name == "gemini" && a.Available }) {
			t.Fatalf("this Mac's agents: %+v", res.Machines[0].Status.GetAgents())
		}
		return nil
	})
	call("check agents", func(ctx context.Context) error {
		_, err := app.CheckAgents(ctx, &localv1.CheckAgentsRequest{})
		return err
	})
	other := gitRepo(t, map[string]string{"README.md": "# other\n"})
	call("add a project", func(ctx context.Context) error {
		_, err := app.AddProject(ctx, &localv1.AddProjectRequest{MachineId: machineID, ProjectId: "other", Path: other})
		return err
	})
	call("list projects", func(ctx context.Context) error {
		res, err := app.ListProjects(ctx, &localv1.ListProjectsRequest{})
		if err != nil {
			return err
		}
		var ids []string
		for _, p := range res.Projects {
			ids = append(ids, p.ProjectId)
		}
		slices.Sort(ids)
		if !slices.Equal(ids, []string{"demo", "other"}) || len(res.Unreachable) != 0 {
			t.Fatalf("projects %v, unreachable %+v", ids, res.Unreachable)
		}
		return nil
	})
	call("pause", func(ctx context.Context) error {
		if _, err := app.SetMachinePaused(ctx, &localv1.SetMachinePausedRequest{MachineId: machineID, Paused: true}); err != nil {
			return err
		}
		_, err := app.SetMachinePaused(ctx, &localv1.SetMachinePausedRequest{MachineId: machineID, Paused: false})
		return err
	})
	call("today's work", func(ctx context.Context) error {
		res, err := app.QueryWork(ctx, &localv1.QueryWorkRequest{TimeZone: "UTC", FirstDay: time.Now().UTC().Format(time.DateOnly), Days: 1,
			Bucket: localv1.WorkBucket_WORK_BUCKET_HOUR, GroupBy: []localv1.WorkDimension{localv1.WorkDimension_WORK_DIMENSION_AGENT}})
		if err != nil {
			return err
		}
		if len(res.RangeSessions) == 0 || res.RangeSessions[0].Sessions == 0 || res.RangeSessions[0].Group.Agent != "gemini" || len(res.Sessions) == 0 ||
			len(res.Sources) != 1 || !res.Sources[0].Live {
			t.Fatalf("today's work: %+v", res)
		}
		return nil
	})
	call("read the session", func(ctx context.Context) error {
		stream, err := app.StreamSessionEvents(ctx, &localv1.StreamSessionEventsRequest{SessionId: s.SessionID})
		if err != nil {
			return err
		}
		defer stream.Close()
		n := 0
		for stream.Receive() {
			n++
		}
		if n == 0 {
			t.Fatal("no events")
		}
		return stream.Err()
	})

	// Its changes are followed: answering the question shows as the turn goes on.
	watchCtx, stopWatch := context.WithCancel(context.Background())
	defer stopWatch()
	watch, err := app.WatchSessions(watchCtx, &localv1.WatchSessionsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	changed := make(chan *sessionv1.Session, 64)
	go func() {
		for watch.Receive() {
			m := watch.Msg()
			for _, u := range m.Unreachable {
				if u.MachineId == machineID {
					t.Errorf("this Mac unreachable without the relay: %s", u.Error)
				}
			}
			for _, x := range m.Sessions {
				changed <- x
			}
		}
	}()
	q := question()
	if q == nil {
		t.Fatal("the question is gone")
	}
	call("answer", func(ctx context.Context) error {
		_, err := app.AnswerQuestion(ctx, &localv1.AnswerQuestionRequest{SessionId: s.SessionID, QuestionId: q.QuestionId,
			Answers: []*sessionv1.Answer{{ItemId: q.Items[0].Id, Selected: []string{"红"}}}})
		return err
	})
	timeout := time.After(time.Minute)
	for {
		select {
		case x := <-changed:
			if x.SessionId == s.SessionID && x.Status == sessionv1.SessionStatus_SESSION_STATUS_IDLE {
				return
			}
		case <-timeout:
			t.Fatal("the answered turn did not finish in the watch")
		}
	}
}
