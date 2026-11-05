package currentauthority

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
)

// Evaluate runs the fresh checks after all domain guards have finished waiting.
// The installed adapter supplies the canonical identity check. The interval
// guard rejects any persisted authority expiry or activation inside the check.
func (a *Attempt) Evaluate(ctx context.Context, check func(context.Context) error) (*Decision, error) {
	if a.profile != nil && a.profile.ordinary != nil {
		return nil, a.fail(ErrDecision)
	}
	return a.evaluate(ctx, check, nil)
}

func (a *Attempt) evaluate(ctx context.Context, check func(context.Context) error, proof *ordinaryProof) (*Decision, error) {
	c := a.owner
	c.mu.Lock()
	err := a.validLocked(ctx)
	if err == nil && !a.fenced {
		err = ErrUnavailable
		a.poison = errors.Join(a.poison, err)
	}
	if err == nil {
		err = a.profile.verify()
		if err != nil {
			a.poison = errors.Join(a.poison, err)
		}
	}
	if err == nil && (a.evaluation || a.staging || a.decision != nil || a.pending != 0 || check == nil) {
		err = ErrDecision
		a.poison = errors.Join(a.poison, err)
	}
	if err != nil {
		c.mu.Unlock()
		return nil, err
	}
	a.evaluation = true
	c.mu.Unlock()
	start := c.clock()
	err = check(ctx)
	at := c.clock()
	if err == nil && clockChanged(start, at) {
		err = ErrTemporal
	}
	if err == nil && proof != nil {
		err = proof.facts.validate(a.binding, proof.requireEmail, at)
	}
	if err == nil {
		var crossed bool
		err = a.handle.QueryRow(ctx, boundarySQL(c.schema), start, at).Scan(&crossed)
		if err == nil && crossed {
			err = ErrTemporal
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	a.evaluation = false
	if err != nil {
		a.poison = errors.Join(a.poison, err)
	}
	if scopeErr := a.validLocked(ctx); scopeErr != nil {
		return nil, scopeErr
	}
	if a.pending != 0 {
		a.poison = errors.Join(a.poison, ErrPending)
		return nil, ErrPending
	}
	d := &Decision{attempt: a, binding: a.binding, at: at}
	if proof != nil {
		copied := *proof
		d.kind = ordinaryDecision
		d.ordinary = &copied
	}
	a.decision = d
	return d, nil
}

// Stage consumes a decision and records one domain operation in this transaction.
// The adapter must read and compare the durable binding before calling Stage,
// revalidate target/user/key time predicates at at, and persist at with the row.
// The callback must return every validation error and finish its SQL before exit.
func (a *Attempt) Stage(ctx context.Context, d *Decision, durable Binding, write func(context.Context, driver.Tx, time.Time) error) error {
	bound, err := canonical(durable)
	if err != nil {
		return a.fail(err)
	}
	c := a.owner
	c.mu.Lock()
	err = a.validLocked(ctx)
	if err == nil && bound != a.binding {
		err = ErrBinding
	}
	if err == nil && (d == nil || d != a.decision || d.attempt != a || d.binding != a.binding || d.used || a.staged || a.staging || a.evaluation || a.pending != 0 || write == nil) {
		err = ErrDecision
	}
	if err != nil {
		if a.phase == evaluating {
			a.poison = errors.Join(a.poison, err)
		}
		c.mu.Unlock()
		return err
	}
	if err = d.validateOrdinary(a); err != nil {
		a.poison = errors.Join(a.poison, err)
		c.mu.Unlock()
		return err
	}
	d.used = true
	a.staging = true
	c.mu.Unlock()
	err = write(ctx, a.handle, d.at)
	c.mu.Lock()
	defer c.mu.Unlock()
	a.staging = false
	if err != nil {
		a.poison = errors.Join(a.poison, err)
	}
	if scopeErr := a.validLocked(ctx); scopeErr != nil {
		return scopeErr
	}
	if a.pending != 0 {
		a.poison = errors.Join(a.poison, ErrPending)
		return ErrPending
	}
	a.staged = true
	return nil
}

func (c *Coordinator) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func clockChanged(start, end time.Time) bool {
	elapsed := end.Sub(start)
	wall := end.Round(0).Sub(start.Round(0))
	delta := wall - elapsed
	// Smaller steps cannot be distinguished from normal clock adjustment. The
	// supported profile still requires stable application wall time.
	return elapsed < 0 || wall < 0 || delta > time.Millisecond || delta < -time.Millisecond
}

var authorityTables = [...]string{
	"authsome_members", "authsome_organizations", "authsome_users",
	"keysmith_key_scopes", "keysmith_keys", "keysmith_scopes",
	"loomplane_projects", "loomplane_team_grants", "loomplane_team_members", "loomplane_teams",
	"warden_assignments", "warden_permissions", "warden_policies",
	"warden_relations", "warden_resource_types", "warden_role_permissions", "warden_roles",
}

func fenceSQL(schema string) string {
	sql := "LOCK TABLE "
	for i, table := range authorityTables {
		if i > 0 {
			sql += ", "
		}
		sql += pgx.Identifier{schema, table}.Sanitize()
	}
	return sql + " IN SHARE MODE"
}

func boundarySQL(schema string) string {
	table := func(name string) string { return pgx.Identifier{schema, name}.Sanitize() }
	return `SELECT EXISTS (SELECT 1 FROM (
 SELECT expires_at AS boundary_at FROM ` + table("warden_assignments") + `
 UNION ALL SELECT not_before AS boundary_at FROM ` + table("warden_policies") + ` WHERE is_active
 UNION ALL SELECT not_after AS boundary_at FROM ` + table("warden_policies") + ` WHERE is_active
 UNION ALL SELECT expires_at AS boundary_at FROM ` + table("keysmith_keys") + `
 UNION ALL SELECT ban_expires AS boundary_at FROM ` + table("authsome_users") + ` WHERE banned
 ) boundaries WHERE boundary_at IS NOT NULL AND boundary_at >= $1 AND boundary_at <= $2)`
}
