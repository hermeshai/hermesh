// Command hermesh-signer is the protected signer of a Windows trusted
// device (docs/windows.md §7.3). Set up once, elevated (one UAC prompt),
// it runs from Program Files as a task of this user's at the highest
// privileges, from logon: no program of the user's that is not elevated
// can use its root keys or touch its panel. It answers hermeshd's signing
// requests (internal/signer): its panel shows what a batch signs, Windows
// Hello confirms the owner is there, and the TPM signs.
//
//	hermesh-signer run                 the signer, as its task runs it
//	hermesh-signer setup --user <SID>  sets it up for that user (elevated)
//	hermesh-signer remove --user <SID> takes it away again (elevated)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"

	"github.com/hermeshai/hermesh/gen/go/hermesh/local/v1/localv1connect"
	"github.com/hermeshai/hermesh/internal/localipc"
	"github.com/hermeshai/hermesh/internal/paths"
	"github.com/hermeshai/hermesh/internal/signer"
)

// version is set when releasing (-X main.version=…).
var version = "dev"

func main() {
	cmd, args := "run", os.Args[1:]
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "run":
		err = run(args)
	case "setup":
		err = setup(args)
	case "remove":
		err = remove(args)
	case "version":
		fmt.Println(version)
	default:
		err = fmt.Errorf("unknown command %q (run, setup, remove, version)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hermesh-signer:", err)
		var r *refusal
		if errors.As(err, &r) {
			os.Exit(r.code)
		}
		os.Exit(1)
	}
}

// refusal: this PC can't hold a key apart from the user's other programs.
// Its exit status tells the app which, for its own words.
type refusal struct {
	code int
	msg  string
}

func (r *refusal) Error() string { return r.msg }

var (
	errNotAdmin = &refusal{3, "this PC can be a trusted device from an administrator account only: Windows elevated another account, which would hold the key"}
	errUAC      = &refusal{4, "User Account Control is off, so every program of yours runs elevated and nothing keeps them from the key: turn it on to make this PC a trusted device"}
	errNoTPM    = &refusal{5, "this PC has no TPM 2.0 to keep the key in"}
)

// run answers the signing requests of the owner role in dataDir until the
// session ends: one signer per user (another one leaves).
func run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	dataDir := fs.String("data-dir", paths.DataDir(), "Hermesh's data folder")
	socket := fs.String("socket", "", "the owner role's local API (default: the data folder's)")
	test := fs.String("test", "", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	sid, err := userSID()
	if err != nil {
		return err
	}
	name, _ := windows.UTF16PtrFromString(`Local\Hermesh Signer ` + sid)
	if _, err := windows.CreateMutex(nil, false, name); errors.Is(err, windows.ERROR_ALREADY_EXISTS) && *test == "" {
		return nil
	}
	keys, owner, err := parts(sid, *test)
	if err != nil {
		return err
	}
	if *socket == "" {
		*socket = paths.LocalAPI(*dataDir)
	}
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return localipc.Dial(ctx, *socket)
	}}}
	signer.Run(context.Background(), localv1connect.NewLocalServiceClient(hc, "http://local"), keys, owner)
	return nil
}

// parts are the TPM's keys and the panel; test builds (-tags testsigner)
// can run with software keys that approve everything instead.
var parts = func(sid, test string) (signer.Keys, signer.Owner, error) {
	if test != "" {
		return nil, nil, errors.New("this build has no test signer")
	}
	return tpmKeys{dir: keysDir(sid)}, newPanel(), nil
}

// keysDir holds what opens each root key of the user sid, in a folder only
// administrators and the system can read.
func keysDir(sid string) string {
	return filepath.Join(os.Getenv("ProgramData"), "Hermesh Signer", "keys", sid)
}

func userSID() (string, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}
