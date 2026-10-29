package ctl

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/pkg/client"
)

func (a *App) cloudClustersCommand() cli.Command {
	commands := make([]cli.Command, 0, 10)
	for _, name := range []string{"list", "show", "options", "discover", "preview", "operations", "operation", "apply", "retry", "cancel"} {
		flags := append(recoveryFlags(), infraFileFlag(), infraYesFlag(), cloudFlag("review", "Exact saved review digest"), cloudFlag("region", "Provider region"), cloudFlag("cursor", "Next page cursor"))
		commands = append(commands, infraCommand(name, "cloud-clusters "+name+" <cloud-id> [resource-id]", "Manage reviewed cluster setup", func(ctx cli.CommandContext) error { return a.clusterCommand(ctx, name) }, flags...))
	}
	return infraGroup("cloud-clusters", "cloud-clusters <cloud-id>", "Manage organization Kubernetes clusters", func(ctx cli.CommandContext) error { return a.clusterCommand(ctx, "list") }, commands...)
}
func (a *App) clusterCommand(ctx cli.CommandContext, action string) error {
	count := 2
	if action == "list" || action == "preview" || action == "operations" || action == "discover" {
		count = 1
	}
	if err := infraRequired(ctx, count); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	cloud, id := ctx.Arg(0), ctx.Arg(1)
	var out any
	var operation *client.CloudClusterOperation
	switch action {
	case "list":
		out, err = c.CloudClusters(ctxOf(ctx), cloud, ctx.String("cursor"))
	case "show":
		out, err = c.CloudCluster(ctxOf(ctx), cloud, id)
	case "options":
		out, err = c.CloudClusterOptions(ctxOf(ctx), cloud, id, ctx.String("region"))
	case "discover":
		out, err = c.DiscoverClusters(ctxOf(ctx), cloud, ctx.String("cursor"))
	case "operations":
		out, err = c.CloudClusterOperations(ctxOf(ctx), cloud, ctx.String("cursor"))
	case "preview":
		var in client.ClusterInput
		if readErr := a.infrastructureInput(ctx, &in); readErr != nil {
			return readErr
		}
		operation, err = c.PreviewCloudCluster(ctxOf(ctx), cloud, in)
	case "operation":
		operation, err = c.CloudClusterOperation(ctxOf(ctx), cloud, id)
	case "apply":
		if !ctx.Bool("yes") || ctx.String("review") == "" {
			return errors.New("give --yes and --review with the exact saved digest")
		}
		operation, err = c.ApplyCloudCluster(ctxOf(ctx), cloud, id, ctx.String("review"))
	case "retry":
		operation, err = c.RetryCloudCluster(ctxOf(ctx), cloud, id)
	case "cancel":
		operation, err = c.CancelCloudCluster(ctxOf(ctx), cloud, id)
	}
	if err != nil {
		return err
	}
	if operation == nil {
		return a.json(out)
	}
	if !ctx.Bool("wait") {
		return a.json(operation)
	}
	bounded, cancel := context.WithTimeout(ctxOf(ctx), time.Duration(ctx.Duration("timeout")))
	defer cancel()
	latest, waitErr := c.WaitCloudClusterOperation(bounded, cloud, operation.ID)
	if latest != nil {
		operation = latest
	}
	printErr := a.json(operation)
	if waitErr != nil {
		return errors.Join(waitErr, printErr)
	}
	if printErr != nil {
		return printErr
	}
	if operation.Status != "succeeded" {
		return fmt.Errorf("cluster operation %s is %s", operation.ID, operation.Status)
	}
	return nil
}
