package ctl

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
			cli.WithUsage("services create <project-name> --target <connection-id> [--provider server|kubernetes|rds|cloud-sql] [--region <cloud-region>] [--env production] [--json]"),
			cli.WithFlags(jsonFlag,
				cli.NewStringFlag("kind", "", "Database engine: postgres, mysql, mariadb, redis or valkey, or any other kind that makes its own service project", "postgres"),
				cli.NewStringFlag("provider", "", "Database target: server, kubernetes, rds or cloud-sql (non-server profiles support PostgreSQL only)", "server"),
				cli.NewStringFlag("target", "", "Checked server, Kubernetes or cloud account connection", ""),
				cli.NewStringFlag("region", "", "Managed database region; defaults to the provider or connection region", ""),
				cli.NewStringFlag("env", "e", "Environment", "production"),
				timeoutFlag("Maximum time to prepare the plan", 5*time.Minute)))),
		cli.WithSubcommand(cli.NewCommand("add", "Plan a catalog service added to an existing project", a.addCatalogService,
			cli.WithUsage("services add <project-id> --kind <kind> --name <resource> --target <connection-id> [--provider server|kubernetes] [--version <version>] [--env production] [--json]"),
			cli.WithFlags(jsonFlag,
				cli.NewStringFlag("kind", "", "Catalog kind to add, such as postgres or a composed service", ""),
				cli.NewStringFlag("name", "", "Name of the resource in the manifest", ""),
				cli.NewStringFlag("provider", "", "Where it runs: server or kubernetes", "server"),
				cli.NewStringFlag("target", "", "Checked server or Kubernetes connection", ""),
				cli.NewStringFlag("version", "", "Release of the kind; defaults to its newest", ""),
				cli.NewStringFlag("env", "e", "Environment", "production"),
				timeoutFlag("Maximum time to prepare the plan", 5*time.Minute)))))
}

// serviceProjectVersion is the newest version of a kind that makes its own
// service project, as a database does, and "" for any other name. Kinds that
// live inside an app project are added with services add.
func serviceProjectVersion(reg *recipes.Registry, kind string) string {
	for _, info := range reg.Kinds() {
		if info.Name == kind && info.Placement == recipes.PlacementServiceProject && len(info.Versions) > 0 {
			return info.Versions[0]
		}
	}
	return ""
}

// kindInfo describes a catalog kind, if there is one.
func kindInfo(reg *recipes.Registry, kind string) (recipes.KindInfo, bool) {
	for _, info := range reg.Kinds() {
		if info.Name == kind {
			return info, true
		}
	}
	return recipes.KindInfo{}, false
}

func (a *App) createCatalogService(ctx cli.CommandContext) error {
	name := strings.TrimSpace(strings.Join(ctx.Args(), " "))
	target := strings.TrimSpace(ctx.String("target"))
	if name == "" || target == "" {
		return errors.New("provide a project name and --target with a checked server, Kubernetes or cloud account connection")
	}
	kind := ctx.String("kind")
	version := serviceProjectVersion(a.opts.Recipes, kind)
	if version == "" {
		if info, ok := kindInfo(a.opts.Recipes, kind); ok && info.Placement == recipes.PlacementAppProject {
			return fmt.Errorf("%s is added to an existing project: use services add", kind)
		}
		return errors.New("choose --kind postgres, mysql, mariadb, redis or valkey")
	}
	targetProvider := ctx.String("provider")
	managedCloud := ""
	switch targetProvider {
	case "server", "kubernetes":
	case "rds":
		managedCloud = "aws"
	case "cloud-sql":
		managedCloud = "gcp"
	default:
		return errors.New("choose --provider server, kubernetes, rds or cloud-sql")
	}
	if targetProvider != "server" && kind != "postgres" {
		return fmt.Errorf("%s currently supports --kind postgres only", targetProvider)
	}
	region := strings.TrimSpace(ctx.String("region"))
	if region != "" && managedCloud == "" {
		return errors.New("--region applies to rds or cloud-sql; server and Kubernetes placement comes from --target")
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
	resource := manifest.Resource{Kind: kind, Provider: targetProvider, Version: version, Region: region}
	if managedCloud == "" {
		resource.Target = target
	} else if chooseErr := c.ChooseEnvironmentConnection(bounded, project.ID, ctx.String("env"), managedCloud, target); chooseErr != nil {
		return fmt.Errorf("choose deployment connection for project %s: %w", project.ID, chooseErr)
	}
	raw, err := yaml.Marshal(manifest.Manifest{Version: manifest.Version, Resources: map[string]manifest.Resource{"database": resource}})
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

func (a *App) addCatalogService(ctx cli.CommandContext) error {
	projectID := strings.TrimSpace(strings.Join(ctx.Args(), " "))
	name := strings.TrimSpace(ctx.String("name"))
	target := strings.TrimSpace(ctx.String("target"))
	kind := strings.TrimSpace(ctx.String("kind"))
	if projectID == "" || name == "" || target == "" || kind == "" {
		return errors.New("provide a project id, --kind, --name and --target with a checked server or Kubernetes connection")
	}
	targetProvider := ctx.String("provider")
	if targetProvider != "server" && targetProvider != "kubernetes" {
		return errors.New("choose --provider server or kubernetes")
	}
	info, ok := kindInfo(a.opts.Recipes, kind)
	if !ok {
		return fmt.Errorf("%s is not a catalog kind", kind)
	}
	if !slices.Contains(info.Targets, targetProvider) {
		return fmt.Errorf("%s does not run on %s", kind, targetProvider)
	}
	version := strings.TrimSpace(ctx.String("version"))
	if version == "" && len(info.Versions) > 0 {
		version = info.Versions[0]
	}
	duration := time.Duration(ctx.Duration("timeout"))
	bounded, cancel := context.WithTimeout(ctxOf(ctx), duration)
	defer cancel()
	c, err := a.client()
	if err != nil {
		return err
	}
	if _, err = c.EditManifest(bounded, projectID, client.ManifestEdit{AddResource: &client.NewResource{
		Name: name, Kind: kind, Provider: targetProvider, Target: target, Version: version,
	}}); err != nil {
		return fmt.Errorf("add %s to the configuration of project %s: %w", name, projectID, err)
	}
	plan, err := c.CreatePlan(bounded, projectID, client.NewPlan{Environment: ctx.String("env")})
	if err != nil {
		return fmt.Errorf("plan project %s: %w", projectID, err)
	}
	plan, err = c.WaitPlan(bounded, plan.ID)
	if err != nil {
		return err
	}
	if ctx.Bool("json") {
		return a.json(plan)
	}
	a.printf("Added %s to project %s.\n", name, projectID)
	a.printPlan(plan)
	return nil
}
