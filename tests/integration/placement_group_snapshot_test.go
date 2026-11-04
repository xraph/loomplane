//go:build integration

package integration

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/grove"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	vaultpg "github.com/xraph/vault/store/postgres"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
	"github.com/xraph/loomplane/pkg/fleet"
)

// This fixture exercises persisted target/acceptance guards. It deliberately
// does not stand in for the installed identity engine's transaction binding.
type groupSnapshotAuthority struct{ revoked atomic.Bool }

func (a *groupSnapshotAuthority) LockGroupAuthorityTx(ctx context.Context, tx driver.Tx) error {
	if a.revoked.Load() {
		return security.ErrForbidden
	}
	// Fixed fixture gate proves call ordering without claiming identity tables.
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('group-fixture-identity/v1',0))`)
	return err
}
func (a *groupSnapshotAuthority) AuthorizePlacementGroup(context.Context, domain.QualifiedGroupPlan) (security.Principal, error) {
	if a.revoked.Load() {
		return security.Principal{}, security.ErrForbidden
	}
	return security.Principal{OrgID: "org", UserID: "usr"}, nil
}
func (a *groupSnapshotAuthority) AuthorizeGroupAcceptanceTx(ctx context.Context, tx driver.Tx, p security.Principal, project string) error {
	if a.revoked.Load() || p.OrgID != "org" || p.UserID != "usr" || project != "proj" {
		return security.ErrForbidden
	}
	// Real target facts are checked by normal acceptance; actor evaluation is
	// isolated here until the installed transaction authority is available.
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_projects WHERE org_id=$1 AND id=$2)`, p.OrgID, project).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return security.ErrForbidden
	}
	return ctx.Err()
}
func (a *groupSnapshotAuthority) AuthorizePlacementGroupTx(ctx context.Context, tx driver.Tx, g domain.QualifiedGroupPlan, p security.Principal) error {
	if a.revoked.Load() {
		return security.ErrForbidden
	}
	return planning.GuardAcceptedPlacementGroupTx(ctx, tx, g, p)
}

type groupSnapshotFixture struct {
	x         *placementGroupFixture
	db        *pgdriver.PgDB
	plans     *planning.PgStore
	variables *environments.Service
	vault     *deployments.VaultResourceSecrets
	authority *groupSnapshotAuthority
	store     *deployments.PlacementGroupStore
	group     domain.QualifiedGroupPlan
	variable  *environments.Variable
}

func newGroupSnapshotFixture(t *testing.T, accept bool) *groupSnapshotFixture {
	t.Helper()
	x := newPlacementGroupFixture(t)
	ctx := t.Context()
	dsn := x.f.sql.Config().ConnString()
	db := pgdriver.New()
	if err := db.Open(ctx, dsn, driver.WithPoolSize(1)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pools, err := environments.OpenSourcePools(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if e := pools.Close(); e != nil {
			t.Error(e)
		}
	})
	sourceStorage := vaultpg.New(pools.Vault)
	if err = sourceStorage.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEncryptor(bytes.Repeat([]byte{43}, 32))
	if err != nil {
		t.Fatal(err)
	}
	variables := environments.NewService(environments.NewPgStoreWithSourcePool(db, pools.SQL), environments.NewVaultSecrets(secret.NewService(sourceStorage, enc)), projects.NewPgStore(db), nil, &groupAuth{}, nil, nil)
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	variable, err := variables.CreateProject(ctx, actor, "proj", environments.CreateVariableInput{Key: "TIMEOUT", Value: "reviewed", Secret: true, Environment: "production"})
	if err != nil {
		t.Fatal(err)
	}
	x.service.Variables = variables
	delete(x.manifest.Federation.PlacementGroups, "other")
	svc := x.manifest.Services["api"]
	svc.Source = ""
	x.manifest.Services["api"] = svc
	groups, err := x.service.PreviewPlacementGroups(ctx, x.plan, x.manifest, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	x.plan.PlacementGroups = groups
	x.plan.Connections = map[string]string{}
	x.plan.Actions = []planning.Action{}
	x.plan.Status = planning.StatusReady
	x.plan.Executable = true // Internal persistence fixture only; public planner remains closed.
	x.plan.Revision = "snapshot-review"
	now := time.Now().UTC()
	x.plan.PlannedAt = &now
	if ok, e := x.f.plans.FinishPlanning(ctx, x.plan); e != nil || !ok {
		t.Fatalf("finish %v %v", ok, e)
	}
	authority := &groupSnapshotAuthority{}
	plans := planning.NewPgStore(db)
	plans.BindGroupAcceptanceAuthority(authority)
	if accept {
		if ok, e := plans.Decide(security.WithPrincipal(ctx, actor), "org", x.plan.ID, planning.StatusApproved, "usr", "", now); e != nil || !ok {
			t.Fatalf("accept %v %v", ok, e)
		}
	}
	store := &deployments.PlacementGroupStore{DB: db, Authority: authority}
	// Resource Vault intentionally shares the only main-pool connection.
	wrapped, err := grove.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	resourceVault := deployments.NewVaultResourceSecrets(secret.NewService(vaultpg.New(wrapped), enc))
	return &groupSnapshotFixture{x: x, db: db, plans: plans, variables: variables, vault: resourceVault, authority: authority, store: store, group: groups[0], variable: variable}
}
func (f *groupSnapshotFixture) manager() *deployments.GroupConfigurationSnapshots {
	return &deployments.GroupConfigurationSnapshots{Store: f.store, Secrets: f.vault, Variables: f.variables}
}
func TestPlacementGroupSnapshotPersistencePoolOne(t *testing.T) {
	f := newGroupSnapshotFixture(t, true)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	id, err := f.manager().Publish(ctx, f.group)
	if err != nil || id == "" {
		t.Fatalf("publish: %s %v", id, err)
	}
	if _, err = f.variables.Update(ctx, security.Principal{OrgID: "org", UserID: "usr"}, f.variable.ID, environments.UpdateVariableInput{Value: "rotated"}); err != nil {
		t.Fatal(err)
	}
	// A new repository instance represents process restart, with no in-memory claim.
	f.store = &deployments.PlacementGroupStore{DB: f.db, Authority: f.authority}
	for range f.group.Members {
		values, flags, e := f.manager().Values(ctx, f.group)
		if e != nil || values["TIMEOUT"] != "reviewed" || !flags["TIMEOUT"] {
			t.Fatalf("retained values: %v %v %v", values, flags, e)
		}
	}
	f.authority.revoked.Store(true)
	if _, _, err = f.manager().Values(ctx, f.group); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("revoked: %v", err)
	}
}
func TestPlacementGroupSnapshotAcceptanceRejectsChangedSources(t *testing.T) {
	f := newGroupSnapshotFixture(t, false)
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	if _, err := f.variables.Update(t.Context(), actor, f.variable.ID, environments.UpdateVariableInput{Value: "changed"}); err != nil {
		t.Fatal(err)
	}
	ok, err := f.plans.Decide(security.WithPrincipal(t.Context(), actor), "org", f.x.plan.ID, planning.StatusApproved, "usr", "", time.Now().UTC())
	if ok || !errors.Is(err, planning.ErrStale) {
		t.Fatalf("stale acceptance: %v %v", ok, err)
	}
	var count int
	if err = f.x.f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_group_executions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial acceptance: %d %v", count, err)
	}
}
func TestPlacementGroupSnapshotDatabaseRefusesPartialWinner(t *testing.T) {
	f := newGroupSnapshotFixture(t, true)
	ctx := t.Context()
	state, err := f.store.BeginSnapshot(ctx, f.group, true)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := f.store.IssueSnapshot(ctx, state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.x.f.sql.Exec(ctx, `UPDATE loomplane_group_executions SET snapshot_candidate=$1`, candidate.ID); err == nil {
		t.Fatal("partial winner persisted without exact Vault identity/version")
	}
}

type snapshotLostResponse struct {
	*deployments.PlacementGroupStore
	issue, beforePublish, afterPublish atomic.Bool
}

func (s *snapshotLostResponse) IssueSnapshot(ctx context.Context, state deployments.GroupSnapshotState) (deployments.GroupSnapshotCandidate, error) {
	c, err := s.PlacementGroupStore.IssueSnapshot(ctx, state)
	if err == nil && s.issue.Swap(false) {
		return deployments.GroupSnapshotCandidate{}, errors.New("lost issue response")
	}
	return c, err
}
func (s *snapshotLostResponse) PublishSnapshot(ctx context.Context, state deployments.GroupSnapshotState, c deployments.GroupSnapshotCandidate) error {
	if s.beforePublish.Swap(false) {
		return errors.New("publication transaction never started")
	}
	err := s.PlacementGroupStore.PublishSnapshot(ctx, state, c)
	if err == nil && s.afterPublish.Swap(false) {
		return errors.New("lost publication response")
	}
	return err
}

type snapshotVaultResponse struct {
	*deployments.VaultResourceSecrets
	lost            atomic.Bool
	pause           atomic.Bool
	entered, resume chan struct{}
}

func (s *snapshotVaultResponse) PutVersion(ctx context.Context, key string, value []byte) (int64, error) {
	if s.pause.Swap(false) {
		close(s.entered)
		select {
		case <-s.resume:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	version, err := s.VaultResourceSecrets.PutVersion(ctx, key, value)
	if err == nil && s.lost.Swap(false) {
		return 0, errors.New("lost Vault response")
	}
	return version, err
}
func TestPlacementGroupRecoverySnapshotUncertainResponses(t *testing.T) {
	for _, mode := range []string{"issue", "vault", "before_publish", "after_publish"} {
		t.Run(mode, func(t *testing.T) {
			f := newGroupSnapshotFixture(t, true)
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			repo := &snapshotLostResponse{PlacementGroupStore: f.store}
			vault := &snapshotVaultResponse{VaultResourceSecrets: f.vault}
			switch mode {
			case "issue":
				repo.issue.Store(true)
			case "vault":
				vault.lost.Store(true)
			case "before_publish":
				repo.beforePublish.Store(true)
			case "after_publish":
				repo.afterPublish.Store(true)
			}
			manager := &deployments.GroupConfigurationSnapshots{Store: repo, Secrets: vault, Variables: f.variables}
			if _, err := manager.Publish(ctx, f.group); err == nil {
				t.Fatal("expected uncertain response")
			}
			if mode != "issue" {
				// Existing encrypted data is authoritative after source deletion. No
				// restart may capture latest values or fail because the source disappeared.
				if err := f.variables.Delete(ctx, security.Principal{OrgID: "org", UserID: "usr"}, f.variable.ID); err != nil {
					t.Fatal(err)
				}
			}
			f.store = &deployments.PlacementGroupStore{DB: f.db, Authority: f.authority}
			if _, err := f.manager().Publish(ctx, f.group); err != nil {
				t.Fatalf("recover: %v", err)
			}
			values, flags, err := f.manager().Values(ctx, f.group)
			if err != nil || values["TIMEOUT"] != "reviewed" || !flags["TIMEOUT"] {
				t.Fatalf("recovered snapshot: %v %v %v", values, flags, err)
			}
			state, err := f.store.BeginSnapshot(ctx, f.group, false)
			if err != nil {
				t.Fatal(err)
			}
			expected := 1
			if mode == "issue" {
				expected = 2
			}
			if len(state.Candidates) != expected || state.Winner == nil {
				t.Fatalf("candidate history: %+v", state.Candidates)
			}
			for _, candidate := range state.Candidates {
				_, version, e := f.vault.SnapshotMetadata(ctx, candidate.Key)
				if mode == "issue" && candidate.Epoch == 1 {
					if !errors.Is(e, deployments.ErrNoCredential) {
						t.Fatalf("lost issue wrote key: %v", e)
					}
				} else if e != nil || version != 1 {
					t.Fatalf("candidate overwritten: %d %v", version, e)
				}
			}
		})
	}
}
func TestPlacementGroupRecoverySnapshotLatePriorWriter(t *testing.T) {
	f := newGroupSnapshotFixture(t, true)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	vault := &snapshotVaultResponse{VaultResourceSecrets: f.vault, entered: make(chan struct{}), resume: make(chan struct{})}
	vault.pause.Store(true)
	manager := &deployments.GroupConfigurationSnapshots{Store: f.store, Secrets: vault, Variables: f.variables}
	old := make(chan error, 1)
	go func() { _, err := manager.Publish(ctx, f.group); old <- err }()
	select {
	case <-vault.entered:
	case err := <-old:
		t.Fatalf("before effect: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := f.manager().Publish(ctx, f.group); err != nil {
		t.Fatal(err)
	}
	before, err := f.store.BeginSnapshot(ctx, f.group, false)
	if err != nil {
		t.Fatal(err)
	}
	close(vault.resume)
	if err = <-old; err == nil {
		t.Fatal("stale writer published")
	}
	after, err := f.store.BeginSnapshot(ctx, f.group, false)
	if err != nil || before.Winner == nil || after.Winner == nil || *before.Winner != *after.Winner || len(after.Candidates) != 2 {
		t.Fatalf("winner changed after late write: %v", err)
	}
	for _, candidate := range after.Candidates {
		if _, version, e := f.vault.SnapshotMetadata(ctx, candidate.Key); e != nil || version != 1 {
			t.Fatalf("uncertain candidate not retained: %d %v", version, e)
		}
	}
}

func TestPlacementGroupSnapshotSourceGateBeforeCloudAndFleetLocks(t *testing.T) {
	f := newGroupSnapshotFixture(t, false)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	fleetStore := fleets.NewPgStore(f.x.f.db)
	service := fleets.NewService(fleets.Deps{Store: fleetStore, Projects: projects.NewPgStore(f.x.f.db), Authz: fleetAuth{}})
	managed, err := service.CreateFleet(ctx, actor, "second")
	if err != nil {
		t.Fatal(err)
	}
	stage, err := service.CreateStage(ctx, actor, managed.ID, fleet.CreateStageInput{Name: "production", EnvironmentKind: "production", AllowedCloudIDs: []string{"cloud"}, MaxInstances: 1})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.x.f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = environments.LockRuntimeSourcesTx(ctx, tx); err != nil {
		t.Fatal(err)
	}
	approval := make(chan error, 1)
	reservation := make(chan error, 1)
	go func() {
		_, e := f.plans.Decide(security.WithPrincipal(ctx, actor), "org", f.x.plan.ID, planning.StatusApproved, "usr", "", time.Now().UTC())
		approval <- e
	}()
	go func() {
		_, e := fleetStore.ReserveInstance(ctx, fleets.FleetScope{OrgID: "org", ProjectID: "second", FleetID: managed.ID}, fleet.CreateInstanceInput{StageID: stage.ID, ReleaseID: "release", ProfileID: "small", ExternalRef: "ordered", IdempotencyKey: "ordered"})
		reservation <- e
	}()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiters int
		if err = f.x.f.sql.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory' AND query LIKE '%variable-sources/v1%'`).Scan(&waiters); err != nil {
			t.Fatal(err)
		}
		if waiters >= 2 {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	// Neither waiting operation may own a Cloud or parent lock first. The
	// source writer takes both now; an inverted order would deadlock here.
	if err = cloudprofiles.LockOrganization(ctx, tx, "org"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM loomplane_variables WHERE id=$1`, f.variable.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-approval; !errors.Is(err, planning.ErrStale) {
		t.Fatalf("approval used old sources: %v", err)
	}
	if err = <-reservation; err != nil {
		t.Fatalf("Fleet reservation: %v", err)
	}
}

func TestPlacementGroupSnapshotRotationInvalidatesTimeoutVerification(t *testing.T) {
	f := newGroupSnapshotFixture(t, false)
	ctx := t.Context()
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	lifetime := int64(30)
	drain := domain.DrainPolicy{Mode: "bounded", MaxConnectionLifetimeSeconds: &lifetime, TerminationGraceSeconds: 30}
	group := f.x.manifest.Federation.PlacementGroups["web"]
	for _, p := range f.x.prerequisites {
		if _, err := f.x.service.VerifyTimeout(ctx, actor, "proj", planning.TimeoutVerificationInput{AttachmentID: p.AttachmentID, ExpectedAttachmentRevision: 1, ConfigRevision: f.group.ConfigRevision, DrainPolicy: drain, EvidenceRef: "verified-effective-config"}); err != nil {
			t.Fatal(err)
		}
		saved, err := f.x.service.Save(ctx, actor, "proj", planning.MemberPrerequisiteInput{ID: p.ID, AttachmentID: p.AttachmentID, ExpectedRevision: p.Revision, TLSSecretName: p.TLSSecretName, ExpectedTLSSecretUID: p.TLSSecretUID, RenewalAuthorityID: p.RenewalAuthorityID, ExpectedRenewalAuthorityGeneration: p.RenewalAuthorityGeneration, DrainPolicy: drain})
		if err != nil {
			t.Fatal(err)
		}
		for name, m := range group.Members {
			if m.PrerequisiteID == p.ID {
				m.PrerequisiteRevision = saved.Revision
				group.Members[name] = m
			}
		}
	}
	f.x.manifest.Federation.PlacementGroups["web"] = group
	if _, err := f.x.service.PreviewPlacementGroups(ctx, f.x.plan, f.x.manifest, nil, nil); err != nil {
		t.Fatalf("matching effective config: %v", err)
	}
	if _, err := f.variables.Update(ctx, actor, f.variable.ID, environments.UpdateVariableInput{Value: "different-timeout"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.x.service.PreviewPlacementGroups(ctx, f.x.plan, f.x.manifest, nil, nil); err == nil {
		t.Fatal("rotated variable retained old timeout verification")
	}
}

func TestPlacementGroupSnapshotDeleteBeforeCaptureAndCancellation(t *testing.T) {
	for _, mode := range []string{"delete", "cancel", "foreign"} {
		t.Run(mode, func(t *testing.T) {
			f := newGroupSnapshotFixture(t, true)
			ctx := t.Context()
			group := f.group
			switch mode {
			case "delete":
				if err := f.variables.Delete(ctx, security.Principal{OrgID: "org", UserID: "usr"}, f.variable.ID); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				if _, err := f.x.f.sql.Exec(ctx, `UPDATE loomplane_group_executions SET cancelled_at=now()`); err != nil {
					t.Fatal(err)
				}
			case "foreign":
				group.ProjectID = "second"
			}
			if _, err := f.manager().Publish(ctx, group); err == nil {
				t.Fatal("invalid capture succeeded")
			}
			var candidates int
			if err := f.x.f.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_group_executions WHERE snapshot_candidate IS NOT NULL`).Scan(&candidates); err != nil || candidates != 0 {
				t.Fatalf("invalid snapshot attached: %d %v", candidates, err)
			}
		})
	}
}

func TestPlacementGroupSnapshotAcceptanceRequiresInstalledTransactionAuthority(t *testing.T) {
	f := newGroupSnapshotFixture(t, false)
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	ok, err := planning.NewPgStore(f.db).Decide(security.WithPrincipal(t.Context(), actor), "org", f.x.plan.ID, planning.StatusApproved, "usr", "", time.Now().UTC())
	if ok || !errors.Is(err, planning.ErrUnavailable) {
		t.Fatalf("missing transaction authority accepted group: %v %v", ok, err)
	}
}
