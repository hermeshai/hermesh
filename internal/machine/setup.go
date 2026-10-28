package machine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	"github.com/hermeshai/hermesh/internal/provider"
	"github.com/hermeshai/hermesh/internal/shellenv"
)

// What a machine offers the owner's devices while they set it up from
// afar (docs/onboarding.md §5.5): the repositories to make projects of,
// and a test of each coding agent.

const (
	repoDepth = 3   // levels under the home folder
	repoLimit = 100 // repositories listed at most
)

// repoSkip are folders never searched: dependencies, caches and builds.
var repoSkip = map[string]bool{"node_modules": true, "vendor": true, "dist": true, "build": true, "target": true, "venv": true,
	"__pycache__": true, "go": true, "snap": true}

// protectedOnMac are the folders macOS asks the person before an app reads:
// searching them would put a prompt on a screen nobody may be watching.
var protectedOnMac = map[string]bool{"Desktop": true, "Documents": true, "Downloads": true, "Library": true, "Movies": true,
	"Music": true, "Pictures": true, "Public": true, "Applications": true}

// FindRepos lists the git repositories under the home folder, newest commit
// first, as "~/…" paths. It reads only directory entries and the .git
// files that tell the branch and the last commit.
func FindRepos(ctx context.Context) []*machinev1.Repo {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var repos []*machinev1.Repo
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if ctx.Err() != nil || len(repos) >= repoLimit {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || repoSkip[e.Name()] {
				continue
			}
			if depth == 1 && runtime.GOOS == "darwin" && protectedOnMac[e.Name()] {
				continue
			}
			path := filepath.Join(dir, e.Name())
			if r := repoAt(home, path); r != nil {
				repos = append(repos, r)
				continue
			}
			if depth < repoDepth {
				walk(path, depth+1)
			}
		}
	}
	walk(home, 1)
	sort.SliceStable(repos, func(i, j int) bool { return repos[i].LastCommit.AsTime().After(repos[j].LastCommit.AsTime()) })
	return repos
}

// repoAt describes the repository at path, nil if it is none.
func repoAt(home, path string) *machinev1.Repo {
	git := filepath.Join(path, ".git")
	st, err := os.Stat(git)
	if err != nil || !st.IsDir() {
		return nil
	}
	r := &machinev1.Repo{Path: "~" + strings.TrimPrefix(path, home)}
	if head, err := os.ReadFile(filepath.Join(git, "HEAD")); err == nil {
		r.Branch = strings.TrimPrefix(strings.TrimSpace(string(head)), "ref: refs/heads/")
		if len(r.Branch) == 40 { // detached
			r.Branch = r.Branch[:7]
		}
	}
	for _, f := range []string{"logs/HEAD", "index", "HEAD"} {
		if fi, err := os.Stat(filepath.Join(git, f)); err == nil {
			r.LastCommit = timestamppb.New(fi.ModTime())
			break
		}
	}
	return r
}

// testPrompt is the turn Test runs: short, no tools, one word back.
const testPrompt = "Reply with the single word ready, and nothing else. Use no tools."

// testTimeout bounds a test turn, sign-in prompts and slow starts included.
const testTimeout = 90 * time.Second

// TestAgent runs one short turn of a coding agent in a scratch folder and
// reports its answer: proof that it is installed and signed in. Nothing is
// kept and no tool may run.
func (n *Machine) TestAgent(ctx context.Context, name string) (*machinev1.TestAgentResponse, error) {
	p, ok := n.cfg.Providers[name]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no coding agent %q here", name))
	}
	dir, err := os.MkdirTemp("", "hermesh-test-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	tmp := filepath.Join(dir, "tmp")
	os.Mkdir(tmp, 0o700)
	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	start := time.Now()
	n.audit.Log(AuditRecord{Kind: "test", Subject: name})
	sess, err := p.Start(ctx, provider.StartSpec{WorkDir: dir, TempDir: tmp, Prompt: testPrompt, Env: shellenv.Env(tmp),
		Gate: func(context.Context, provider.ToolAction) provider.Decision {
			return provider.Decision{Reason: "a test turn uses no tools"}
		}})
	if err != nil {
		return &machinev1.TestAgentResponse{Reply: err.Error(), Millis: uint32(time.Since(start).Milliseconds())}, nil
	}
	defer sess.Close()
	var reply string
	for {
		select {
		case <-ctx.Done():
			return &machinev1.TestAgentResponse{Reply: "no answer in time", Millis: uint32(time.Since(start).Milliseconds())}, nil
		case <-sess.Done():
			why := "it stopped before answering"
			if err := sess.Err(); err != nil && !errors.Is(err, fs.ErrClosed) {
				why = err.Error()
			}
			return &machinev1.TestAgentResponse{Reply: why, Millis: uint32(time.Since(start).Milliseconds())}, nil
		case e := <-sess.Events():
			switch e.Kind {
			case provider.EventAgentMessage:
				reply = strings.TrimSpace(e.Text)
			case provider.EventError:
				return &machinev1.TestAgentResponse{Reply: e.Text, Millis: uint32(time.Since(start).Milliseconds())}, nil
			case provider.EventTurnCompleted:
				if reply == "" {
					reply = strings.TrimSpace(e.Text)
				}
				return &machinev1.TestAgentResponse{Ok: !e.IsError && reply != "", Reply: reply, Millis: uint32(time.Since(start).Milliseconds())}, nil
			}
		}
	}
}
