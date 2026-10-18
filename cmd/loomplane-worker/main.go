// Command loomplane-worker analyzes repositories: it checks out a commit,
// reads it, and stores the application graph it finds. It reads untrusted
// code, so it runs apart from the API, and it never runs that code.
//
//	loomplane-worker              work the analysis queue
//	loomplane-worker --help       list every command
//
// Start loomplane first: it migrates the database this process uses.
//
// Keep this package to this one file, like cmd/loomplane.
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/joho/godotenv"
	"github.com/xraph/forge"
	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/internal/buildinfo"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/platform"
)

func main() {
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "loomplane-worker: load .env:", err)
		os.Exit(1)
	}
	cli.NewAppRunner(newApp).
		Name("loomplane-worker").
		Version(buildinfo.Version).
		Description("Loomplane's repository analysis worker").
		DisableMigrationCommands().
		Run()
}

func newApp(cli.CommandContext) (forge.App, error) {
	dir := os.Getenv("LOOMPLANE_CONFIG_DIR")
	if dir == "" {
		dir = "."
	}
	cfg, err := config.Load(dir)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return platform.NewWorker(cfg)
}
