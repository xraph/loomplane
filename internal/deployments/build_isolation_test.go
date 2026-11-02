package deployments

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

type buildWorkerInventory struct {
	Credentials
	shared bool
	err    error
}

func (i buildWorkerInventory) BuildWorkerShared(context.Context, string, string) (bool, error) {
	return i.shared, i.err
}

func TestBuilderRejectsUnsafeWorkersBeforeDial(t *testing.T) {
	for _, mode := range []string{"unclassified", "shared", "new pool member", "inventory failure", "changed policy"} {
		t.Run(mode, func(t *testing.T) {
			job, builder, _, _ := imageBuildFixture(t, true)
			target := job.Action.Service.BuildTarget
			c, _, err := job.BuildCredentials.Credential(t.Context(), job.Deployment.OrgID, target.Builder)
			if err != nil {
				t.Fatal(err)
			}
			inventory := buildWorkerInventory{Credentials: job.BuildCredentials}
			switch mode {
			case "unclassified":
				delete(c.Details, "build_workers")
			case "shared", "changed policy":
				c.Details["build_workers"] = "shared"
			case "new pool member":
				inventory.shared = true
			case "inventory failure":
				inventory.err = errors.New("inventory unavailable")
			}
			if mode != "changed policy" {
				target.BuilderSettings = planning.ReviewedBuildSettings(connections.Server, c.Details)
			}
			job.BuildCredentials = inventory
			builder.Dial = func(context.Context, *server.Config) (*server.Client, error) {
				t.Fatal("unsafe build reached the host")
				return nil, nil
			}
			if _, _, _, err := builder.builderSession(t.Context(), job); err == nil {
				t.Fatal("unsafe builder accepted")
			}
		})
	}
}
