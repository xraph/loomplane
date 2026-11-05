package currentauthority

import (
	"context"

	"github.com/xraph/warden"
)

// NewFreshCache keeps ordinary caching and invalidation while current checks
// always miss and never populate cached results. It does not enable authority.
func NewFreshCache(owner *Coordinator, ordinary warden.Cache) warden.Cache {
	return &freshCache{owner: owner, ordinary: ordinary}
}

type freshCache struct {
	owner    *Coordinator
	ordinary warden.Cache
}

var _ warden.Cache = (*freshCache)(nil)

func (c *freshCache) Get(ctx context.Context, tenantID, namespacePath string, req *warden.CheckRequest) (*warden.CheckResult, bool) {
	if _, scoped, err := c.owner.readerScope(ctx); scoped || err != nil {
		return nil, false
	}
	return c.ordinary.Get(ctx, tenantID, namespacePath, req)
}
func (c *freshCache) Set(ctx context.Context, tenantID, namespacePath string, req *warden.CheckRequest, result *warden.CheckResult) {
	if _, scoped, err := c.owner.readerScope(ctx); scoped || err != nil {
		return
	}
	c.ordinary.Set(ctx, tenantID, namespacePath, req, result)
}
func (c *freshCache) InvalidateTenant(ctx context.Context, tenantID string) {
	c.ordinary.InvalidateTenant(ctx, tenantID)
}
func (c *freshCache) InvalidateSubject(ctx context.Context, tenantID string, kind warden.SubjectKind, subjectID string) {
	c.ordinary.InvalidateSubject(ctx, tenantID, kind, subjectID)
}
func (c *freshCache) Clear(ctx context.Context) { c.ordinary.Clear(ctx) }
