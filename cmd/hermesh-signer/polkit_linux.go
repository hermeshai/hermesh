package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"

	"github.com/hermeshai/hermesh/internal/preview"
	"github.com/hermeshai/hermesh/internal/signer"
)

// action is the signer's polkit action (packaging/linux/io.hermesh.signer.policy):
// the user's own authentication, every time (auth_self, never kept).
const action = "io.hermesh.signer.sign"

// polkitOwner asks the user through polkit, which shows what is signed in
// the desktop's own authentication dialog: a program of the user's can't
// draw it, change it or answer it (on Wayland, nor read it). The subject is
// the user's Hermesh app, a process of their desktop session, whose
// authentication agent polkit asks.
type polkitOwner struct {
	uid, pid int
	w        words
}

func newPolkitOwner(uid, pid int, language string) signer.Owner {
	if language != "zh-CN" {
		language = "en"
	}
	return &polkitOwner{uid: uid, pid: pid, w: loadWords(language)}
}

func (o *polkitOwner) Language() string { return o.w.lang }

func (o *polkitOwner) Approve(ctx context.Context, cards []preview.Card) (bool, error) {
	subject, err := processSubject(o.pid, o.uid)
	if err != nil {
		return false, err
	}
	cmd := exec.CommandContext(ctx, "pkcheck", "--action-id", action, "--process", subject, "--allow-user-interaction",
		"--detail", "polkit.message", message(o.w, cards))
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && (exit.ExitCode() == 1 || exit.ExitCode() == 3): // not authorized; dismissed
		return false, nil
	default:
		return false, fmt.Errorf("polkit: %v %s", err, strings.TrimSpace(string(out)))
	}
}

func (o *polkitOwner) Failed(err error) {
	if !errors.Is(err, signer.ErrRefused) {
		slog.Warn("not signed", "uid", o.uid, "err", err)
	}
}

// Show: polkit's dialog is in front already.
func (o *polkitOwner) Show() {}

// Matched tells the two digits to type on what joins, in a notification on
// the user's desktop: polkit's dialog has gone by then.
func (o *polkitOwner) Matched(m preview.Match) {
	title := o.w.MatchMachine
	switch {
	case m.Label != "":
		title = fmt.Sprintf(o.w.MatchOn, m.Label)
	case !m.Machine:
		title = o.w.MatchDevice
	}
	u, err := user.LookupId(strconv.Itoa(o.uid))
	if err != nil {
		return
	}
	bus := fmt.Sprintf("unix:path=/run/user/%d/bus", o.uid)
	cmd := exec.Command("runuser", "-u", u.Username, "--", "env", "DBUS_SESSION_BUS_ADDRESS="+bus,
		"notify-send", "--urgency=critical", "--app-name=Hermesh", title, m.Digits)
	if out, err := cmd.CombinedOutput(); err != nil {
		slog.Warn("the two digits not shown", "uid", o.uid, "err", err, "out", strings.TrimSpace(string(out)))
	}
}

// message is what the dialog says: each card as the panels show it.
func message(w words, cards []preview.Card) string {
	var b strings.Builder
	b.WriteString(w.Hello)
	if len(cards) > 1 {
		b.WriteString("\n" + fmt.Sprintf(w.Several, len(cards)))
	}
	for _, c := range cards {
		b.WriteString("\n\n" + c.Title)
		if c.Code != "" {
			b.WriteString("\n" + fmt.Sprintf(w.Code, c.Code) + " · " + w.Match)
		}
		for _, l := range c.Lines {
			b.WriteString("\n" + l)
		}
		if c.Why != "" {
			b.WriteString("\n" + c.Why)
		}
		if c.Warning != "" {
			b.WriteString("\n⚠ " + c.Warning)
		}
	}
	return b.String()
}

// processSubject is pkcheck's --process for pid: its start time pins it, so
// another process that later gets the same pid is no one polkit asks.
func processSubject(pid, uid int) (string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", errors.New("the Hermesh app that asked is not running: open it, and approve again")
	}
	// The fields after the command's name, which is in parentheses and may hold spaces.
	rest := string(b)
	if i := strings.LastIndexByte(rest, ')'); i >= 0 {
		rest = rest[i+1:]
	}
	f := strings.Fields(rest)
	if len(f) < 20 {
		return "", fmt.Errorf("/proc/%d/stat: too short", pid)
	}
	return fmt.Sprintf("%d,%s,%d", pid, f[19], uid), nil
}
