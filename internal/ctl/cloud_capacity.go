package ctl

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/pkg/client"
	"github.com/xraph/loomplane/pkg/provider"
)

var humanFlag = cli.NewBoolFlag("human", "", "Print one line per dimension instead of JSON", false)

// rolloutOffText matches the dashboard's and the server's sentence for a
// deployment that needs a server while automatic expansion is off.
const rolloutOffText = "Automatic expansion is off on this Loomplane server, so no server will be added. Add or adopt one yourself."

const staleCapacity = "this changed since you opened it; fetch the current version and try again"

func (a *App) cloudCapacityCommand() cli.Command {
	subcommands := []cli.Command{
		infraCommand("get", "cloud-capacity get <cloud-id>", "Show a Cloud's capacity policy", a.getCapacityPolicy),
		infraCommand("set", "cloud-capacity set <cloud-id> --file policy.json", "Save a capacity policy at the revision you read; the file holds expected_revision and policy", a.setCapacityPolicy, infraFileFlag()),
		infraCommand("usage", "cloud-capacity usage <cloud-id> [--human]", "Show what a Cloud counts against its limits", a.capacityUsage(false), humanFlag),
		infraCommand("refresh", "cloud-capacity refresh <cloud-id> [--human]", "Read the providers again, then show usage", a.capacityUsage(true), humanFlag),
		infraCommand("expansions", "cloud-capacity expansions <cloud-id> [--human]", "List the servers a Cloud's pools are growing by", a.capacityExpansions, humanFlag),
		infraCommand("approve", "cloud-capacity approve <cloud-id> <expansion-id> --revision <n> [--yes]", "Approve a new server at the revision you read", a.approveExpansion, cloudRevisionFlag(), infraYesFlag()),
		infraCommand("create-pool", "cloud-capacity create-pool <cloud-id> --file scaling-pool.json", "Create an empty pool and the policy that grows it in one step", a.createScalingPool, infraFileFlag()),
	}
	return infraGroup("cloud-capacity", "cloud-capacity <get|set|usage|refresh|expansions|approve|create-pool> <cloud-id>", "Manage a Cloud's automatic capacity", a.getCapacityPolicy, subcommands...)
}

func (a *App) getCapacityPolicy(ctx cli.CommandContext) error {
	if err := infraRequired(ctx, 1); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	v, err := c.CapacityPolicy(ctxOf(ctx), ctx.Arg(0))
	if err != nil {
		return err
	}
	return a.json(v)
}

func (a *App) setCapacityPolicy(ctx cli.CommandContext) error {
	if err := infraRequired(ctx, 1); err != nil {
		return err
	}
	var in struct {
		ExpectedRevision *int64                `json:"expected_revision"`
		Policy           client.CapacityPolicy `json:"policy"`
	}
	if err := a.infrastructureInput(ctx, &in); err != nil {
		return err
	}
	if in.ExpectedRevision == nil {
		return errors.New("the file needs expected_revision: the revision of the policy you read, 0 for a Cloud never configured")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	v, err := c.SaveCapacityPolicy(ctxOf(ctx), ctx.Arg(0), client.CapacityPolicyInput{ExpectedRevision: *in.ExpectedRevision, Policy: in.Policy})
	if err != nil {
		return capacityErr(err)
	}
	return a.json(v)
}

func (a *App) createScalingPool(ctx cli.CommandContext) error {
	if err := infraRequired(ctx, 1); err != nil {
		return err
	}
	var in client.ScalingPoolInput
	if err := a.infrastructureInput(ctx, &in); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	v, err := c.CreateScalingPool(ctxOf(ctx), ctx.Arg(0), in)
	if err != nil {
		return capacityErr(err)
	}
	return a.json(v)
}

func (a *App) capacityUsage(refresh bool) cli.CommandHandler {
	return func(ctx cli.CommandContext) error {
		if err := infraRequired(ctx, 1); err != nil {
			return err
		}
		c, err := a.client()
		if err != nil {
			return err
		}
		var v *client.CapacityUsage
		if refresh {
			v, err = c.RefreshCapacity(ctxOf(ctx), ctx.Arg(0))
		} else {
			v, err = c.CapacityUsage(ctxOf(ctx), ctx.Arg(0))
		}
		if err != nil {
			return err
		}
		if !ctx.Bool("human") {
			return a.json(v)
		}
		a.printUsage(v)
		return nil
	}
}

func (a *App) printUsage(v *client.CapacityUsage) {
	line := func(label string, p, pend, res, head *int64, unit func(*int64) string) {
		a.printf("%s: provisioned %s, pending %s, reserved %s, headroom %s\n", label, unit(p), unit(pend), unit(res), unit(head))
	}
	n := countOrUnknown
	line("Servers", v.Provisioned.Servers, v.Pending.Servers, v.Reserved.Servers, v.Headroom.Servers, n)
	line("CPU", v.Provisioned.CPUMillis, v.Pending.CPUMillis, v.Reserved.CPUMillis, v.Headroom.CPUMillis, func(p *int64) string {
		if p == nil {
			return "unknown"
		}
		return coresText(*p)
	})
	bytesText := func(p *int64) string {
		if p == nil {
			return "unknown"
		}
		return gibText(*p)
	}
	line("Memory", v.Provisioned.MemoryBytes, v.Pending.MemoryBytes, v.Reserved.MemoryBytes, v.Headroom.MemoryBytes, bytesText)
	line("Storage", v.Provisioned.StorageBytes, v.Pending.StorageBytes, v.Reserved.StorageBytes, v.Headroom.StorageBytes, bytesText)
	for _, g := range v.GPUs {
		a.printf("%s GPUs: provisioned %s, reserved %d, limit %s\n", g.Kind, countOrUnknown(g.Provisioned), g.Reserved, countOrUnknown(g.Limit))
	}
	for _, f := range v.Frontends {
		a.printf("Frontends on %s: %d counted, limit %s\n", f.TargetID, f.Counted, limitOrNone(f.Maximum))
	}
	if !v.Fresh {
		a.printf("These numbers are out of date. Run `loomctl cloud-capacity refresh` to read the providers again.\n")
	}
}

func coresText(millis int64) string {
	return strconv.FormatFloat(float64(millis)/1000, 'f', -1, 64) + " cores"
}

func gibText(bytes int64) string {
	return strconv.FormatFloat(float64(bytes)/(1<<30), 'f', 1, 64) + " GiB"
}

// amountText is an amount of a block's resource in the units printUsage uses.
func amountText(resource string, n int64) string {
	switch {
	case resource == "cpu_millis":
		return coresText(n)
	case strings.HasSuffix(resource, "_bytes"):
		return gibText(n)
	}
	return strconv.FormatInt(n, 10)
}

func countOrUnknown(p *int64) string {
	if p == nil {
		return "unknown"
	}
	return strconv.FormatInt(*p, 10)
}

func limitOrNone(p *int64) string {
	if p == nil {
		return "none"
	}
	return strconv.FormatInt(*p, 10)
}

func (a *App) capacityExpansions(ctx cli.CommandContext) error {
	if err := infraRequired(ctx, 1); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	list, err := c.Expansions(ctxOf(ctx), ctx.Arg(0))
	if err != nil {
		return err
	}
	if !ctx.Bool("human") {
		return a.json(client.ExpansionList{Expansions: list})
	}
	if len(list) == 0 {
		a.printf("No servers are being added.\n")
	}
	for _, e := range list {
		a.printf("%s\n", expansionLine(e))
	}
	return nil
}

func expansionLine(e client.Expansion) string {
	return strings.Join([]string{e.ID, e.Status, e.PoolID, e.Size, expansionGPUs(e), expansionPrice(e)}, " ")
}

func expansionGPUs(e client.Expansion) string {
	if e.Resources.GPUs > 0 {
		return fmt.Sprintf("%dx%s", e.Resources.GPUs, e.Resources.GPUKind)
	}
	if e.Accelerator != nil && e.Accelerator.Count > 0 {
		return fmt.Sprintf("%dx%s", e.Accelerator.Count, e.Accelerator.Kind)
	}
	return "no-gpu"
}

// expansionPrice never reads an unknown price as free.
func expansionPrice(e client.Expansion) string {
	if !e.HourlyPriceKnown {
		return "price unknown"
	}
	return fmt.Sprintf("$%.2f/hr", e.EstimatedHourlyUSD)
}

func pauseRevisionFlag() cli.Flag {
	return cloudFlag("revision", "The revision the pause showed")
}

func revisionOf(ctx cli.CommandContext) (int64, error) {
	s := ctx.String("revision")
	if s == "" {
		return 0, errors.New("give --revision: the revision you read")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("--revision is a whole number, as the command showed it")
	}
	return n, nil
}

func (a *App) approveExpansion(ctx cli.CommandContext) error {
	if err := infraRequired(ctx, 2); err != nil {
		return err
	}
	rev, err := revisionOf(ctx)
	if err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	if !ctx.Bool("yes") {
		list, lerr := c.Expansions(ctxOf(ctx), ctx.Arg(0))
		if lerr != nil {
			return lerr
		}
		var found *client.Expansion
		for i := range list {
			if list[i].ID == ctx.Arg(1) {
				found = &list[i]
			}
		}
		if found == nil {
			return fmt.Errorf("expansion %s isn't in this Cloud's list", ctx.Arg(1))
		}
		a.printf("%s\n", expansionLine(*found))
		if found.Block != nil {
			a.printf("  %s\n", blockLine(*found.Block))
		}
		ok, cerr := ctx.Confirm("Approve this server?")
		if cerr != nil {
			return fmt.Errorf("pass --yes to approve without asking: %w", cerr)
		}
		if !ok {
			a.printf("Nothing approved.\n")
			return nil
		}
	}
	v, err := c.ApproveExpansion(ctxOf(ctx), ctx.Arg(0), ctx.Arg(1), rev)
	if err != nil {
		return capacityErr(err)
	}
	if ctx.Bool("json") {
		return a.json(v)
	}
	a.printf("Approved %s. %s\n", v.ID, expansionLine(*v))
	return nil
}

// capacityErr keeps the server's reason for a refusal. A 409 with no message
// is the stale revision case, so it gets the generic sentence.
func capacityErr(err error) error {
	var apiErr *client.Error
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict && apiErr.Message == "" {
		return errors.New(staleCapacity)
	}
	return err
}

func (a *App) resume(ctx cli.CommandContext) error { return a.decideWait(ctx, true) }
func (a *App) cancel(ctx cli.CommandContext) error { return a.decideWait(ctx, false) }

func (a *App) decideWait(ctx cli.CommandContext, resume bool) error {
	if ctx.Arg(0) == "" {
		return errors.New("give the deployment ID")
	}
	rev, err := revisionOf(ctx)
	if err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	var d *client.Deployment
	if resume {
		d, err = c.ResumeCapacity(ctxOf(ctx), ctx.Arg(0), rev)
	} else {
		d, err = c.CancelCapacityWait(ctxOf(ctx), ctx.Arg(0), rev)
	}
	if err != nil {
		return capacityErr(err)
	}
	if ctx.Bool("json") {
		return a.json(d)
	}
	if resume {
		a.printf("Resuming %s.\n", d.ID)
	} else {
		a.printf("Cancelled %s.\n", d.ID)
	}
	a.printWait(d)
	return nil
}

// printWait lists why a paused deployment waits and how to resume it.
func (a *App) printWait(d *client.Deployment) {
	if !d.Paused() || d.CapacityWait == nil {
		return
	}
	for _, b := range d.CapacityWait.Blocks {
		a.printf("  %s\n", blockLine(b))
	}
	a.printf("Paused for capacity. Resume with: loomctl resume %s --revision %d\n", d.ID, d.CapacityWait.Revision)
}

func blockLabel(resource string) string {
	switch {
	case resource == "servers":
		return "Servers"
	case resource == "cpu_millis":
		return "CPU"
	case resource == "memory_bytes":
		return "Memory"
	case resource == "storage_bytes":
		return "Storage"
	case resource == "frontends":
		return "Frontends"
	case strings.HasPrefix(resource, "gpus:"):
		kind := strings.TrimPrefix(resource, "gpus:")
		if k, ok := provider.LookupAcceleratorKind(kind); ok {
			kind = k.Title
		}
		return kind + " GPUs"
	}
	w := strings.ReplaceAll(resource, "_", " ")
	if w == "" {
		return w
	}
	return strings.ToUpper(w[:1]) + w[1:]
}

// blockLine says in a sentence why capacity wasn't granted.
func blockLine(b client.CapacityBlock) string {
	label := blockLabel(b.Resource)
	switch b.Reason {
	case "hard_limit":
		// The server's sentence, in the units printUsage uses.
		scope := "this Cloud's"
		if b.PoolID != "" {
			scope = "this pool's"
		}
		return fmt.Sprintf("%s is at %s limit: %s of %s counted, %s more needed.", label, scope, amountText(b.Resource, b.Counted), amountText(b.Resource, b.Limit), amountText(b.Resource, b.Requested))
	case "expansion_approval":
		return "Waiting for a Cloud manager to approve a new server."
	case "provisioning":
		if b.Resource == "gpu_runtime" {
			return "Waiting for the new GPU server's NVIDIA runtime."
		}
		return "A new server is being created."
	case "stale_inventory":
		return fmt.Sprintf("Loomplane can't confirm current %s usage, so it won't add capacity until a refresh succeeds.", label)
	case "provider_quota":
		return "The provider account's quota is full."
	case "provider_failure":
		return "The provider couldn't create the server. Loomplane will retry."
	case "placement_constraint":
		return "No allowed server size fits this workload."
	case "review_required":
		if b.Resource == "rollout" {
			return rolloutOffText
		}
		return "This deployment's plan changed. Review it again."
	}
	return strings.ReplaceAll(b.Reason, "_", " ")
}
