package ctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/pkg/client"
)

// infrastructureInput reads a typed reviewed API input, never an arbitrary URL.
func (a *App) infrastructureInput(ctx cli.CommandContext, value any) error {
	reader := a.opts.In
	if name := ctx.String("file"); name != "-" {
		if name == "" {
			return errors.New("give --file with the typed JSON input, or - for standard input")
		}
		f, err := os.Open(name)
		if err != nil {
			return err
		}
		defer f.Close()
		reader = f
	}
	dec := json.NewDecoder(reader)
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("input must contain one JSON object")
	}
	return nil
}
func infraCommand(name, usage, description string, handler cli.CommandHandler, flags ...cli.Flag) cli.Command {
	all := []cli.Flag{jsonFlag}
	for _, flag := range flags {
		if flag.Name() != "json" {
			all = append(all, flag)
		}
	}
	return cli.NewCommand(name, description, handler, cli.WithUsage(usage), cli.WithFlags(all...))
}
func infraGroup(name, usage, description string, handler cli.CommandHandler, subcommands ...cli.Command) cli.Command {
	options := make([]cli.CommandOption, 0, 2+len(subcommands))
	options = append(options, cli.WithUsage(usage), cli.WithFlag(jsonFlag))
	for _, subcommand := range subcommands {
		options = append(options, cli.WithSubcommand(subcommand))
	}
	return cli.NewCommand(name, description, handler, options...)
}

func infraFileFlag() cli.Flag {
	return cloudFlag("file", "Typed JSON input file; - reads standard input")
}
func infraYesFlag() cli.Flag {
	return cli.NewBoolFlag("yes", "y", "Accept the exact displayed review", false)
}
func infraRequired(ctx cli.CommandContext, count int) error {
	for i := 0; i < count; i++ {
		if ctx.Arg(i) == "" {
			return errors.New("give every resource ID shown in command usage")
		}
	}
	return nil
}
func (a *App) cloudServersCommand() cli.Command {
	subcommands := make([]cli.Command, 0, 10)
	for _, spec := range []struct {
		name, description string
		handler           cli.CommandHandler
		flags             []cli.Flag
	}{
		{"options", "Read the connected provider's server catalog", a.infraServerOptions, nil},
		{"check", "Refresh pinned SSH and Docker health", a.checkInfraServer, nil},
		{"usage", "Show reservations, serving workloads and retained storage", a.infraServerUsage, nil},
		{"preview", "Review create, adopt, resize, disconnect or delete", a.previewInfraServer, []cli.Flag{infraFileFlag()}},
		{"apply", "Fetch and accept a saved server operation review", a.applyInfraServer, append(recoveryFlags(), infraYesFlag(), cloudFlag("review", "Exact saved review token"))},
		{"drain", "Review disabling new admission, then accept with --yes", a.drainInfraServer, []cli.Flag{infraYesFlag(), cloudFlag("review", "Exact saved drain digest for --yes")}},
		{"operations", "List persisted server operation history", a.infraOperations, nil},
		{"show", "Show one server operation and provider action progress", a.showInfraOperation, recoveryFlags()},
		{"retry", "Resume the recorded operation after recovery", a.retryInfraOperation, recoveryFlags()},
		{"cancel", "Cancel an operation through its recorded cleanup authority", a.cancelInfraOperation, nil},
	} {
		subcommands = append(subcommands, infraCommand(spec.name, "cloud-servers "+spec.name+" <cloud-id> [resource-id]", spec.description, spec.handler, spec.flags...))
	}
	return infraGroup("cloud-servers", "cloud-servers <cloud-id>", "List organization server capacity", a.listInfraServers, subcommands...)
}
func (a *App) listInfraServers(ctx cli.CommandContext) error {
	if err := infraRequired(ctx, 1); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	v, err := c.CloudServers(ctxOf(ctx), ctx.Arg(0))
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) infraServerOptions(ctx cli.CommandContext) error {
	if err := infraRequired(ctx, 2); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	v, err := c.CloudServerOptions(ctxOf(ctx), ctx.Arg(0), ctx.Arg(1))
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) checkInfraServer(ctx cli.CommandContext) error {
	if err := infraRequired(ctx, 2); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	v, err := c.CheckCloudServer(ctxOf(ctx), ctx.Arg(0), ctx.Arg(1))
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) infraServerUsage(ctx cli.CommandContext) error {
	if err := infraRequired(ctx, 2); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	v, err := c.CloudServerUsage(ctxOf(ctx), ctx.Arg(0), ctx.Arg(1))
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) previewInfraServer(ctx cli.CommandContext) error {
	if err := infraRequired(ctx, 1); err != nil {
		return err
	}
	var in client.ServerInput
	if err := a.infrastructureInput(ctx, &in); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	v, err := c.PreviewCloudServer(ctxOf(ctx), ctx.Arg(0), in)
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) infraOperations(ctx cli.CommandContext) error {
	if err := infraRequired(ctx, 1); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	v, err := c.InfrastructureOperations(ctxOf(ctx), ctx.Arg(0))
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) applyInfraServer(ctx cli.CommandContext) error {
	if err := infraRequired(ctx, 2); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	v, err := c.InfrastructureOperation(ctxOf(ctx), ctx.Arg(0), ctx.Arg(1))
	if err != nil {
		return err
	}
	if printErr := a.json(v); printErr != nil {
		return printErr
	}
	if !ctx.Bool("yes") {
		return nil
	}
	if ctx.String("review") == "" || ctx.String("review") != v.Review {
		return errors.New("review differs; fetch the operation and review again")
	}
	v, err = c.ApplyInfrastructureOperation(ctxOf(ctx), ctx.Arg(0), v.ID, client.InfrastructureApplyInput{Review: v.Review})
	if err != nil {
		return err
	}
	return a.infraOperationResult(ctx, c, v)
}
func (a *App) showInfraOperation(ctx cli.CommandContext) error {
	return a.infraOperationAction(ctx, "show")
}
func (a *App) retryInfraOperation(ctx cli.CommandContext) error {
	return a.infraOperationAction(ctx, "retry")
}
func (a *App) cancelInfraOperation(ctx cli.CommandContext) error {
	return a.infraOperationAction(ctx, "cancel")
}
func (a *App) infraOperationAction(ctx cli.CommandContext, action string) error {
	if err := infraRequired(ctx, 2); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	var v *client.InfrastructureOperation
	switch action {
	case "retry":
		v, err = c.RetryInfrastructureOperation(ctxOf(ctx), ctx.Arg(0), ctx.Arg(1))
	case "cancel":
		v, err = c.CancelInfrastructureOperation(ctxOf(ctx), ctx.Arg(0), ctx.Arg(1))
	default:
		v, err = c.InfrastructureOperation(ctxOf(ctx), ctx.Arg(0), ctx.Arg(1))
	}
	if err != nil {
		return err
	}
	return a.infraOperationResult(ctx, c, v)
}
func (a *App) infraOperationResult(ctx cli.CommandContext, c *client.Client, v *client.InfrastructureOperation) error {
	if !ctx.Bool("wait") {
		return a.json(v)
	}
	bounded, cancel := context.WithTimeout(ctxOf(ctx), time.Duration(ctx.Duration("timeout")))
	defer cancel()
	latest, err := c.WaitInfrastructureOperation(bounded, ctx.Arg(0), v.ID)
	if latest != nil {
		v = latest
	}
	if err != nil {
		return errors.Join(err, a.json(v))
	}
	if printErr := a.json(v); printErr != nil {
		return printErr
	}
	if v.Status != "succeeded" {
		return fmt.Errorf("server operation %s is %s: %s", v.ID, v.Status, v.Message)
	}
	return nil
}
func (a *App) drainInfraServer(ctx cli.CommandContext) error {
	if err := infraRequired(ctx, 2); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	v, err := c.PreviewCloudServerDrain(ctxOf(ctx), ctx.Arg(0), ctx.Arg(1))
	if err != nil {
		return err
	}
	if printErr := a.json(v); printErr != nil {
		return printErr
	}
	if !ctx.Bool("yes") {
		return nil
	}
	if ctx.String("review") == "" || ctx.String("review") != v.Review {
		return errors.New("drain inventory changed; review again")
	}
	server, err := c.ApplyCloudServerDrain(ctxOf(ctx), ctx.Arg(0), ctx.Arg(1), client.InfrastructureApplyInput{Review: v.Review})
	if err != nil {
		return err
	}
	return a.json(server)
}

func (a *App) cloudPoolsCommand() cli.Command {
	subcommands := []cli.Command{
		infraCommand("save", "cloud-pools save <cloud-id> [pool-id] --file pool.json", "Create or update pool membership at its revision", a.saveInfraPool, infraFileFlag()),
		infraCommand("move", "cloud-pools move <cloud-id> --file move.json [--yes]", "Review a stateless move intent for your next deployment", a.moveInfraPool, infraFileFlag(), infraYesFlag()),
		infraCommand("recover", "cloud-pools recover <cloud-id> --file recovery.json [--yes]", "Review accepted reservation cleanup without releasing serving workloads", a.recoverInfraReservation, infraFileFlag(), infraYesFlag()),
	}
	return infraGroup("cloud-pools", "cloud-pools <cloud-id>", "List server pools and reviewed budgets", a.listInfraPools, subcommands...)
}
func (a *App) listInfraPools(ctx cli.CommandContext) error {
	if err := infraRequired(ctx, 1); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	v, err := c.CloudPools(ctxOf(ctx), ctx.Arg(0))
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) saveInfraPool(ctx cli.CommandContext) error {
	if err := infraRequired(ctx, 1); err != nil {
		return err
	}
	var in client.PoolInput
	if err := a.infrastructureInput(ctx, &in); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	var v *client.Pool
	if ctx.Arg(1) == "" {
		v, err = c.CreateCloudPool(ctxOf(ctx), ctx.Arg(0), in)
	} else {
		v, err = c.UpdateCloudPool(ctxOf(ctx), ctx.Arg(0), ctx.Arg(1), in)
	}
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) moveInfraPool(ctx cli.CommandContext) error {
	if err := infraRequired(ctx, 1); err != nil {
		return err
	}
	var in client.MoveInput
	if err := a.infrastructureInput(ctx, &in); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	v, err := c.PreviewCloudMove(ctxOf(ctx), ctx.Arg(0), in)
	if err != nil {
		return err
	}
	if printErr := a.json(v); printErr != nil {
		return printErr
	}
	if !ctx.Bool("yes") {
		return nil
	}
	if in.Review == "" || in.Review != v.Review {
		return errors.New("move review differs; copy the reviewed digest to the input and review again")
	}
	// Resolve the exact environment before accepting so the next plan uses its name.
	environments, err := c.Environments(ctxOf(ctx), in.ProjectID)
	if err != nil {
		return err
	}
	environment := ""
	for _, env := range environments {
		if env.ID == in.EnvironmentID {
			environment = env.Name
			break
		}
	}
	if environment == "" {
		return errors.New("move environment is absent from the selected project")
	}
	localDeadline := time.Now().UTC().Add(15 * time.Minute)
	v, err = c.AcceptCloudMove(ctxOf(ctx), ctx.Arg(0), in)
	if err != nil {
		return err
	}
	return a.json(struct {
		Intent         *client.MoveReview `json:"intent"`
		Status         string             `json:"status"`
		Validity       string             `json:"validity"`
		LocalApproveBy time.Time          `json:"local_review_and_approve_by"`
		NextPreview    string             `json:"next_preview_command"`
		NextApproval   string             `json:"next_approval_command"`
	}{v, "intent_saved", "Server intent lasts 15 minutes from acceptance. This local deadline is conservative; no deployment has started.", localDeadline, "loomctl plan " + shellQuote(in.ProjectID) + " --env " + shellQuote(environment) + " --json", "loomctl approve <plan-id-returned-by-preview> --yes"})
}
func shellQuote(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'" }

func (a *App) cloudGrantsCommand() cli.Command {
	subcommands := []cli.Command{
		infraCommand("grant", "cloud-grants grant <cloud-id> <project-id> [--targets ids]", "Grant all or selected targets", a.grantInfraProject, cloudFlag("targets", "Comma-separated target IDs; empty grants the whole Cloud")),
		infraCommand("revoke", "cloud-grants revoke <cloud-id> <project-id>", "Revoke fresh placement while retaining cleanup authority", a.revokeInfraProject),
	}
	return infraGroup("cloud-grants", "cloud-grants <cloud-id>", "List organization Cloud project grants", a.listInfraGrants, subcommands...)
}
func (a *App) listInfraGrants(ctx cli.CommandContext) error {
	if err := infraRequired(ctx, 1); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	v, err := c.CloudProjectGrants(ctxOf(ctx), ctx.Arg(0))
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) grantInfraProject(ctx cli.CommandContext) error {
	if err := infraRequired(ctx, 2); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	v, err := c.GrantCloudProject(ctxOf(ctx), ctx.Arg(0), ctx.Arg(1), cloudTags(ctx.String("targets")))
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) revokeInfraProject(ctx cli.CommandContext) error {
	if err := infraRequired(ctx, 2); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	v, err := c.RevokeCloudProject(ctxOf(ctx), ctx.Arg(0), ctx.Arg(1))
	if err != nil {
		return err
	}
	return a.json(v)
}

func (a *App) dnsZonesCommand() cli.Command {
	subcommands := make([]cli.Command, 0, 5)
	for _, spec := range []struct {
		name, usage string
		handler     cli.CommandHandler
		flags       []cli.Flag
	}{
		{"project", "dns-zones project <project>", a.projectDNSZones, nil},
		{"create", "dns-zones create --file zone.json", a.createDNSZone, []cli.Flag{infraFileFlag()}},
		{"update", "dns-zones update <zone-id> --file zone-update.json", a.updateDNSZone, []cli.Flag{infraFileFlag()}},
		{"verify", "dns-zones verify <zone-id>", a.verifyDNSZone, nil},
		{"remove", "dns-zones remove <zone-id> --revision n --yes", a.removeDNSZone, []cli.Flag{cloudRevisionFlag(), infraYesFlag()}},
	} {
		subcommands = append(subcommands, infraCommand(spec.name, spec.usage, "Manage domain ownership and project access", spec.handler, spec.flags...))
	}
	return infraGroup("dns-zones", "dns-zones", "List organization DNS zones independently of compute", a.listDNSZones, subcommands...)
}
func (a *App) listDNSZones(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	v, err := c.DNSZones(ctxOf(ctx))
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) projectDNSZones(ctx cli.CommandContext) error {
	c, p, err := a.cloudClient(ctx, 1)
	if err != nil {
		return err
	}
	v, err := c.ProjectDNSZones(ctxOf(ctx), p.ID)
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) createDNSZone(ctx cli.CommandContext) error {
	var in client.ZoneInput
	if err := a.infrastructureInput(ctx, &in); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	v, err := c.CreateDNSZone(ctxOf(ctx), in)
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) updateDNSZone(ctx cli.CommandContext) error {
	if err := infraRequired(ctx, 1); err != nil {
		return err
	}
	var in client.ZoneUpdate
	if err := a.infrastructureInput(ctx, &in); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	v, err := c.UpdateDNSZone(ctxOf(ctx), ctx.Arg(0), in)
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) verifyDNSZone(ctx cli.CommandContext) error {
	if err := infraRequired(ctx, 1); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	v, err := c.VerifyDNSZone(ctxOf(ctx), ctx.Arg(0))
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) removeDNSZone(ctx cli.CommandContext) error {
	if err := infraRequired(ctx, 1); err != nil {
		return err
	}
	revision, err := cloudRevision(ctx)
	if err != nil {
		return err
	}
	if !ctx.Bool("yes") {
		return errors.New("pass --yes with the zone revision you reviewed")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	return c.RemoveDNSZone(ctxOf(ctx), ctx.Arg(0), client.ResourceRevisionInput{Revision: revision})
}

func (a *App) domainRoutesCommand() cli.Command {
	subcommands := make([]cli.Command, 0, 8)
	for _, spec := range []struct {
		name, usage string
		handler     cli.CommandHandler
		flags       []cli.Flag
	}{
		{"preview", "domain-routes preview <project> --file route.json", a.previewDomainRoute, []cli.Flag{infraFileFlag()}},
		{"apply", "domain-routes apply <project> <operation-id> --review digest --yes [--wait]", a.applyDomainRoute, append(recoveryFlags(), infraYesFlag(), cloudFlag("review", "Exact saved review digest"))},
		{"show", "domain-routes show <project> <operation-id> [--wait]", a.showDomainRoute, recoveryFlags()},
		{"retry", "domain-routes retry <project> <operation-id> [--wait]", a.retryDomainRoute, recoveryFlags()},
		{"verify", "domain-routes verify <project> <route-id> [--wait]", a.verifyDomainRoute, recoveryFlags()},
		{"remove", "domain-routes remove <project> <route-id> --revision n --yes [--wait]", a.removeDomainRoute, append(recoveryFlags(), infraYesFlag(), cloudRevisionFlag())},
		{"ttl", "domain-routes ttl <project> <route-id> --revision n --external-ttl-seconds n", a.updateDomainTTL, []cli.Flag{cloudRevisionFlag(), cloudFlag("external-ttl-seconds", "Maximum previously served external DNS TTL, 60 to 86400 seconds")}},
		{"history", "domain-routes history <project> <route-id>", a.domainRouteHistory, nil},
	} {
		subcommands = append(subcommands, infraCommand(spec.name, spec.usage, "Review or follow persisted route authority", spec.handler, spec.flags...))
	}
	return infraGroup("domain-routes", "domain-routes <project>", "List project routes with independent DNS, TLS and backend status", a.listDomainRoutes, subcommands...)
}
func (a *App) listDomainRoutes(ctx cli.CommandContext) error {
	c, p, err := a.cloudClient(ctx, 1)
	if err != nil {
		return err
	}
	v, err := c.DomainRoutes(ctxOf(ctx), p.ID)
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) previewDomainRoute(ctx cli.CommandContext) error {
	var in client.RouteInput
	if err := a.infrastructureInput(ctx, &in); err != nil {
		return err
	}
	c, p, err := a.cloudClient(ctx, 1)
	if err != nil {
		return err
	}
	v, err := c.PreviewDomainRoute(ctxOf(ctx), p.ID, in)
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) applyDomainRoute(ctx cli.CommandContext) error {
	c, p, err := a.cloudClient(ctx, 2)
	if err != nil {
		return err
	}
	v, err := c.DomainRouteOperation(ctxOf(ctx), p.ID, ctx.Arg(1))
	if err != nil {
		return err
	}
	if printErr := a.json(v); printErr != nil {
		return printErr
	}
	if !ctx.Bool("yes") {
		return nil
	}
	if ctx.String("review") == "" || ctx.String("review") != v.Review {
		return errors.New("route review differs; fetch the operation and review again")
	}
	v, err = c.ApplyDomainRoute(ctxOf(ctx), p.ID, v.ID, client.InfrastructureApplyInput{Review: v.Review})
	if err != nil {
		return err
	}
	return a.domainRouteResult(ctx, c, p.ID, v)
}
func (a *App) showDomainRoute(ctx cli.CommandContext) error { return a.domainRouteAction(ctx, "show") }
func (a *App) retryDomainRoute(ctx cli.CommandContext) error {
	return a.domainRouteAction(ctx, "retry")
}
func (a *App) verifyDomainRoute(ctx cli.CommandContext) error {
	return a.domainRouteAction(ctx, "verify")
}
func (a *App) domainRouteAction(ctx cli.CommandContext, action string) error {
	c, p, err := a.cloudClient(ctx, 2)
	if err != nil {
		return err
	}
	var v *client.RouteOperation
	switch action {
	case "retry":
		v, err = c.RetryDomainRoute(ctxOf(ctx), p.ID, ctx.Arg(1))
	case "verify":
		v, err = c.VerifyDomainRoute(ctxOf(ctx), p.ID, ctx.Arg(1))
	default:
		v, err = c.DomainRouteOperation(ctxOf(ctx), p.ID, ctx.Arg(1))
	}
	if err != nil {
		return err
	}
	return a.domainRouteResult(ctx, c, p.ID, v)
}
func (a *App) removeDomainRoute(ctx cli.CommandContext) error {
	revision, err := cloudRevision(ctx)
	if err != nil {
		return err
	}
	if !ctx.Bool("yes") {
		return errors.New("pass --yes with the route revision you reviewed")
	}
	c, p, err := a.cloudClient(ctx, 2)
	if err != nil {
		return err
	}
	v, err := c.RemoveDomainRoute(ctxOf(ctx), p.ID, ctx.Arg(1), client.ResourceRevisionInput{Revision: revision})
	if err != nil {
		return err
	}
	return a.domainRouteResult(ctx, c, p.ID, v)
}
func (a *App) updateDomainTTL(ctx cli.CommandContext) error {
	revision, err := cloudRevision(ctx)
	if err != nil {
		return err
	}
	ttl, err := strconv.Atoi(ctx.String("external-ttl-seconds"))
	if err != nil || ttl < 60 || ttl > 86400 {
		return errors.New("give --external-ttl-seconds from 60 to 86400 using the maximum previously served TTL")
	}
	c, p, err := a.cloudClient(ctx, 2)
	if err != nil {
		return err
	}
	v, err := c.UpdateDomainRouteTTL(ctxOf(ctx), p.ID, ctx.Arg(1), client.RouteTTLInput{Revision: revision, ExternalTTLSeconds: ttl})
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) domainRouteHistory(ctx cli.CommandContext) error {
	c, p, err := a.cloudClient(ctx, 2)
	if err != nil {
		return err
	}
	v, err := c.DomainRouteHistory(ctxOf(ctx), p.ID, ctx.Arg(1))
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) domainRouteResult(ctx cli.CommandContext, c *client.Client, project string, v *client.RouteOperation) error {
	if !ctx.Bool("wait") {
		return a.json(v)
	}
	bounded, cancel := context.WithTimeout(ctxOf(ctx), time.Duration(ctx.Duration("timeout")))
	defer cancel()
	latest, err := c.WaitDomainRouteOperation(bounded, project, v.ID)
	if latest != nil {
		v = latest
	}
	if err != nil {
		return errors.Join(err, a.json(v))
	}
	if printErr := a.json(v); printErr != nil {
		return printErr
	}
	if v.Status != "succeeded" {
		return fmt.Errorf("route operation %s is %s: %s", v.ID, v.Status, v.Message)
	}
	return nil
}

func (a *App) recoverInfraReservation(ctx cli.CommandContext) error {
	if err := infraRequired(ctx, 1); err != nil {
		return err
	}
	var in client.RecoveryInput
	if err := a.infrastructureInput(ctx, &in); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	v, err := c.PreviewCloudReservationRecovery(ctxOf(ctx), ctx.Arg(0), in)
	if err != nil {
		return err
	}
	if printErr := a.json(v); printErr != nil {
		return printErr
	}
	if !ctx.Bool("yes") {
		return nil
	}
	if in.Review == "" || in.Review != v.Review {
		return errors.New("reservation state changed; copy the reviewed digest to the input and review again")
	}
	v, err = c.RecoverCloudReservation(ctxOf(ctx), ctx.Arg(0), in)
	if err != nil {
		return err
	}
	return a.json(v)
}
