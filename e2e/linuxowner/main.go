// Command linuxowner plays the Hermesh app's calls on a Linux computer of
// an end-to-end test, where the test can't reach hermeshd's local API from
// outside: it runs there, as the user, and makes one call.
//
//	linuxowner -sock S setup RELAY CLAIM   the first device, its root key the signer's
//	linuxowner -sock S machine NAME        this computer as a machine too (signed)
//	linuxowner -sock S her MACHINE         Her on for a machine (signed)
//	linuxowner -sock S signer              whether a signer watches
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/local/v1/localv1connect"
	"github.com/hermeshai/hermesh/internal/localipc"
)

func main() {
	sock := flag.String("sock", "", "hermeshd's local API")
	flag.Parse()
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return localipc.Dial(ctx, *sock)
	}}}
	c := localv1connect.NewLocalServiceClient(hc, "http://local")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	a := flag.Args()
	var err error
	switch {
	case len(a) == 3 && a[0] == "setup":
		var r *localv1.SetupResponse
		if r, err = c.Setup(ctx, &localv1.SetupRequest{RelayUrl: a[1], ClaimCode: a[2], RootLabel: "test-linux", RootPlatform: "linux"}); err == nil {
			fmt.Println(r.OwnerId)
		}
	case len(a) == 2 && a[0] == "machine":
		var r *localv1.SetLocalMachineResponse
		if r, err = c.SetLocalMachine(ctx, &localv1.SetLocalMachineRequest{Enabled: true, MachineName: a[1]}); err == nil {
			fmt.Println(r.MachineId)
		}
	case len(a) == 2 && a[0] == "her":
		_, err = c.SetHer(ctx, &localv1.SetHerRequest{MachineId: a[1], Enabled: true})
	case len(a) == 1 && a[0] == "signer":
		var r *localv1.GetStatusResponse
		if r, err = c.GetStatus(ctx, &localv1.GetStatusRequest{}); err == nil {
			fmt.Printf("connected=%v busy=%v\n", r.GetSigner().GetConnected(), r.GetSigner().GetBusy())
		}
	default:
		err = fmt.Errorf("usage: linuxowner -sock S setup RELAY CLAIM | machine NAME | her MACHINE | signer")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "linuxowner:", err)
		os.Exit(1)
	}
}
