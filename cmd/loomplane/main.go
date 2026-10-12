// Command loomplane runs the Loomplane control plane.
//
//	loomplane              serve the API and dashboard (also serve, start, run)
//	loomplane info         print name, version and environment
//	loomplane extensions   list the registered extensions
//	loomplane --help       list every command
//
// Keep this package to this one file: `forge dev` builds main.go on its
// own, so anything in a sibling file is undefined there. Command logic
// belongs in internal/.
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/joho/godotenv"
	"github.com/xraph/forge"
	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/platform"
)

func main() {
	// Forge never reads .env; load it here so local runs pick up secrets.
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "loomplane: load .env:", err)
		os.Exit(1)
	}
	cli.NewAppRunner(newApp).
		Name("loomplane").
		Version(platform.Version).
		Description("Loomplane: your infrastructure, woven together").
		// Migrations run on every start, in one pass between registering
		// and starting the extensions (internal/platform). Forge's migrate
		// commands look for a CentralMigrator, which the grove extension
		// doesn't provide, so they'd only report nothing to do.
		DisableMigrationCommands().
		Run()
}

// newApp runs after flags parse and before the chosen command. Building the
// app does no I/O; Forge registers and starts extensions when it starts.
func newApp(cli.CommandContext) (forge.App, error) {
	dir := os.Getenv("LOOMPLANE_CONFIG_DIR")
	if dir == "" {
		dir = "."
	}
	cfg, err := config.Load(dir)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return platform.New(cfg)
}
