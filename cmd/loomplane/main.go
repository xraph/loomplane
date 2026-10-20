// Command loomplane runs the Loomplane control plane.
//
//	loomplane              serve the API and dashboard (also serve, start, run)
//	loomplane info         print name, version and environment
//	loomplane extensions   list the registered extensions
//	loomplane backup       back up the database to a file
//	loomplane restore      restore the database from a backup
//	loomplane migrations   show applied and pending migrations
//	loomplane migrate      apply pending migrations
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

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/buildinfo"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/ops"
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
		Version(buildinfo.Version).
		Description("Loomplane: your infrastructure, woven together").
		// Loomplane's operational commands below own migration and restore
		// handling. Disable Forge's overlapping command names.
		DisableMigrationCommands().
		WithExtraCommands(ops.Commands(settings)...).
		Run()
}

// newApp runs after flags parse and before the chosen command. Building the
// app does no I/O; Forge registers and starts extensions when it starts.
func newApp(cli.CommandContext) (forge.App, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	return platform.New(cfg)
}

func loadConfig() (config.Config, error) {
	dir := os.Getenv("LOOMPLANE_CONFIG_DIR")
	if dir == "" {
		dir = "."
	}
	cfg, err := config.Load(dir)
	if err != nil {
		return config.Config{}, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

// settings are what the operational commands need.
func settings() (ops.Settings, error) {
	cfg, err := loadConfig()
	if err != nil {
		return ops.Settings{}, err
	}
	return ops.Settings{DatabaseURL: cfg.Database.URL, Groups: migrations.All(), Version: buildinfo.Version}, nil
}
