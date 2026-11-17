// Command hermesh-signer is the protected signer of a Linux trusted device
// (docs/linux.md §7.3). The Hermesh package installs it as a system service,
// as root: it keeps each user's root keys where only root reads them, and
// answers the signing requests of that user's hermeshd (internal/signer).
// Before each signature polkit asks the user for their password in the
// desktop's own dialog, which shows what is signed; no program of the
// user's can sign without them.
//
// A user's Hermesh app asks it, on its socket, to watch the user's
// hermeshd: the socket must be the user's own, and the app's process, in
// the user's desktop session, is the one polkit asks in.
//
//	hermesh-signer serve     the service (systemd starts it on its socket)
//	hermesh-signer version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
)

// version is set when releasing (-X main.version=…).
var version = "dev"

// Where it keeps keys and listens, as the package installs it.
const (
	keysRoot   = "/var/lib/hermesh-signer"
	socketPath = "/run/hermesh-signer.sock"
)

func main() {
	cmd, args := "serve", os.Args[1:]
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serveCommand(args)
	case "version":
		fmt.Println(version)
	default:
		err = fmt.Errorf("unknown command %q (serve, version)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hermesh-signer:", err)
		os.Exit(1)
	}
}

func serveCommand(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := fs.String("listen", socketPath, "the socket the users' apps ask on")
	test := fs.String("test", "", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	keys, owners, err := parts(*test)
	if err != nil {
		return err
	}
	ln, err := listener(*listen)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() { <-ctx.Done(); ln.Close() }()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	log.Info("hermesh-signer serving", "socket", *listen, "version", version)
	err = newServer(keys, owners, log).serve(ctx, ln)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// parts are each user's keys and how the user is asked: files only root
// reads, and polkit. Test builds (-tags testsigner) can keep software keys
// in a folder of the test's and approve everything instead.
var parts = func(test string) (keysFor, ownerFor, error) {
	if test != "" {
		return nil, nil, errors.New("this build has no test signer")
	}
	if os.Geteuid() != 0 {
		return nil, nil, errors.New("the signer runs as root, as its service does: only then can no program of the user's read its keys")
	}
	return func(uid int) fileKeys { return fileKeys{dir: fmt.Sprintf("%s/%d", keysRoot, uid)} }, newPolkitOwner, nil
}

// listener is the socket systemd passes (socket activation), else one of
// its own at path that every user can ask on.
func listener(path string) (net.Listener, error) {
	if os.Getenv("LISTEN_PID") == fmt.Sprint(os.Getpid()) && os.Getenv("LISTEN_FDS") == "1" {
		return net.FileListener(os.NewFile(3, "systemd socket"))
	}
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	return ln, os.Chmod(path, 0o666)
}
