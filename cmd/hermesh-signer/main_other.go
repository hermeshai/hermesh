//go:build !windows && !linux

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "hermesh-signer is the signer of a Windows or Linux trusted device: macOS has Hermesh Signer.app")
	os.Exit(2)
}
