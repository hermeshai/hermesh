// Command hermesh is the client CLI: a client (usually an AI agent) pairs
// with an owner, then runs coding-agent sessions (Claude Code, Codex, ACP
// agents) on the owner's machines.
package main

import (
	"os"

	"github.com/hermeshai/hermesh/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
