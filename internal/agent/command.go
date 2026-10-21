package agent

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
)

// RunCommand accepts a single trusted installation file. Ambient management
// credentials and a local kubeconfig never become agent runtime credentials.
func RunCommand(ctx context.Context, args []string, output io.Writer, version string) error {
	flags := flag.NewFlagSet("loomplane-agent", flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("config", "", "absolute path to the agent installation JSON")
	showVersion := flags.Bool("version", false, "print the agent version")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return ErrConfiguration
	}
	if *showVersion {
		_, err := fmt.Fprintln(output, version)
		return err
	}
	if *path == "" {
		return ErrConfiguration
	}
	cfg, err := LoadLaunchConfig(*path)
	if err != nil {
		return err
	}
	return RunConfigured(ctx, cfg)
}
