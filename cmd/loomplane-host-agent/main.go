package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	fc "github.com/xraph/loomplane/internal/firecracker"
)

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if len(os.Args) == 3 && os.Args[1] == "--check" {
		caps, err := fc.CheckHost(ctx, os.Args[2])
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(caps)
	}
	if len(os.Args) != 2 {
		return fc.HostUsage()
	}
	return fc.RunHost(ctx, os.Args[1])
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "loomplane-host-agent:", err)
		os.Exit(1)
	}
}
