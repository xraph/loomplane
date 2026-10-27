package fabriqloom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
)

// revokeTimeout bounds the revocations one release triggers.
const revokeTimeout = 10 * time.Second

// Observer revokes a service's retiring Fabriq keys once a release has
// written its binding: from then on the service runs with the key the
// release gave it, so the one it replaced can go.
type Observer struct{ h *Hooks }

var _ deployments.ReleaseObserver = (*Observer)(nil)

// NewObserver returns the fabriq kind's release observer.
func NewObserver(d HookDeps) *Observer { return &Observer{h: NewHooks(d)} }

// Released implements deployments.ReleaseObserver. What fails is logged
// and returned, which warns on the step; the keys stay retiring, and the
// service's next release tries again. A deleted service's keys go through
// ReleaseConsumer instead.
func (o *Observer) Released(ctx context.Context, r deployments.Release) error {
	b := r.Binding
	if r.Removed || b == nil || b.Subject != planning.ServiceSubject {
		return nil
	}
	var cfg planning.ServiceConfig
	if err := json.Unmarshal(b.Config, &cfg); err != nil {
		return nil // not a service binding this observer can read
	}
	var resources []string
	for _, link := range cfg.Env {
		if link.Subject == planning.ResourceSubject && strings.HasPrefix(link.Field, "consumer:") && !slices.Contains(resources, link.From) {
			resources = append(resources, link.From)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, revokeTimeout)
	defer cancel()
	var errs []error
	for _, resource := range resources {
		if err := o.retire(ctx, r, resource); err != nil {
			err = fmt.Errorf("revoke %s's replaced Fabriq keys on %s: %w", b.Name, resource, err)
			o.h.d.warn("fabriq key revocation", err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// retire revokes the service's retiring keys on one resource.
func (o *Observer) retire(ctx context.Context, r deployments.Release, resource string) error {
	service := r.Binding.Name
	unlock, err := o.h.d.Keys.LockService(ctx, r.OrgID, r.EnvironmentID, resource, service)
	if err != nil {
		return err
	}
	defer unlock()
	records, err := o.h.d.Keys.ForService(ctx, r.OrgID, r.EnvironmentID, resource, service)
	if err != nil {
		return err
	}
	var retiring []*KeyRecord
	for _, k := range records {
		if k.State == KeyRetiring {
			retiring = append(retiring, k)
		}
	}
	if len(retiring) == 0 {
		return nil
	}
	ref := deployments.ComponentRef{ResourceRef: deployments.ResourceRef{ProjectID: r.ProjectID, EnvironmentID: r.EnvironmentID, Resource: resource}, Component: consoleComponent, Port: consolePort}
	ch, err := o.h.d.Access.InternalChannel(ctx, r.OrgID, ref)
	if err != nil {
		return fmt.Errorf("reach Fabriq's console: %w", err)
	}
	defer func() { _ = ch.Close() }() //nolint:errcheck // nothing to do about it
	admin := &AdminClient{Channel: ch}
	var errs []error
	for _, k := range retiring {
		errs = append(errs, o.h.revoke(ctx, admin, r.OrgID, r.EnvironmentID, resource, k))
	}
	return errors.Join(errs...)
}
