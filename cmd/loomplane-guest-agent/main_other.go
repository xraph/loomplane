//go:build !linux

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "loomplane-guest-agent requires Linux inside a Firecracker VM")
	os.Exit(1)
}
