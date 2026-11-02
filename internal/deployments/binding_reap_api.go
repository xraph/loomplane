package deployments

import (
	"context"
	"fmt"
	"slices"
)

func (br *BindingReaper) reapResourceAccess(ctx context.Context, env reapedEnvironment, name string) {
	inventory := nativeOutputInventory{store: br.Secrets, org: env.org, environment: env.id, name: name}
	warn := func(err error) {
		br.warn(fmt.Sprintf("delete the credentials of %s, whose environment %s was deleted", name, env.name), err)
	}
	fields, err := inventory.fields(ctx)
	if err != nil {
		warn(err)
		return
	}
	fields = append(fields, resourceCredentialFields...)
	slices.Sort(fields)
	failed := false
	for _, field := range slices.Compact(fields) {
		if deleteErr := br.Secrets.Delete(ctx, inventory.key(field)); deleteErr != nil {
			failed = true
			warn(deleteErr)
		}
	}
	if !failed {
		if deleteErr := br.Secrets.Delete(ctx, inventory.key(apiOutputFields)); deleteErr != nil {
			warn(deleteErr)
		}
	}
}
