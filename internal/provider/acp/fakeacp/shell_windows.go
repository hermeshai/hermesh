package main

import "os/exec"

// shellCommand runs a scripted tool's command line.
func shellCommand(line string) *exec.Cmd { return exec.Command("cmd", "/c", line) }
