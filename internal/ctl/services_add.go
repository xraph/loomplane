package ctl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/client"
	"github.com/xraph/loomplane/pkg/manifest"
)

func (a *App) addServiceCommand() cli.Command {
	return cli.NewCommand("add", "Add a service to a project, from its code, from what analysis found or from the catalog, then plan an environment", a.addService,
		cli.WithUsage(`services add <project> --suggested <name>     # one services suggest lists
  services add <project> --source <path>        # your code: a directory, Dockerfile or Procfile process
  services add <project> --kind <catalog-kind>  # postgres, redis and the rest of the catalog
    [--name <name>] [--env production] [--only] [--needs a,b] [--needed-by a,b]
    [--cloud-target <target> [--cloud <name>] | --provider <provider> [--target <connection>]]
    [--dry-run] [--no-plan] [--json]`),
		cli.WithFlags(jsonFlag,
			cli.NewStringFlag("suggested", "", "A service analysis found that the manifest doesn't run yet, by the name services suggest gives it", ""),
			cli.NewStringFlag("source", "", "Where the service's code is: a directory, a Dockerfile or a Procfile process", ""),
			cli.NewStringFlag("repo", "", "The repository --source is in, when the project has several", ""),
			cli.NewStringFlag("kind", "", "A catalog kind, such as postgres or redis. With --source, what the service is: http-service, web-app, static-site or worker", ""),
			cli.NewStringFlag("name", "", "Its name in the manifest. Defaults to the suggestion's, the kind's or the source's last directory, numbered when taken", ""),
			cli.NewStringFlag("cloud-target", "", "A service runs on this Cloud target, in the project's default Cloud unless --cloud names another", ""),
			cli.NewStringFlag("cloud", "", "The Cloud --cloud-target is in, by name, when it isn't the project's default", ""),
			cli.NewStringFlag("provider", "", "Where it runs. A catalog kind runs on server or kubernetes, and takes it from --target when left out", ""),
			cli.NewStringFlag("target", "", "The checked server or Kubernetes connection it runs on. A catalog kind defaults to the only ready one", ""),
			cli.NewStringFlag("region", "", "The region it runs in, on a provider that has regions", ""),
			cli.NewStringFlag("version", "", "The catalog kind's release. Defaults to its newest", ""),
			cli.NewIntFlag("port", "", "The port the service listens on, when its Dockerfile doesn't say", 0),
			cli.NewStringSliceFlag("needs", "", "What the service needs, comma separated: services and resources already in the manifest", nil),
			cli.NewStringSliceFlag("needed-by", "", "Services that need it, comma separated. Each gets its address in a variable", nil),
			cli.NewStringFlag("env", "e", "The environment to plan, and with --only the one it runs in", "production"),
			cli.NewBoolFlag("only", "", "Run it only in --env: off in every other environment", false),
			cli.NewBoolFlag("dry-run", "", "Print the loomplane.yaml it would save, and change nothing", false),
			cli.NewBoolFlag("no-plan", "", "Save it to the manifest without planning", false),
			timeoutFlag("Maximum time to prepare the plan", 5*time.Minute)))
}

func (a *App) suggestServicesCommand() cli.Command {
	return cli.NewCommand("suggest", "List the services analysis found that the project's manifest doesn't run yet", a.suggestServices,
		cli.WithUsage("services suggest <project> [--json]"),
		cli.WithFlags(jsonFlag))
}

// serviceKinds are what --kind means alongside --source.
var serviceKinds = []string{"http-service", "web-app", "static-site", "worker"}

func (a *App) addService(ctx cli.CommandContext) error {
	suggested := strings.TrimSpace(ctx.String("suggested"))
	source := strings.TrimSpace(ctx.String("source"))
	kind := strings.TrimSpace(ctx.String("kind"))
	env := strings.TrimSpace(ctx.String("env"))
	needs, neededBy := list(ctx.StringSlice("needs")), list(ctx.StringSlice("needed-by"))
	ref := strings.TrimSpace(strings.Join(ctx.Args(), " "))
	if ref == "" {
		return errors.New("name the project: its slug, name or ID")
	}
	// Each way of adding a service is checked before anything is sent.
	var info recipes.KindInfo
	catalog := false
	switch {
	case suggested != "" && source != "":
		return errors.New("add a suggested service or one from --source, not both")
	case suggested != "" || source != "":
		if kind != "" && !slices.Contains(serviceKinds, kind) {
			return fmt.Errorf("with --source, --kind is what the service is: %s. To add %s from the catalog, leave out --source", strings.Join(serviceKinds, ", "), kind)
		}
	case kind != "":
		var ok bool
		if info, ok = kindInfo(a.opts.Recipes, kind); !ok {
			return fmt.Errorf("%s is not a catalog kind", kind)
		}
		if info.Placement == recipes.PlacementServiceProject && !slices.ContainsFunc(info.Targets, func(t string) bool { return t == "server" || t == "kubernetes" }) {
			return fmt.Errorf("%s makes a service project of its own: use services create", kind)
		}
		if len(needs) > 0 {
			return errors.New("--needs is for a service from your code; a catalog kind's dependencies come with it")
		}
		catalog = true
	default:
		return errors.New("say what to add: --suggested <name> from services suggest, --source <path> for your code, or --kind <kind> from the catalog")
	}
	targetProvider := strings.TrimSpace(ctx.String("provider"))
	if catalog && targetProvider != "" && targetProvider != "server" && targetProvider != "kubernetes" {
		return errors.New("a catalog kind added to a project runs on --provider server or kubernetes")
	}
	if catalog && targetProvider != "" && !slices.Contains(info.Targets, targetProvider) {
		return fmt.Errorf("%s does not run on %s", kind, targetProvider)
	}
	if ctx.Bool("only") && env == "" {
		return errors.New("--only needs --env, the environment it runs in")
	}

	bounded, cancel := context.WithTimeout(ctxOf(ctx), time.Duration(ctx.Duration("timeout")))
	defer cancel()
	c, err := a.client()
	if err != nil {
		return err
	}
	project, err := c.FindProject(bounded, ref)
	if err != nil {
		return err
	}
	taken, err := manifestNames(bounded, c, project.ID)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(ctx.String("name"))
	edit := client.ManifestEdit{DryRun: ctx.Bool("dry-run")}
	scope := func(enabledIn *string, only *bool, by *[]string) {
		if ctx.Bool("only") {
			*enabledIn, *only = env, true
		}
		*by = neededBy
	}
	if catalog {
		target := strings.TrimSpace(ctx.String("target"))
		if target == "" {
			if target, targetProvider, err = onlyTarget(bounded, c, info, targetProvider); err != nil {
				return err
			}
		}
		if targetProvider == "" {
			targetProvider = "server"
		}
		version := strings.TrimSpace(ctx.String("version"))
		if version == "" && len(info.Versions) > 0 {
			version = info.Versions[0]
		}
		if name == "" {
			name = freeName(slug(kind), taken)
		}
		r := &client.NewResource{Name: name, Kind: kind, Provider: targetProvider, Target: target, Version: version}
		scope(&r.EnabledIn, &r.Only, &r.NeededBy)
		edit.AddResource = r
	} else {
		var s client.NewService
		if suggested != "" {
			found, findErr := suggestion(bounded, c, project.ID, suggested)
			if findErr != nil {
				return findErr
			}
			s = found
		} else {
			s.Source = source
		}
		set := func(dst *string, flag string) {
			if v := strings.TrimSpace(ctx.String(flag)); v != "" {
				*dst = v
			}
		}
		set(&s.Repo, "repo")
		set(&s.Kind, "kind")
		set(&s.Provider, "provider")
		set(&s.Target, "target")
		if cloudTarget := strings.TrimSpace(ctx.String("cloud-target")); cloudTarget != "" {
			if strings.TrimSpace(ctx.String("provider")) != "" || strings.TrimSpace(ctx.String("target")) != "" {
				return errors.New("a service runs on --cloud-target or on --provider and --target, not both")
			}
			// A Cloud target replaces the provider a suggestion came with.
			s.CloudTarget, s.Cloud, s.Provider, s.Target = cloudTarget, strings.TrimSpace(ctx.String("cloud")), "", ""
		} else if strings.TrimSpace(ctx.String("cloud")) != "" {
			return errors.New("--cloud names the Cloud a --cloud-target is in; pass the target too")
		}
		set(&s.Region, "region")
		if port := ctx.Int("port"); port > 0 {
			s.Port = port
		}
		if len(needs) > 0 {
			s.Needs = needs
		}
		switch {
		case name != "":
			s.Name = name
		case s.Name == "":
			s.Name = freeName(slug(lastPart(s.Source)), taken)
		}
		name = s.Name
		scope(&s.EnabledIn, &s.Only, &s.NeededBy)
		edit.AddService = &s
	}

	revision, err := c.EditManifest(bounded, project.ID, edit)
	if err != nil {
		return fmt.Errorf("add %s to %s: %w", name, project.Name, err)
	}
	if edit.DryRun {
		if ctx.Bool("json") {
			return a.json(revision)
		}
		a.printf("# loomplane.yaml for %s with %s added. Nothing was saved.\n%s", project.Name, name, revision.YAML)
		return nil
	}
	if ctx.Bool("no-plan") {
		if ctx.Bool("json") {
			return a.json(revision)
		}
		a.printf("Added %s to %s's loomplane.yaml, revision %d.\nPlan it with: loomctl plan %s --env %s\n", name, project.Name, revision.Revision, project.ID, env)
		return nil
	}
	configuration, parseErr := manifest.Parse([]byte(revision.YAML))
	// An unknown configuration keeps normal source-pinning requirements.
	catalogOnly := parseErr == nil && configuration.CatalogOnly()
	plan, err := c.CreatePlan(bounded, project.ID, client.NewPlan{Environment: env, Catalog: catalogOnly})
	if err != nil {
		return fmt.Errorf("%s is in %s's manifest now, but planning %s didn't start: %w. Plan it with: loomctl plan %s --env %s", name, project.Name, env, err, project.ID, env)
	}
	if plan, err = c.WaitPlan(bounded, plan.ID); err != nil {
		return err
	}
	if ctx.Bool("json") {
		return a.json(plan)
	}
	a.printf("Added %s to %s.\n", name, project.Name)
	a.printPlan(plan)
	return nil
}

func (a *App) suggestServices(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	project, err := a.project(ctx, c)
	if err != nil {
		return err
	}
	out, err := c.SuggestServices(ctx.Context(), project.ID)
	if err != nil {
		return err
	}
	if ctx.Bool("json") {
		return a.json(out)
	}
	switch {
	case !out.Analyzed:
		a.printf("Nothing to suggest until an analysis of %s finishes. Run: loomctl analyze %s\n", project.Name, project.ID)
		return nil
	case len(out.Services) == 0:
		a.printf("%s's manifest runs every service analysis found. Add another with --source or --kind.\n", project.Name)
		return nil
	}
	rows := make([][]string, 0, len(out.Services))
	for _, s := range out.Services {
		provider := s.Service.Provider
		if provider == "" {
			provider = "(default)"
		}
		needs := strings.Join(s.Service.Needs, ", ")
		if needs == "" {
			needs = "-"
		}
		rows = append(rows, []string{s.Service.Name, s.Service.Source, s.Kind, provider, needs})
	}
	a.table([]string{"NAME", "SOURCE", "KIND", "PROVIDER", "NEEDS"}, rows)
	a.printf("\nAdd one with: loomctl services add %s --suggested %s\n", project.ID, out.Services[0].Service.Name)
	return nil
}

// manifestNames is everything the project's manifest already names, and
// nothing for a project with no manifest yet.
func manifestNames(ctx context.Context, c *client.Client, projectID string) (map[string]bool, error) {
	taken := map[string]bool{}
	rev, err := c.Manifest(ctx, projectID)
	var apiErr *client.Error
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return taken, nil
	}
	if err != nil {
		return nil, err
	}
	m, err := manifest.Parse([]byte(rev.YAML))
	if err != nil {
		return taken, nil //nolint:nilerr // A manifest that doesn't parse names nothing to avoid; the edit says what's wrong.
	}
	for _, names := range [][]string{m.ServiceNames(), m.ResourceNames(), m.GatewayNames(), m.LinkNames()} {
		for _, n := range names {
			taken[n] = true
		}
	}
	return taken, nil
}

// suggestion is the suggested service called name, or found at that
// source or path.
func suggestion(ctx context.Context, c *client.Client, projectID, name string) (client.NewService, error) {
	out, err := c.SuggestServices(ctx, projectID)
	if err != nil {
		return client.NewService{}, err
	}
	if !out.Analyzed {
		return client.NewService{}, errors.New("nothing is suggested until an analysis finishes: run loomctl analyze first")
	}
	var names []string
	for _, s := range out.Services {
		if s.Service.Name == name || s.Service.Source == name || s.Path == name {
			return s.Service, nil
		}
		names = append(names, s.Service.Name)
	}
	if len(names) == 0 {
		return client.NewService{}, fmt.Errorf("nothing is suggested: the manifest runs every service analysis found")
	}
	return client.NewService{}, fmt.Errorf("no suggested service is called %s. Suggested: %s", name, strings.Join(names, ", "))
}

// onlyTarget is the one ready connection a catalog kind can run on, and
// the provider it means, when there's exactly one.
func onlyTarget(ctx context.Context, c *client.Client, info recipes.KindInfo, provider string) (target, targetProvider string, err error) {
	all, err := c.Connections(ctx)
	if err != nil {
		return "", "", err
	}
	var ids, labels []string
	var providers []string
	for _, conn := range all {
		if conn.Status != "ready" || !slices.Contains(info.Targets, conn.Cloud) || (provider != "" && conn.Cloud != provider) {
			continue
		}
		if conn.Cloud != "server" && conn.Cloud != "kubernetes" {
			continue
		}
		if conn.Cloud == "kubernetes" && conn.Details["namespace"] == "" {
			continue
		}
		ids = append(ids, conn.ID)
		providers = append(providers, conn.Cloud)
		labels = append(labels, fmt.Sprintf("%s (%s, %s)", conn.ID, conn.Name, conn.Cloud))
	}
	switch len(ids) {
	case 1:
		return ids[0], providers[0], nil
	case 0:
		return "", "", fmt.Errorf("no ready server or Kubernetes connection runs %s. Connect one under Integrations, then try again, or name one with --target", info.Name)
	}
	return "", "", fmt.Errorf("choose where %s runs with --target: %s", info.Name, strings.Join(labels, "; "))
}

// list splits each value on commas and drops the blanks.
func list(values []string) []string {
	var out []string
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

var notNameChars = regexp.MustCompile(`[^a-z0-9]+`)

// slug makes s a manifest name: lower case letters, digits and hyphens,
// starting with a letter, at most 40 long.
func slug(s string) string {
	n := strings.Trim(notNameChars.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if n == "" || n[0] < 'a' || n[0] > 'z' {
		n = strings.TrimSuffix("service-"+n, "-")
	}
	return strings.TrimRight(n[:min(len(n), 40)], "-")
}

// lastPart is what a source is named for: its last directory, its
// Procfile process, or the directory its Dockerfile is in.
func lastPart(source string) string {
	if i := strings.LastIndex(source, ":"); i >= 0 {
		source = source[i+1:]
	}
	source = strings.TrimSuffix(source, "/")
	base := path.Base(source)
	if strings.HasPrefix(strings.ToLower(base), "dockerfile") || strings.HasSuffix(strings.ToLower(base), ".dockerfile") {
		if dir := path.Dir(source); dir != "." && dir != "/" {
			return path.Base(dir)
		}
		return "app"
	}
	if base == "." || base == "/" {
		return "app"
	}
	return base
}

// freeName is base, or the first base-N, that isn't taken.
func freeName(base string, taken map[string]bool) string {
	if !taken[base] {
		return base
	}
	for n := 2; ; n++ {
		suffix := fmt.Sprintf("-%d", n)
		if name := strings.TrimRight(base[:min(len(base), 40-len(suffix))], "-") + suffix; !taken[name] {
			return name
		}
	}
}
