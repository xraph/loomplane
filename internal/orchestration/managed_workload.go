package orchestration

import (
	"context"

	drt "github.com/xraph/dispatch/durable/runtime"
)

const ManagedWorkloadBuild = "deployment-v5"
const ManagedWorkloadProtocol = "managed-workload-journal-v1"

type managedWorkloadContextKey struct{}

// ManagedWorkloadContext identifies activities routed to the journal workflow.
func ManagedWorkloadContext(ctx context.Context) context.Context {
	return context.WithValue(CapacityContext(ctx), managedWorkloadContextKey{}, true)
}
func IsManagedWorkloadContext(ctx context.Context) bool {
	value, ok := ctx.Value(managedWorkloadContextKey{}).(bool)
	return ok && value
}

// ManagedWorkloadOptions preserves previous build registrations and replay histories.
func (d *Deployments) ManagedWorkloadOptions(owner string, concurrency int) drt.Options {
	options := d.CanaryOptions(owner, concurrency)
	options.BuildID = ManagedWorkloadBuild
	// Cloud sync retains its existing workflow while using the journal activities.
	options.Workflows[cloudSyncWorkflow] = d.syncV3
	for name, activity := range options.Activities {
		options.Activities[name] = func(ctx context.Context, info drt.ActivityInfo, raw []byte) ([]byte, error) {
			return activity(ManagedWorkloadContext(ctx), info, raw)
		}
	}
	return options
}
