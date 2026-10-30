package orchestration

import (
	"context"

	drt "github.com/xraph/dispatch/durable/runtime"
)

const ManagedCleanupBuild = "deployment-v6"
const ManagedCleanupProtocol = "managed-cleanup-journal-v1"

type managedCleanupContextKey struct{}

func ManagedCleanupContext(ctx context.Context) context.Context {
	return context.WithValue(ManagedWorkloadContext(ctx), managedCleanupContextKey{}, true)
}

func IsManagedCleanupContext(ctx context.Context) bool {
	value, ok := ctx.Value(managedCleanupContextKey{}).(bool)
	return ok && value
}

// ManagedCleanupOptions keeps prior workflow registrations available for replay.
func (d *Deployments) ManagedCleanupOptions(owner string, concurrency int) drt.Options {
	options := d.ManagedWorkloadOptions(owner, concurrency)
	options.BuildID = ManagedCleanupBuild
	options.Workflows[managedFailedCleanupWorkflow] = d.managedCleanupWorkflow
	options.Activities[managedFailedCleanupActivity] = d.managedCleanupActivity
	for name, activity := range options.Activities {
		options.Activities[name] = func(ctx context.Context, info drt.ActivityInfo, raw []byte) ([]byte, error) {
			return activity(ManagedCleanupContext(ctx), info, raw)
		}
	}
	return options
}
