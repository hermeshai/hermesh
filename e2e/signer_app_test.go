//go:build darwin

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

var (
	signerOnce sync.Once
	signerBin  string
	signerErr  error
)

// signerBinary builds the signer (apps/signer/macos) once per test run.
func signerBinary(t *testing.T) string {
	t.Helper()
	signerOnce.Do(func() {
		dir, _ := filepath.Abs(filepath.Join("apps", "signer", "macos")) // the tests run from the repository root
		if out, err := exec.Command("swift", "build", "--package-path", dir).CombinedOutput(); err != nil {
			signerErr = fmt.Errorf("swift build: %v\n%s", err, out)
			return
		}
		out, err := exec.Command("swift", "build", "--package-path", dir, "--show-bin-path").Output()
		if err != nil {
			signerErr = fmt.Errorf("swift build --show-bin-path: %v", err)
			return
		}
		signerBin = filepath.Join(strings.TrimSpace(string(out)), "HermeshSigner")
	})
	if signerErr != nil {
		t.Fatal(signerErr)
	}
	return signerBin
}

// signerReport is one line of the signer's --test-report: a decision and
// what its panel showed.
type signerReport struct {
	Decision        string   `json:"decision"`
	PublicKey       []byte   `json:"public_key"`
	Types           []string `json:"types"`
	Caller          string   `json:"caller"`
	CallerIsHermesh bool     `json:"caller_is_hermesh"`
	Cards           []struct {
		Title, Who, Code, Why, Warning string
		Lines                          []string
	} `json:"cards"`
	Summary   string   `json:"summary"`
	Refused   []string `json:"refused"`
	MatchCode string   `json:"match_code"`
}

func (r signerReport) titles() []string {
	var out []string
	for _, c := range r.Cards {
		out = append(out, c.Title)
	}
	return out
}

func readSignerReport(t *testing.T, path string) []signerReport {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []signerReport
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		var r signerReport
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("report line %q: %v", sc.Text(), err)
		}
		out = append(out, r)
	}
	return out
}

func lastSignerReport(t *testing.T, path string) signerReport {
	t.Helper()
	all := readSignerReport(t, path)
	if len(all) == 0 {
		t.Fatal("the signer reported nothing")
	}
	return all[len(all)-1]
}

// TestSignerApp drives the real signer (the binary of Hermesh Signer.app,
// in its test mode: a software key and a scripted decision) as the only
// signer of an owner Mac, and checks what its panel showed against what
// was asked for. A body with fields the signer does not know cannot come
// from hermeshd, which marshals only what it knows; the Swift tests cover
// that refusal.
func TestSignerApp(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	bin := signerBinary(t)
	dir := t.TempDir()
	keyFile, report := filepath.Join(dir, "root.pem"), filepath.Join(dir, "report.jsonl")
	var signer *proc
	w := newOwnerMacSigned(t, func(w *ownerMac) string {
		signer = start(t, "signer", nil, bin, "--socket", w.sock, "--test-key", keyFile, "--test-decision", "approve", "--test-report", report, "-AppleLanguages", "(en)")
		w.logs = append(w.logs, signer)
		return keyFile
	}, nil, &localv1.GrantSpec{ProjectIds: []string{"demo"}, Agents: []string{"gemini"}})
	// The desktop receives only a busy bit, including key creation and completed batches.
	eventually(t, 10*time.Second, "the signer reports its completed approval state", func() bool {
		out := signer.output()
		return strings.Contains(out, "HERMESH_SIGNER_BUSY=1") && strings.HasSuffix(strings.TrimSpace(out), "HERMESH_SIGNER_BUSY=0")
	})
	ctx := context.Background()
	exe, _ := os.Executable()
	caller := "Requested by " + filepath.Base(exe) // this test made the calls, not Hermesh

	// Setup created the root key in the signer, without a panel; then the
	// panel showed the owner's first root cert with this Mac's device cert,
	// this Mac as a machine, and the agent's grant with the scope asked for.
	got := readSignerReport(t, report)
	if len(got) != 4 || got[0].Decision != "create_root_key" || !bytes.Equal(got[0].PublicKey, rootPub(w.root)) {
		t.Fatalf("report: %+v", got)
	}
	want := [][]string{{"Create your owner identity", "Issue this Mac a certificate for end-to-end connections"}, {"Share the agents on this Mac"}, {"Approve “bot” to connect to your computer"}}
	for i, r := range got[1:] {
		if r.Decision != "approve" || r.Caller != caller || r.CallerIsHermesh || !slices.Equal(r.titles(), want[i]) {
			t.Fatalf("report %d: %+v", i+1, r)
		}
	}
	// The first setup says it in one line; the rest show their cards.
	if !strings.HasPrefix(got[1].Summary, "Creates your owner key in this Mac’s Secure Enclave") || got[2].Summary != "" || got[3].Summary != "" {
		t.Fatalf("summaries: %q %q %q", got[1].Summary, got[2].Summary, got[3].Summary)
	}
	if g := got[3].Cards[0]; !slices.Contains(g.Lines, "Projects it can use: demo") || !slices.Contains(g.Lines, "Coding agents it can run: Gemini CLI") {
		t.Fatalf("grant card: %+v", g)
	}

	// Another agent asks: the panel shows the verification code the
	// requester shows and the scope that was approved.
	hermesh := filepath.Join(w.bin, "hermesh")
	login := start(t, "login bot2", []string{"HERMESH_HOME=" + filepath.Join(w.tmp, "bot2")}, hermesh, "auth", "login", w.addr, "--name", "bot2")
	code := login.pairingCode(t)
	details, err := w.app.client.LookupPairing(ctx, &localv1.LookupPairingRequest{Code: code})
	if err != nil {
		t.Fatal(err)
	}
	spec := &localv1.GrantSpec{Label: "bot2", ProjectIds: []string{"demo"}, Agents: []string{"codex"}, TtlDays: 7}
	approved, err := w.app.client.ApproveClientPairing(ctx, &localv1.ApproveClientPairingRequest{Code: code, Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	r := lastSignerReport(t, report)
	if c := r.Cards[0]; r.Decision != "approve" || c.Title != "Approve “bot2” to connect to your computer" || c.Code != details.Details.VerificationCode ||
		!slices.Contains(c.Lines, "Projects it can use: demo") || !slices.Contains(c.Lines, "Coding agents it can run: Codex") || !strings.HasPrefix(c.Warning, "Full trust") {
		t.Fatalf("approval shown as %+v; the requester shows %s", r, details.Details.VerificationCode)
	}
	if c := login.wait(t, 30*time.Second); c != 0 {
		t.Fatalf("login: %s", login.output())
	}

	// Changing its scope: the panel shows what changes.
	spec.Agents = []string{"codex", "gemini"}
	if _, err := w.app.client.UpdateClient(ctx, &localv1.UpdateClientRequest{GrantId: approved.GrantId, Spec: spec}); err != nil {
		t.Fatal(err)
	}
	if r := lastSignerReport(t, report); r.titles()[0] != "Change the access of personal agent “bot2”" || !slices.Contains(r.Cards[0].Lines, "Coding agents: Codex → Codex, Gemini CLI") {
		t.Fatalf("update shown as %+v", r)
	}

	// A second Mac joins: the panel shows it with the code it shows, and
	// the two digits the person then types there.
	st, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	sock2 := filepath.Join(filepath.Dir(w.sock), "o2.sock")
	w.logs = append(w.logs, start(t, "owner second-mac", nil, filepath.Join(w.bin, "hermeshd"), "run", "--owner", "--data-dir", filepath.Join(w.tmp, "second-mac"), "--socket", sock2))
	root2, _ := identity.GenerateKey()
	app2 := newTestApp(t, sock2, root2)
	join, err := app2.client.RequestJoin(ctx, &localv1.RequestJoinRequest{RelayUrl: st.RelayUrl, OwnerId: st.OwnerId,
		RootPublicKey: rootPub(root2), RootLabel: "second-mac", RootPlatform: "macos"})
	if err != nil {
		t.Fatal(err)
	}
	approval, err := w.app.client.ApproveDevicePairing(ctx, &localv1.ApproveDevicePairingRequest{Code: join.Code})
	if err != nil {
		t.Fatal(err)
	}
	r = lastSignerReport(t, report)
	if c := r.Cards[0]; c.Title != "Add “second-mac” as a trusted device" || c.Code != join.VerificationCode || r.MatchCode != approval.MatchCode {
		t.Fatalf("device approval shown as %+v; the device shows %s, hermeshd says %s", r, join.VerificationCode, approval.MatchCode)
	}
	eventually(t, 60*time.Second, "approval reaches the second Mac", func() bool {
		res, err := app2.client.WaitJoin(ctx, &localv1.WaitJoinRequest{Code: join.Code})
		return err == nil && res.Done
	})
	if _, err := app2.client.ConfirmJoin(ctx, &localv1.ConfirmJoinRequest{Code: join.Code, Accept: true, MatchCode: r.MatchCode}); err != nil {
		t.Fatalf("the digits the signer showed: %v", err)
	}

	// Removing it: the panel names the device that goes.
	if _, err := w.app.client.RemoveDevice(ctx, &localv1.RemoveDeviceRequest{KeyId: identity.KeyID(root2.Public())}); err != nil {
		t.Fatal(err)
	}
	if r := lastSignerReport(t, report); r.titles()[0] != "Remove trusted device “second-mac”" || r.MatchCode != "" {
		t.Fatalf("removal shown as %+v", r)
	}

	// Rejected in the panel (a restarted signer, same key): the call fails.
	signer.kill()
	parent := exec.Command("sleep", "60")
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { parent.Process.Kill() })
	rejecting := start(t, "signer rejecting", nil, bin, "--socket", w.sock, "--parent-pid", strconv.Itoa(parent.Process.Pid),
		"--test-key", keyFile, "--test-decision", "reject", "--test-report", report, "-AppleLanguages", "(en)")
	w.logs = append(w.logs, rejecting)
	login3 := start(t, "login bot3", []string{"HERMESH_HOME=" + filepath.Join(w.tmp, "bot3")}, hermesh, "auth", "login", w.addr, "--name", "bot3")
	_, err = w.app.client.ApproveClientPairing(ctx, &localv1.ApproveClientPairingRequest{Code: login3.pairingCode(t), Spec: &localv1.GrantSpec{Label: "bot3"}})
	if err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("approval the signer rejected: %v", err)
	}
	if r := lastSignerReport(t, report); r.Decision != "reject" || r.titles()[0] != "Approve “bot3” to connect to your computer" || r.Caller != caller {
		t.Fatalf("rejection reported as %+v", r)
	}

	// The signer exits with the app that started it.
	parent.Process.Kill()
	parent.Wait()
	select {
	case <-rejecting.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the signer outlived its parent")
	}
}
