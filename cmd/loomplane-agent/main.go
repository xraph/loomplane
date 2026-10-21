// Command loomplane-agent observes a destination and receives signed policy.
// Every process requires a fresh installation enrollment and local SPIRE identity.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/xraph/loomplane/internal/agent"
	"github.com/xraph/loomplane/internal/buildinfo"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "loomplane-agent:", err)
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	err := agent.RunCommand(ctx, os.Args[1:], os.Stdout, buildinfo.Version)
	if ctx.Err() != nil && errors.Is(err, context.Canceled) && !errors.Is(err, agent.ErrCleanup) {
		return nil
	}
	return err
}
