package ctl

import (
	"errors"
	"fmt"

	"github.com/xraph/forge/cli"
)

func (a *App) storageCommand() cli.Command {
	return cli.NewCommand("storage", "Inspect persistent volumes, including retained data", a.storage, cli.WithUsage("storage <connection-id> [--json]"), cli.WithFlag(jsonFlag))
}

func (a *App) storage(ctx cli.CommandContext) error {
	if len(ctx.Args()) != 1 {
		return errors.New("provide one Docker or Kubernetes connection ID")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	volumes, err := c.StorageVolumes(ctxOf(ctx), ctx.Arg(0))
	if err != nil {
		return err
	}
	if ctx.Bool("json") {
		return a.json(volumes)
	}
	rows := make([][]string, 0, len(volumes))
	for _, volume := range volumes {
		capacity := "Shared host capacity"
		if volume.CapacityBytes > 0 {
			capacity = fmt.Sprintf("%d MiB", volume.CapacityBytes/(1<<20))
		} else if volume.Provider == "kubernetes" {
			capacity = "Not reported"
		}
		rows = append(rows, []string{volume.Name, volume.Status, volume.ProjectID, volume.EnvironmentID, volume.Role, capacity})
	}
	a.table([]string{"VOLUME", "STATUS", "PROJECT", "ENVIRONMENT", "ROLE", "CAPACITY"}, rows)
	return nil
}
