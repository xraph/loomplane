package ctl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xraph/forge/cli"
	"gopkg.in/yaml.v3"

	"github.com/xraph/loomplane/pkg/client"
	"github.com/xraph/loomplane/pkg/manifest"
)

func (a *App) servicesCommand() cli.Command {
	return cli.NewCommand("services", "Prepare catalog services without a repository", nil,
		cli.WithSubcommand(cli.NewCommand("create", "Plan a standalone PostgreSQL 17 service", a.createCatalogService,
			cli.WithUsage("services create <project-name> --target <server-connection-id> [--env production] [--json]"),
			cli.WithFlags(jsonFlag, cli.NewStringFlag("target", "", "Checked Docker server connection", ""), cli.NewStringFlag("env", "e", "Environment", "production"), timeoutFlag("Maximum time to prepare the plan", 5*time.Minute)))))
}

func (a *App) createCatalogService(ctx cli.CommandContext) error {
	name := strings.TrimSpace(strings.Join(ctx.Args(), " "))
	target := strings.TrimSpace(ctx.String("target"))
	if name == "" || target == "" {
		return errors.New("provide a project name and --target with a checked Docker server connection")
	}
	duration := time.Duration(ctx.Duration("timeout"))
	bounded, cancel := context.WithTimeout(ctxOf(ctx), duration)
	defer cancel()
	c, err := a.client()
	if err != nil {
		return err
	}
	project, err := c.CreateProject(bounded, client.NewProject{Name: name, Description: "Standalone PostgreSQL service"})
	if err != nil {
		return err
	}
	if _, err = c.WaitProjectReady(bounded, project.ID); err != nil {
		return fmt.Errorf("project %s created; setup is not ready: %w", project.ID, err)
	}
	raw, err := yaml.Marshal(manifest.Manifest{Version: manifest.Version, Resources: map[string]manifest.Resource{"database": {Kind: "postgres", Provider: "server", Version: "17", Target: target}}})
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
