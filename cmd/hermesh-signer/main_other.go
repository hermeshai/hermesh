//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "hermesh-signer is the signer of a Windows trusted device: macOS has Hermesh Signer.app, and a Linux trusted device signs in hermeshd's terminal")
	os.Exit(2)
}
