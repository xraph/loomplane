package ctl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xraph/forge/cli"
	"gopkg.in/yaml.v3"

	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/client"
	"github.com/xraph/loomplane/pkg/manifest"
)

func (a *App) servicesCommand() cli.Command {
	return cli.NewCommand("services", "Prepare catalog services without a repository", nil,
		cli.WithSubcommand(cli.NewCommand("create", "Plan a standalone database service", a.createCatalogService,
			cli.WithUsage("services create <project-name> --target <connection-id> [--provider server|kubernetes] [--env production] [--json]"),
			cli.WithFlags(jsonFlag, cli.NewStringFlag("kind", "", "Database engine: postgres, mysql or mariadb", "postgres"), cli.NewStringFlag("provider", "", "Database target: server or kubernetes (PostgreSQL only)", "server"), cli.NewStringFlag("target", "", "Checked server or Kubernetes connection", ""), cli.NewStringFlag("env", "e", "Environment", "production"), timeoutFlag("Maximum time to prepare the plan", 5*time.Minute)))))
}

func (a *App) createCatalogService(ctx cli.CommandContext) error {
	name := strings.TrimSpace(strings.Join(ctx.Args(), " "))
	target := strings.TrimSpace(ctx.String("target"))
	if name == "" || target == "" {
		return errors.New("provide a project name and --target with a checked server or Kubernetes connection")
	}
	kind := ctx.String("kind")
	version := recipes.DefaultVersion(kind)
	if version == "" {
		return errors.New("choose --kind postgres, mysql or mariadb")
	}
	targetProvider := ctx.String("provider")
	if targetProvider != "server" && targetProvider != "kubernetes" {
		return errors.New("choose --provider server or kubernetes")
	}
	if targetProvider == "kubernetes" && kind != "postgres" {
		return errors.New("kubernetes currently supports --kind postgres only")
	}
	duration := time.Duration(ctx.Duration("timeout"))
	bounded, cancel := context.WithTimeout(ctxOf(ctx), duration)
	defer cancel()
	c, err := a.client()
	if err != nil {
		return err
	}
	project, err := c.CreateProject(bounded, client.NewProject{Name: name, Description: "Standalone " + kind + " service"})
	if err != nil {
		return err
	}
	if _, err = c.WaitProjectReady(bounded, project.ID); err != nil {
		return fmt.Errorf("project %s created; setup is not ready: %w", project.ID, err)
	}
	raw, err := yaml.Marshal(manifest.Manifest{Version: manifest.Version, Resources: map[string]manifest.Resource{"database": {Kind: kind, Provider: targetProvider, Version: version, Target: target}}})
	if err != nil {
		return err
	}
	if _, err = c.SaveManifest(bounded, project.ID, string(raw)); err != nil {
		return fmt.Errorf("save configuration for project %s: %w", project.ID, err)
	}
	plan, err := c.CreatePlan(bounded, project.ID, client.NewPlan{Environment: ctx.String("env"), Catalog: true})
	if err != nil {
		return fmt.Errorf("plan project %s: %w", project.ID, err)
	}
	plan, err = c.WaitPlan(bounded, plan.ID)
	if err != nil {
		return err
	}
	if ctx.Bool("json") {
		return a.json(plan)
	}
	a.printf("Created service project %s.\n", project.ID)
	a.printPlan(plan)
	return nil
}
