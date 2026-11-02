//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver/pgmigrate"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
)

func rollbackGroupSnapshot(ctx context.Context, f *groupSnapshotFixture) error {
	_, err := migrate.NewOrchestrator(pgmigrate.New(f.x.f.db), migrations.Group()).Rollback(ctx)
	return err
}
func snapshotMigrationState(ctx context.Context, t *testing.T, f *groupSnapshotFixture) string {
	t.Helper()
	var raw string
	err := f.x.f.sql.QueryRow(ctx, `SELECT jsonb_build_object(
 'applied',(SELECT jsonb_agg(to_jsonb(m) ORDER BY m.id) FROM grove_migrations m),
 'config',(SELECT jsonb_agg(to_jsonb(c) ORDER BY c.group_id,c.generation) FROM loomplane_group_configurations c),
 'executions',(SELECT jsonb_agg(to_jsonb(e) ORDER BY e.group_id,e.generation) FROM loomplane_group_executions e),
 'candidates',(SELECT jsonb_agg(to_jsonb(c) ORDER BY c.id) FROM loomplane_group_snapshot_candidates c),
 'activation',pg_get_functiondef('loomplane_group_activation_guard()'::regprocedure),
 'guard',pg_get_functiondef('loomplane_group_execution_guard()'::regprocedure),
 'constraints',(SELECT jsonb_agg(pg_get_constraintdef(oid) ORDER BY conrelid,conname) FROM pg_constraint WHERE conrelid IN ('loomplane_group_configurations'::regclass,'loomplane_group_executions'::regclass,'loomplane_group_snapshot_candidates'::regclass)),
 'triggers',(SELECT jsonb_agg(pg_get_triggerdef(oid) ORDER BY tgrelid,tgname) FROM pg_trigger WHERE tgrelid IN ('loomplane_group_configurations'::regclass,'loomplane_group_executions'::regclass,'loomplane_group_snapshot_candidates'::regclass))
 )::text`).Scan(&raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestPlacementGroupSnapshotDowngradeRetainsRecovery(t *testing.T) {
	for _, mode := range []string{"accepted", "issued", "winner", "cancelled_unresolved"} {
		t.Run(mode, func(t *testing.T) {
			f := newGroupSnapshotFixture(t, true, "20261208000700")
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			if mode == "winner" {
				if _, err := f.manager().Publish(ctx, f.group); err != nil {
					t.Fatal(err)
				}
			} else if mode != "accepted" {
				state, err := f.store.BeginSnapshot(ctx, f.group, true)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.store.IssueSnapshot(ctx, state); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "cancelled_unresolved" {
				if _, err := f.x.f.sql.Exec(ctx, `UPDATE loomplane_group_executions SET cancelled_at=now()`); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshotMigrationState(ctx, t, f)
			if err := rollbackGroupSnapshot(ctx, f); err == nil || !strings.Contains(err.Error(), "retained group snapshot state") {
				t.Fatalf("unsafe downgrade result: %v", err)
			}
			if after := snapshotMigrationState(ctx, t, f); after != before {
				t.Fatal("refused downgrade changed applied migration, guard or retained references")
			}
			if mode == "winner" {
				f.store = &deployments.PlacementGroupStore{DB: f.db, Authority: f.authority}
				values, flags, err := f.manager().Values(ctx, f.group)
				if err != nil || values["TIMEOUT"] != "reviewed" || !flags["TIMEOUT"] {
					t.Fatalf("winner not recoverable after refusal: %v", err)
				}
			}
		})
	}
}
func TestPlacementGroupSnapshotDowngradeEmpty(t *testing.T) {
	f := newGroupSnapshotFixture(t, false, "20261208000700")
	ctx := t.Context()
	if err := rollbackGroupSnapshot(ctx, f); err != nil {
		t.Fatal(err)
	}
	var empty bool
	err := f.x.f.sql.QueryRow(ctx, `SELECT to_regclass('loomplane_group_configurations') IS NULL AND to_regclass('loomplane_group_executions') IS NULL AND to_regclass('loomplane_group_snapshot_candidates') IS NULL AND NOT EXISTS(SELECT 1 FROM grove_migrations WHERE version='20261208000700' AND "group"='loomplane') AND position('placement group runtime is unavailable' in pg_get_functiondef('loomplane_group_activation_guard()'::regprocedure))>0`).Scan(&empty)
	if err != nil || !empty {
		t.Fatalf("empty downgrade incomplete: %v", err)
	}
}

type snapshotDowngradePause struct {
	*groupSnapshotAuthority
	entered, resume chan struct{}
	acceptance      bool
}

func (p *snapshotDowngradePause) wait(ctx context.Context) error {
	close(p.entered)
	select {
	case <-p.resume:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *snapshotDowngradePause) AuthorizeGroupAcceptanceTx(ctx context.Context, tx driver.Tx, actor security.Principal, project string) error {
	if err := p.groupSnapshotAuthority.AuthorizeGroupAcceptanceTx(ctx, tx, actor, project); err != nil {
		return err
	}
	if p.acceptance {
		return p.wait(ctx)
	}
	return nil
}
func (p *snapshotDowngradePause) AuthorizePlacementGroupTx(ctx context.Context, tx driver.Tx, g domain.QualifiedGroupPlan, actor security.Principal) error {
	if err := p.groupSnapshotAuthority.AuthorizePlacementGroupTx(ctx, tx, g, actor); err != nil {
		return err
	}
	if !p.acceptance {
		return p.wait(ctx)
	}
	return nil
}
func waitSnapshotMigration(ctx context.Context, t *testing.T, f *groupSnapshotFixture) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := f.x.f.sql.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%group_activation_guard%' AND pid<>pg_backend_pid())`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
func TestPlacementGroupSnapshotDowngradeConcurrentWriter(t *testing.T) {
	for _, mode := range []string{"acceptance", "issuance"} {
		t.Run(mode, func(t *testing.T) {
			f := newGroupSnapshotFixture(t, mode == "issuance", "20261208000700")
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			pause := &snapshotDowngradePause{groupSnapshotAuthority: f.authority, entered: make(chan struct{}), resume: make(chan struct{}), acceptance: mode == "acceptance"}
			var state deployments.GroupSnapshotState
			if mode == "issuance" {
				var err error
				state, err = f.store.BeginSnapshot(ctx, f.group, true)
				if err != nil {
					t.Fatal(err)
				}
				f.store.Authority = pause
			} else {
				f.plans.BindGroupAcceptanceAuthority(pause)
			}
			writer := make(chan error, 1)
			go func() {
				if mode == "issuance" {
					_, err := f.store.IssueSnapshot(ctx, state)
					writer <- err
					return
				}
				actor := security.Principal{OrgID: "org", UserID: "usr"}
				ok, err := f.plans.Decide(security.WithPrincipal(ctx, actor), "org", f.x.plan.ID, planning.StatusApproved, "usr", "", time.Now().UTC())
				if err == nil && !ok {
					err = errors.New("approval did not commit")
				}
				writer <- err
			}()
			select {
			case <-pause.entered:
			case err := <-writer:
				t.Fatalf("writer before fence: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			downgrade := make(chan error, 1)
			go func() { downgrade <- rollbackGroupSnapshot(ctx, f) }()
			waitSnapshotMigration(ctx, t, f)
			close(pause.resume)
			if err := <-writer; err != nil {
				t.Fatal(err)
			}
			if err := <-downgrade; err == nil || !strings.Contains(err.Error(), "retained group snapshot state") {
				t.Fatalf("racing downgrade: %v", err)
			}
			snapshotMigrationState(ctx, t, f)
			var executions, candidates int
			if err := f.x.f.sql.QueryRow(ctx, `SELECT (SELECT count(*) FROM loomplane_group_executions),(SELECT count(*) FROM loomplane_group_snapshot_candidates)`).Scan(&executions, &candidates); err != nil {
				t.Fatal(err)
			}
			want := 0
			if mode == "issuance" {
				want = 1
			}
			if executions != 1 || candidates != want {
				t.Fatalf("committed writer was lost: %d %d", executions, candidates)
			}
		})
	}
}

func TestPlacementGroupSnapshotDowngradeBeforeAcceptance(t *testing.T) {
	f := newGroupSnapshotFixture(t, false, "20261208000700")
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	blocker, err := f.x.f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback() }()
	if _, err = blocker.Exec(ctx, `LOCK TABLE loomplane_group_executions IN ACCESS SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	downgrade := make(chan error, 1)
	go func() { downgrade <- rollbackGroupSnapshot(ctx, f) }()
	waitSnapshotMigration(ctx, t, f)
	approval := make(chan error, 1)
	go func() {
		actor := security.Principal{OrgID: "org", UserID: "usr"}
		_, e := f.plans.Decide(security.WithPrincipal(ctx, actor), "org", f.x.plan.ID, planning.StatusApproved, "usr", "", time.Now().UTC())
		approval <- e
	}()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err = f.x.f.sql.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory' AND query LIKE '%variable-sources/v1%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if err = blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-downgrade; err != nil {
		t.Fatal(err)
	}
	if err = <-approval; err == nil {
		t.Fatal("approval committed after schema downgrade")
	}
	var unchanged bool
	if err = f.x.f.sql.QueryRow(ctx, `SELECT status='ready' AND to_regclass('loomplane_group_executions') IS NULL FROM loomplane_plans WHERE id=$1`, f.x.plan.ID).Scan(&unchanged); err != nil || !unchanged {
		t.Fatalf("approval crossed downgrade: %v", err)
	}
}
