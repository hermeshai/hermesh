package e2e

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/local/v1/localv1connect"
	"github.com/hermeshai/hermesh/pkg/identity"
	"google.golang.org/protobuf/proto"
)

var (
	buildOnce sync.Once
	binDir    string
	buildErr  error
)

func repoRoot() string {
	wd, _ := os.Getwd()
	return filepath.Dir(wd)
}

// binaries builds hermesh-relay, hermeshd and hermesh once per test run.
func binaries(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		binDir, buildErr = os.MkdirTemp("", "hermesh-e2e-bin")
		if buildErr != nil {
			return
		}
		for _, pkg := range []string{"./cmd/hermesh-relay", "./cmd/hermeshd", "./cmd/hermesh", "./internal/provider/acp/fakeacp"} {
			cmd := filepath.Base(pkg)
			args := []string{"build", "-o", filepath.Join(binDir, cmd)}
			if os.Getenv("HERMESH_E2E_RACE") != "" {
				args = append(args, "-race") // the binaries exit 66 on a data race
			}
			out, err := exec.Command("go", append(args, pkg)...).CombinedOutput()
			if err != nil {
				buildErr = fmt.Errorf("build %s: %v\n%s", cmd, err, out)
				return
			}
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return binDir
}

func init() {
	// go build must run from the module root.
	if wd, err := os.Getwd(); err == nil && filepath.Base(wd) == "e2e" {
		os.Chdir(filepath.Dir(wd))
	}
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// proc is a background process with captured output.
type proc struct {
	name  string
	cmd   *exec.Cmd
	mu    sync.Mutex
	out   bytes.Buffer
	lines chan string
	done  chan struct{}
}

func start(t *testing.T, name string, env []string, bin string, args ...string) *proc {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	p := &proc{name: name, cmd: cmd, lines: make(chan string, 1024), done: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			l := sc.Text()
			p.mu.Lock()
			p.out.WriteString(l + "\n")
			p.mu.Unlock()
			select {
			case p.lines <- l:
			default:
			}
		}
	}()
	go func() {
		cmd.Wait()
		pw.Close()
		close(p.done)
	}()
	t.Cleanup(func() { p.kill() })
	return p
}

func (p *proc) kill() {
	select {
	case <-p.done:
		return
	default:
	}
	syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	<-p.done
}

func (p *proc) stop() {
	select {
	case <-p.done:
		return
	default:
	}
	syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		p.kill()
	}
}

func (p *proc) output() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.out.String()
}

var codeRE = regexp.MustCompile(`Pairing code: ([0-9A-Z]{4}-[0-9A-Z]{4}-[0-9A-Z]{4})`)

func (p *proc) pairingCode(t *testing.T) string {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case l := <-p.lines:
			if m := codeRE.FindStringSubmatch(l); m != nil {
				return m[1]
			}
		case <-p.done:
			t.Fatalf("%s exited before showing a pairing code:\n%s", p.name, p.output())
		case <-deadline:
			t.Fatalf("%s showed no pairing code:\n%s", p.name, p.output())
		}
	}
}

func (p *proc) wait(t *testing.T, timeout time.Duration) int {
	t.Helper()
	select {
	case <-p.done:
		return p.cmd.ProcessState.ExitCode()
	case <-time.After(timeout):
		t.Fatalf("%s did not exit:\n%s", p.name, p.output())
	}
	return -1
}

// result of a one-shot CLI command.
type result struct {
	stdout, stderr string
	code           int
}

func run(t *testing.T, env []string, timeout time.Duration, bin string, args ...string) result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), env...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	r := result{stdout: so.String(), stderr: se.String()}
	if cmd.ProcessState != nil {
		r.code = cmd.ProcessState.ExitCode()
	}
	if ctx.Err() != nil {
		t.Fatalf("%s %v timed out\nstdout:%s\nstderr:%s", filepath.Base(bin), args, r.stdout, r.stderr)
	}
	_ = err
	return r
}

// testApp plays the signer of the desktop app: it talks to the bundled
// hermeshd over the Unix socket, answers signing requests with software
// root keys (in place of the Secure Enclave), one per profile: root first,
// handed over when hermeshd asks for a new root key, then new ones.
type testApp struct {
	t      *testing.T
	client localv1connect.LocalServiceClient
	root   *identity.SoftwareKey
	cancel context.CancelFunc

	mu      sync.Mutex
	keys    map[string]*identity.SoftwareKey // by key id, root's included
	handed  bool                             // root was handed over as a new key
	deleted []string                         // key ids hermeshd had deleted
	signed  []string
	callers []*localv1.Caller
	reject  bool
	// check plays the owner reading the request before Touch ID (e.g.
	// comparing a verification code); an error declines it.
	check func(*localv1.SigningRequest) error
}

func newTestApp(t *testing.T, sock string, root *identity.SoftwareKey) *testApp {
	a := &testApp{t: t, client: localClient(t, sock), root: root, keys: map[string]*identity.SoftwareKey{identity.KeyID(root.Public()): root}}
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	t.Cleanup(cancel)
	go a.signLoop(ctx)
	return a
}

// localClient is a client of hermeshd's local API on sock, once it answers.
func localClient(t *testing.T, sock string) localv1connect.LocalServiceClient {
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}}
	c := localv1connect.NewLocalServiceClient(hc, "http://local")
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := c.GetStatus(context.Background(), &localv1.GetStatusRequest{}); err == nil {
			return c
		} else if time.Now().After(deadline) {
			t.Fatalf("local API not reachable: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (a *testApp) signLoop(ctx context.Context) {
	for ctx.Err() == nil {
		stream, err := a.client.WatchSigningRequests(ctx, &localv1.WatchSigningRequestsRequest{})
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		for stream.Receive() {
			req := stream.Msg()
			a.mu.Lock()
			a.callers = append(a.callers, req.Caller)
			a.mu.Unlock()
			if req.CreateRootKey {
				a.mu.Lock()
				k := a.root
				if a.handed {
					k, _ = identity.GenerateKey()
					a.keys[identity.KeyID(k.Public())] = k
				}
				a.handed = true
				a.mu.Unlock()
				a.client.CompleteSigningRequest(ctx, &localv1.CompleteSigningRequestRequest{RequestId: req.RequestId, PublicKey: rootPub(k)})
				continue
			}
			if req.DeleteRootKey {
				a.mu.Lock()
				delete(a.keys, req.RootKeyId)
				a.deleted = append(a.deleted, req.RootKeyId)
				a.mu.Unlock()
				a.client.CompleteSigningRequest(ctx, &localv1.CompleteSigningRequestRequest{RequestId: req.RequestId})
				continue
			}
			// Like the real signer: parse and "show" what is signed, then
			// check the message is exactly the domain-separated body.
			if !bytes.Equal(req.Message, identity.SigningInput(req.Type, req.Body)) {
				a.client.CompleteSigningRequest(ctx, &localv1.CompleteSigningRequestRequest{RequestId: req.RequestId, Rejected: true})
				continue
			}
			a.mu.Lock()
			a.signed = append(a.signed, req.Type)
			reject, check := a.reject, a.check
			a.mu.Unlock()
			if check != nil {
				if err := check(req); err != nil {
					a.t.Logf("app declined %s: %v", req.Type, err)
					reject = true
				}
			}
			if reject {
				a.client.CompleteSigningRequest(ctx, &localv1.CompleteSigningRequestRequest{RequestId: req.RequestId, Rejected: true})
				continue
			}
			a.mu.Lock()
			key := a.keys[req.RootKeyId]
			a.mu.Unlock()
			if key == nil {
				a.t.Logf("app has no root key %s", req.RootKeyId)
				a.client.CompleteSigningRequest(ctx, &localv1.CompleteSigningRequestRequest{RequestId: req.RequestId, Rejected: true})
				continue
			}
			sig, _ := key.SignMessage(req.Message)
			a.client.CompleteSigningRequest(ctx, &localv1.CompleteSigningRequestRequest{RequestId: req.RequestId, Signature: sig})
		}
		stream.Close()
	}
}

// addKey gives the app another root key, for a profile set up with it.
func (a *testApp) addKey(k *identity.SoftwareKey) {
	a.mu.Lock()
	a.keys[identity.KeyID(k.Public())] = k
	a.mu.Unlock()
}

func (a *testApp) deletedKeys() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.deleted)
}

func (a *testApp) signedTypes() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.signed...)
}

func rootPub(k *identity.SoftwareKey) []byte { return identity.MarshalPublicKey(k.Public()) }

func gitRepo(t *testing.T, files map[string]string) string {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	for name, content := range files {
		os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}
	env := append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

var _ = proto.Equal
var _ = strings.Contains
