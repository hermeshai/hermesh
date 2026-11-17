package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// linuxSignerImage is a fresh Debian computer with systemd, polkit, and a
// user, alice, whose sudo asks for her password as a desktop's does.
const linuxSignerImage = `FROM debian:13
RUN apt-get update && apt-get install -y -o Acquire::Retries=8 --no-install-recommends systemd systemd-sysv dbus dbus-user-session polkitd python3 sudo libnotify-bin ca-certificates && apt-get clean && rm -rf /var/lib/apt/lists/*
RUN useradd -m -s /bin/bash -G sudo alice
CMD ["/sbin/init"]
`

// signerAgent, run as the user, asks the signer to watch the hermeshd at
// argv[1] as the Hermesh app does, then is polkit's authentication agent
// for itself (pkttyagent on a pseudo-terminal, as the desktop's dialog is
// for the app), answering with argv[2] and logging what polkit shows to
// argv[3]. It ends at once when the signer refuses.
const signerAgent = `import json, os, pty, select, socket, sys
sock, secret, log = sys.argv[1], sys.argv[2], open(sys.argv[3], "wb", buffering=0)
s = socket.socket(socket.AF_UNIX)
s.connect("/run/hermesh-signer.sock")
s.sendall(json.dumps({"socket": sock, "language": "en"}).encode() + b"\n")
answer = s.makefile("rb").readline()
log.write(b"signer: " + answer)
s.close()
if not json.loads(answer).get("ok"):
    sys.exit(0)
pid, fd = pty.fork()
if pid == 0:
    os.execvp("pkttyagent", ["pkttyagent", "--process", str(os.getppid())])
seen = b""
while select.select([fd], [], [], 600)[0]:
    try:
        data = os.read(fd, 4096)
    except OSError:
        break
    if not data:
        break
    log.write(data)
    seen += data
    if b"Password:" in seen:
        os.write(fd, (secret + "\n").encode())
        seen = b""
`

// TestLinuxSigner (HERMESH_DOCKER=1) is a Linux trusted device's protected
// signer (docs/linux.md §7.3) with the real polkit: the package's service,
// its polkit action and units on a fresh Debian computer, and a user who
// sets Hermesh up there as her first trusted device.
//
//  1. Her hermeshd asks for a root key: the signer makes it where only root
//     reads it, and the user can't.
//  2. Each signature asks her in polkit's dialog, which shows what is
//     signed; her password approves it.
//  3. A wrong password signs nothing.
//  4. The signer refuses a socket that is not hers, and a user whose sudo
//     asks nothing.
//
// The dialog is pkttyagent's, which polkit asks as it asks a desktop's,
// with a rule that stands in for her being at the computer: a container
// has no seat to be active on.
func TestLinuxSigner(t *testing.T) {
	if os.Getenv("HERMESH_DOCKER") != "1" {
		t.Skip("set HERMESH_DOCKER=1")
	}
	bin := binaries(t)
	tmp := t.TempDir()
	linuxBin := filepath.Join(tmp, "linux")
	for _, p := range []string{"./cmd/hermeshd", "./cmd/hermesh-signer", "./e2e/linuxowner"} {
		cmd := exec.Command("go", "build", "-o", linuxBin+"/", p)
		cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v %s", p, err, out)
		}
	}
	port := freePort(t)
	relayData := filepath.Join(tmp, "relay")
	relay := start(t, "relay", nil, filepath.Join(bin, "hermesh-relay"), "serve", "--data-dir", relayData, "--listen", fmt.Sprintf("127.0.0.1:%d", port))
	eventually(t, 20*time.Second, "relay up", func() bool { return strings.Contains(relay.output(), "relay listening") })
	claim := strings.TrimSpace(run(t, nil, 10*time.Second, filepath.Join(bin, "hermesh-relay"), "claim-code", "--data-dir", relayData).stdout)

	name, sh := startLinuxImage(t, "hermesh-e2e-linux-signer:1", linuxSignerImage, "hermesh-signer", "desk-1")
	cp := func(from, to string) {
		t.Helper()
		if out, err := exec.Command("docker", "cp", from, name+":"+to).CombinedOutput(); err != nil {
			t.Fatalf("docker cp %s: %v %s", from, err, out)
		}
	}
	cp(linuxBin, "/opt/test")
	cp("apps/desktop/build/linux", "/opt/units")
	put := func(path, content string) {
		t.Helper()
		cmd := exec.Command("docker", "exec", "-i", name, "sh", "-c", `cat > "$1"`, "sh", path)
		cmd.Stdin = strings.NewReader(content)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("writing %s: %v %s", path, err, out)
		}
	}
	put("/opt/test/agent.py", signerAgent)
	// As the package installs it.
	if out, err := sh(`set -e
mkdir -p /opt/Hermesh/bin && install -m 0755 /opt/test/hermesh-signer /opt/Hermesh/bin/
install -m 0644 /opt/units/io.hermesh.signer.policy /usr/share/polkit-1/actions/
install -m 0644 /opt/units/hermesh-signer.service /opt/units/hermesh-signer.socket /usr/lib/systemd/system/
systemctl daemon-reload && systemctl enable --now hermesh-signer.socket
echo 'polkit.addRule(function(a, s) { if (a.id == "io.hermesh.signer.sign") return polkit.Result.AUTH_SELF; });' > /etc/polkit-1/rules.d/50-test.rules
echo 'alice:open-sesame-42' | chpasswd
chmod 755 /opt/test/* && systemctl restart polkit`); err != nil {
		t.Fatalf("installing: %v\n%s", err, out)
	}
	alice := func(script string) (string, error) {
		out, err := exec.Command("docker", "exec", "-u", "alice", "-e", "HOME=/home/alice", "-e", "USER=alice", name, "sh", "-c", script).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	const sock = "/home/alice/.hm/o.sock"
	if out, err := alice("mkdir -p /home/alice/.hm && nohup /opt/test/hermeshd run --owner --data-dir /home/alice/.hm --socket " + sock + " > /home/alice/.hm/owner.log 2>&1 &"); err != nil {
		t.Fatalf("hermeshd: %v %s", err, out)
	}
	agent := func(socket, secret string) {
		t.Helper()
		alice("pkill -f agent.py")
		if out, err := alice("nohup python3 /opt/test/agent.py " + socket + " " + secret + " /home/alice/agent.log > /dev/null 2>&1 &"); err != nil {
			t.Fatalf("agent: %v %s", err, out)
		}
		eventually(t, 20*time.Second, "the signer's answer", func() bool {
			log, _ := alice("cat /home/alice/agent.log")
			return strings.HasPrefix(log, "signer: ")
		})
	}
	agentLog := func() string { s, _ := alice("cat /home/alice/agent.log"); return s }
	owner := func(args string) (string, error) { return alice("/opt/test/linuxowner -sock " + sock + " " + args) }
	eventually(t, 20*time.Second, "hermeshd answering", func() bool { _, err := owner("signer"); return err == nil })

	// 1, 2. The first trusted device, and this computer a machine: each
	// signature asked in polkit's dialog, which says what is signed.
	agent(sock, "open-sesame-42")
	if !strings.Contains(agentLog(), `signer: {"ok":true}`) {
		t.Fatalf("the signer: %s", agentLog())
	}
	if s, _ := owner("signer"); s != "connected=true busy=false" {
		t.Fatalf("hermeshd's signer: %s", s)
	}
	if out, err := owner(fmt.Sprintf("setup http://host.docker.internal:%d %s", port, claim)); err != nil {
		t.Fatalf("setup: %v %s", err, out)
	}
	machine, err := owner("machine desk-1")
	if err != nil {
		t.Fatalf("machine: %v %s\n%s", err, machine, agentLog())
	}
	for _, want := range []string{"AUTHENTICATING FOR io.hermesh.signer.sign", "Create your owner identity", "Share the agents on this computer", "Authenticating as: alice", "AUTHENTICATION COMPLETE"} {
		if !strings.Contains(agentLog(), want) {
			t.Fatalf("polkit's dialog did not say %q:\n%s", want, agentLog())
		}
	}
	if s, err := sh("stat -c '%U %a' /var/lib/hermesh-signer /var/lib/hermesh-signer/*/ /var/lib/hermesh-signer/*/*.pem"); err != nil || s != "root 700\nroot 700\nroot 600" {
		t.Fatalf("the keys: %v\n%s", err, s)
	}
	if s, err := alice("cat /var/lib/hermesh-signer/*/*.pem"); err == nil {
		t.Fatalf("alice read her root key: %s", s)
	}

	// 3. A wrong password signs nothing.
	agent(sock, "not-her-secret")
	if out, err := owner("her " + machine); err == nil {
		t.Fatalf("signed with a wrong password: %s\n%s", out, agentLog())
	}
	if !strings.Contains(agentLog(), "AUTHENTICATION FAILED") {
		t.Fatalf("polkit did not refuse:\n%s", agentLog())
	}

	// 4. Refusals: a socket not hers, and a sudo that asks nothing.
	agent("/run/hermesh-signer.sock", "open-sesame-42")
	if !strings.Contains(agentLog(), `"error":"not_yours"`) {
		t.Fatalf("another's socket: %s", agentLog())
	}
	if out, err := sh("echo 'alice ALL=(ALL) NOPASSWD: ALL' > /etc/sudoers.d/alice"); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	agent(sock, "open-sesame-42")
	if !strings.Contains(agentLog(), `"error":"open_sudo"`) {
		t.Fatalf("a sudo that asks nothing: %s", agentLog())
	}
}
