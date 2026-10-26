package orchestration

import "context"

type finalizationKey struct{}

type finalizationIdentity struct{ key, workflow string }

// FinalizationKey identifies a durable finish activity across worker restarts.
func FinalizationKey(ctx context.Context) string {
	identity, ok := ctx.Value(finalizationKey{}).(finalizationIdentity)
	if !ok {
		return ""
	}
	return identity.key
}

// FinalizationWorkflow identifies the workflow that owns the finish activity.
func FinalizationWorkflow(ctx context.Context) string {
	identity, ok := ctx.Value(finalizationKey{}).(finalizationIdentity)
	if !ok {
		return ""
	}
	return identity.workflow
}
