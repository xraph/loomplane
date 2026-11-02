package owned

import (
	"context"
	"maps"

	"github.com/xraph/ctrlplane/id"
)

type runtimeIdentityKey struct{}
type runtimePreflightKey struct{}

// RuntimeIdentity is supplied by the runtime after resolving its actual target.
// It is never recovered from caller-supplied labels.
type RuntimeIdentity struct {
	Instance            id.ID
	Provider, Namespace string
}

// WithRuntimeIdentity binds private file preparation to the actual instance.
func WithRuntimeIdentity(ctx context.Context, identity RuntimeIdentity) context.Context {
	return context.WithValue(ctx, runtimeIdentityKey{}, identity)
}

// RuntimeIdentityFrom reads the runtime's trusted file-preparation identity.
func RuntimeIdentityFrom(ctx context.Context) (RuntimeIdentity, bool) {
	value, ok := ctx.Value(runtimeIdentityKey{}).(RuntimeIdentity)
	return value, ok
}

// RuntimePreflight permits validation before an instance exists. No deferred
// certificate may be emitted during preflight.
func RuntimePreflight(ctx context.Context) context.Context {
	return context.WithValue(ctx, runtimePreflightKey{}, true)
}

// IsRuntimePreflight reports the explicit validation-only phase.
func IsRuntimePreflight(ctx context.Context) bool {
	value, ok := ctx.Value(runtimePreflightKey{}).(bool)
	return ok && value
}

// InstanceLabels overrides a caller's instance claim for the values callback.
func InstanceLabels(labels map[string]string, iid id.ID) map[string]string {
	out := maps.Clone(labels)
	if out == nil {
		out = map[string]string{}
	}
	out[LabelInstance] = iid.String()
	return out
}
