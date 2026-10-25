//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type expansionProvider struct{ *infraProvider }

func (p expansionProvider) ServerOptions(context.Context, provider.InfrastructureCredential) (provider.ServerOptions, error) {
	return provider.ServerOptions{Regions: []provider.ServerRegion{{ID: "region"}}, Images: []provider.ServerImage{{ID: "ubuntu", Bootstrap: true, Architecture: "amd64", Regions: []string{"region"}}}, Sizes: []provider.ServerSize{{ID: "small", CPUs: 2, MemoryMiB: 2048, DiskGB: 20, Architectures: []string{"amd64"}, Regions: []string{"region"}}}}, nil
}
func (p expansionProvider) CapacityInventory(context.Context, provider.InfrastructureCredential) (provider.CapacityInventory, error) {
	out := provider.CapacityInventory{Provider: "digitalocean", AccountID: "account", CheckedAt: time.Now()}
	if p.remote.ID != "" {
		out.Servers = []provider.CapacityServer{{ID: p.remote.ID, Region: "region", CPUMillis: inventoryNumber(2000), MemoryBytes: inventoryNumber(2 << 30), StorageBytes: inventoryNumber(20_000_000_000)}}
	}
	return out, nil
}

// The fixture resolver reads approved persistent rows, rather than trusting the
// demand passed to EnsureCapacity. Task5 supplies the production resolver.
type expansionAuthority struct {
	db      *pgx.Conn
	revoked bool
}

func (a *expansionAuthority) ResolveApprovedCapacityDemand(ctx context.Context, org, plan, service string) (*cloudinfra.CapacityDemand, error) {
	if a.revoked {
		return nil, security.ErrForbidden
	}
	var raw []byte
	if err := a.db.QueryRow(ctx, `SELECT result FROM loomplane_plans WHERE org_id=$1 AND id=$2 AND status='approved'`, org, plan).Scan(&raw); err != nil {
		return nil, err
	}
	var d cloudinfra.CapacityDemand
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	if d.Service != service {
		return nil, security.ErrForbidden
	}
	return &d, nil
}
func expansionFixture(t *testing.T, approval bool, minServers int64) (*infraFixture, *expansionAuthority, cloudinfra.CapacityDemand) {
	t.Helper()
	f := newInfraFixture(t)
	adapters := cloudinfra.NewAdapters()
	if err := adapters.RegisterServer("digitalocean", expansionProvider{f.provider}); err != nil {
		t.Fatal(err)
	}
	f.svc.Adapters = adapters
	f.svc.EnableAutomaticCapacity = true
	pool := &cloudinfra.Pool{ID: "pool", OrgID: "org", CloudID: "cloud", Name: "Grow", Revision: 1, Members: []cloudinfra.PoolMember{}}
	if err := f.store.SavePool(t.Context(), pool, 0); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := f.svc.Clouds.(*cloudprofiles.PgStore).CreateTarget(t.Context(), &cloudprofiles.Target{ID: "pool-target", OrgID: "org", CloudID: "cloud", ProjectID: "proj", Name: "pool", DisplayName: "Pool", Provider: "server", Runtime: "server", PoolID: "pool", Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	policy := cloudinfra.CapacityPolicy{Enabled: true, RequireExpansionApproval: approval, Limits: cloudinfra.ResourceVector{Servers: 4, CPUMillis: 8000, MemoryBytes: 8 << 30, StorageBytes: 80_000_000_000}, Pools: []cloudinfra.PoolExpansionPolicy{{PoolID: "pool", ConnectionID: "infra-account", Region: "region", Architecture: "amd64", Image: "ubuntu", AllowedSizes: []string{"small"}, MinServers: minServers, MaxServers: 4, SystemReserve: cloudinfra.ResourceVector{CPUMillis: 500, MemoryBytes: 512 << 20}}}}
	if _, err := f.svc.SaveCapacityPolicy(t.Context(), f.p, "cloud", cloudinfra.CapacityPolicyInput{Policy: policy}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RefreshCapacity(t.Context(), f.p, "cloud"); err != nil {
		t.Fatal(err)
	}
	a := &expansionAuthority{db: f.sql}
	f.svc.CapacityDemandAuthority = a
	d := cloudinfra.CapacityDemand{OrgID: "org", CloudID: "cloud", PoolID: "pool", TargetID: "pool-target", ProjectID: "proj", EnvironmentID: "env", PlanID: "capacity-plan", Service: "app", Architecture: "amd64", Resources: cloudinfra.ResourceVector{CPUMillis: 1000, MemoryBytes: 1 << 30}, PolicyRevision: 1, IntentHash: "approved-demand"}
	if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) VALUES('capacity-manifest','org','proj',1,'version: 1','capacity','manager',now())`); err != nil {
		t.Fatal(err)
	}
	expansionPlan(t, f, d)
	return f, a, d
}
func expansionPlan(t *testing.T, f *infraFixture, d cloudinfra.CapacityDemand) {
	t.Helper()
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,revision,manifest_id,manifest_revision,result,executable,created_by,created_at) VALUES($1,'org','proj','env','production','approved','approved','capacity-manifest',1,$2,true,'deployer',now())`, d.PlanID, raw)
	if err != nil {
		t.Fatal(err)
	}
}
func ensureExpansion(t *testing.T, f *infraFixture, d cloudinfra.CapacityDemand) *cloudinfra.Expansion {
	t.Helper()
	decision, err := f.svc.EnsureCapacity(t.Context(), d)
	if err != nil || decision == nil || decision.ExpansionID == "" {
		t.Fatalf("ensure: %+v %v", decision, err)
	}
	e, err := f.store.GetExpansion(t.Context(), d.OrgID, d.CloudID, decision.ExpansionID)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func reconcileExpansion(t *testing.T, f *infraFixture) {
	t.Helper()
	if err := f.svc.ReconcileExpansions(t.Context()); err != nil {
		t.Fatal(err)
	}
}
func TestExpansionAutomatic(t *testing.T) {
	f, _, d := expansionFixture(t, false, 0)
	e := ensureExpansion(t, f, d)
	if e.OperationID == "" {
		t.Fatalf("automatic expansion was not accepted: %+v", e)
	}
	reconcileExpansion(t, f)
	again := ensureExpansion(t, f, d)
	reconcileExpansion(t, f)
	if again.ID != e.ID || f.provider.creates != 1 {
		t.Fatalf("duplicate create: %s %s %d", e.ID, again.ID, f.provider.creates)
	}
	o, err := f.store.GetOperation(t.Context(), "org", "cloud", e.OperationID)
	if err != nil || o.Principal.UserID != "manager" {
		t.Fatalf("policy authority: %+v %v", o, err)
	}
	forged := d
	forged.Resources.CPUMillis = 1
	if _, err = f.svc.EnsureCapacity(t.Context(), forged); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("forged demand accepted: %v", err)
	}
}
func TestExpansionApproval(t *testing.T) {
	f, _, d := expansionFixture(t, true, 0)
	e := ensureExpansion(t, f, d)
	reconcileExpansion(t, f)
	if f.provider.creates != 0 || e.OperationID != "" {
		t.Fatal("provider effect before approval")
	}
	if _, err := f.svc.ApproveExpansion(t.Context(), f.p, "cloud", e.ID, e.Revision+1); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("stale approval: %v", err)
	}
	approved, err := f.svc.ApproveExpansion(t.Context(), f.p, "cloud", e.ID, e.Revision)
	if err != nil {
		t.Fatal(err)
	}
	reconcileExpansion(t, f)
	if approved.ApprovedBy != "manager" || f.provider.creates != 1 {
		t.Fatalf("approval: %+v creates=%d", approved, f.provider.creates)
	}
}
func TestExpansionRecovery(t *testing.T) {
	f, _, d := expansionFixture(t, false, 0)
	f.provider.lost = true
	e := ensureExpansion(t, f, d)
	reconcileExpansion(t, f)
	f.svc = cloudinfra.NewService(f.svc.Deps)
	reconcileExpansion(t, f)
	got, err := f.store.GetExpansion(t.Context(), "org", "cloud", e.ID)
	if err != nil || got.OperationID != e.OperationID || f.provider.creates != 1 || !f.provider.seenReconcile {
		t.Fatalf("restart: %+v %v creates=%d reconcile=%v", got, err, f.provider.creates, f.provider.seenReconcile)
	}
}
func TestExpansionMinimum(t *testing.T) {
	for _, approval := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic", true: "approval"}[approval], func(t *testing.T) {
			f, _, _ := expansionFixture(t, approval, 1)
			reconcileExpansion(t, f)
			reconcileExpansion(t, f)
			var count int
			var size string
			if err := f.sql.QueryRow(t.Context(), `SELECT count(*),min(size) FROM loomplane_cloud_expansions`).Scan(&count, &size); err != nil {
				t.Fatal(err)
			}
			if count != 1 || size != "small" {
				t.Fatalf("minimum: count=%d size=%s", count, size)
			}
			if approval && f.provider.creates != 0 || !approval && f.provider.creates != 1 {
				t.Fatalf("creates=%d", f.provider.creates)
			}
		})
	}
}
func TestExpansionCancellation(t *testing.T) {
	f, _, d := expansionFixture(t, false, 0)
	e := ensureExpansion(t, f, d)
	reconcileExpansion(t, f)
	if err := f.svc.DetachCapacityDemand(t.Context(), "org", d.PlanID, d.Service); err != nil {
		t.Fatal(err)
	}
	reconcileExpansion(t, f)
	o, err := f.store.GetOperation(t.Context(), "org", "cloud", e.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	v, err := f.store.GetServer(t.Context(), "org", "cloud", o.ServerID)
	if err != nil || v.ResourceID != "42" {
		t.Fatalf("accepted server hidden: %+v %v", v, err)
	}
	var count int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_cloud_capacity_reservations WHERE status='reserved'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("accepted reservation released: %d %v", count, err)
	}
}
func TestExpansionSharesFutureHost(t *testing.T) {
	f, _, d := expansionFixture(t, true, 0)
	a := ensureExpansion(t, f, d)
	second := d
	second.PlanID = "second-plan"
	second.Service = "second"
	second.IntentHash = "second"
	second.Resources.CPUMillis = 500
	second.Resources.MemoryBytes = 512 << 20
	expansionPlan(t, f, second)
	b := ensureExpansion(t, f, second)
	if a.ID != b.ID {
		t.Fatal("fitting demands did not share host")
	}
	third := second
	third.PlanID = "third-plan"
	third.Service = "third"
	third.IntentHash = "third"
	third.Resources.CPUMillis = 1
	expansionPlan(t, f, third)
	c := ensureExpansion(t, f, third)
	if c.ID == a.ID {
		t.Fatal("future host overbooked")
	}
}
func TestExpansionReevaluatesPolicyAndCeiling(t *testing.T) {
	f, _, d := expansionFixture(t, true, 0)
	e := ensureExpansion(t, f, d)
	policy, err := f.store.GetCapacityPolicy(t.Context(), "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	policy.Limits.CPUMillis = 1000
	if _, err = f.svc.SaveCapacityPolicy(t.Context(), f.p, "cloud", cloudinfra.CapacityPolicyInput{Policy: *policy, ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.ApproveExpansion(t.Context(), f.p, "cloud", e.ID, e.Revision); err == nil {
		t.Fatal("stale policy approval accepted")
	}
	reconcileExpansion(t, f)
	current := ensureExpansion(t, f, d)
	_, err = f.svc.ApproveExpansion(t.Context(), f.p, "cloud", current.ID, current.Revision)
	var limit *cloudinfra.CapacityAdmissionError
	if !errors.As(err, &limit) || limit.Block.Reason != "hard_limit" {
		t.Fatalf("hard limit approval: %v", err)
	}
	if f.provider.creates != 0 {
		t.Fatal("approval overrode ceiling")
	}
	policy, err = f.store.GetCapacityPolicy(t.Context(), "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	policy.Limits.CPUMillis = 8000
	policy.RequireExpansionApproval = false
	if _, err = f.svc.SaveCapacityPolicy(t.Context(), f.p, "cloud", cloudinfra.CapacityPolicyInput{Policy: *policy, ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	reconcileExpansion(t, f)
	reconcileExpansion(t, f)
	if f.provider.creates != 1 {
		t.Fatalf("unchanged approved demand did not resume: %d", f.provider.creates)
	}
}
func TestExpansionConfiguredEmptyPool(t *testing.T) {
	f, _, _ := expansionFixture(t, false, 0)
	if _, err := f.svc.SavePool(t.Context(), f.p, "cloud", "pool", cloudinfra.PoolInput{Name: "Grow", Revision: 1}); err != nil {
		t.Fatalf("enabled empty pool rejected: %v", err)
	}
}

type expansionRuntime struct{ *infraRuntime }

func (r expansionRuntime) Inspect(ctx context.Context, raw []byte, settings map[string]string, vacant bool) (cloudinfra.RuntimeHealth, error) {
	h, err := r.infraRuntime.Inspect(ctx, raw, settings, vacant)
	h.EffectiveCPUMillis = 2000
	h.EffectiveMemoryBytes = 2 << 30
	return h, err
}

type expansionWakeup struct {
	calls int
	fail  bool
}

func (w *expansionWakeup) WakeCapacityDemand(context.Context, cloudinfra.CapacityDemand) error {
	w.calls++
	if w.fail {
		return errors.New("durable signal unavailable")
	}
	return nil
}
func TestExpansionEnrollmentAndWakeRetry(t *testing.T) {
	f, _, d := expansionFixture(t, false, 0)
	f.svc.Runtime = expansionRuntime{f.runtime}
	wake := &expansionWakeup{fail: true}
	f.svc.CapacityDemandWakeup = wake
	e := ensureExpansion(t, f, d)
	reconcileExpansion(t, f)
	o, err := f.store.GetOperation(t.Context(), "org", "cloud", e.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	o.Enrolled = true
	o.HostKey = infraFixtureHostKey(t)
	if err = f.store.SaveOperation(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	if err = f.svc.ReconcileExpansions(t.Context()); err == nil {
		t.Fatal("signal failure was hidden")
	}
	e, err = f.store.GetExpansion(t.Context(), "org", "cloud", e.ID)
	if err != nil || e.Status != "ready" {
		t.Fatalf("wake failure rolled back readiness: %+v %v", e, err)
	}
	pool, err := f.store.GetPool(t.Context(), "org", "cloud", "pool")
	if err != nil || len(pool.Members) != 1 || pool.Members[0].CPUMillis != 1500 || pool.Members[0].MemoryBytes != 1536<<20 {
		t.Fatalf("pool budget: %+v %v", pool, err)
	}
	wake.fail = false
	reconcileExpansion(t, f)
	if wake.calls != 2 || f.provider.creates != 1 {
		t.Fatalf("wake retry: calls=%d creates=%d", wake.calls, f.provider.creates)
	}
	decision, err := f.svc.EnsureCapacity(t.Context(), d)
	if err != nil || decision.Placement == nil || decision.Placement.ServerID != o.ServerID {
		t.Fatalf("ready candidate: %+v %v", decision, err)
	}
	if err = f.svc.DetachCapacityDemand(t.Context(), "org", d.PlanID, d.Service); err != nil {
		t.Fatal(err)
	}
	reconcileExpansion(t, f)
	if wake.calls != 2 {
		t.Fatal("detached waiter was signalled")
	}
}
func TestExpansionRevocation(t *testing.T) {
	for _, attempted := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-effect", true: "accepted-reconciliation"}[attempted], func(t *testing.T) {
			f, a, d := expansionFixture(t, false, 0)
			e := ensureExpansion(t, f, d)
			if attempted {
				reconcileExpansion(t, f)
			}
			a.revoked = true
			f.auth.revokedUser = "manager"
			reconcileExpansion(t, f)
			if !attempted && f.provider.creates != 0 {
				t.Fatal("revoked actor created host")
			}
			if attempted && f.provider.creates != 1 {
				t.Fatal("accepted receipt reconciliation duplicated host")
			}
			o, err := f.store.GetOperation(t.Context(), "org", "cloud", e.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			if attempted && o.Status != "waiting" {
				t.Fatalf("accepted reconciliation stopped after revocation: %+v", o)
			}
		})
	}
}

func TestExpansionCancellationBeforeApproval(t *testing.T) {
	f, _, d := expansionFixture(t, true, 0)
	e := ensureExpansion(t, f, d)
	if err := f.svc.DetachCapacityDemand(t.Context(), "org", d.PlanID, d.Service); err != nil {
		t.Fatal(err)
	}
	reconcileExpansion(t, f)
	got, err := f.store.GetExpansion(t.Context(), "org", "cloud", e.ID)
	if err != nil || got.Status != "cancelled" || f.provider.creates != 0 {
		t.Fatalf("unaccepted cancellation: %+v %v creates=%d", got, err, f.provider.creates)
	}
	var count int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_cloud_capacity_reservations`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("approval reserved headroom: %d %v", count, err)
	}
}

func TestExpansionAcceptedPolicyRevalidation(t *testing.T) {
	for _, approval := range []bool{false, true} {
		t.Run(map[bool]string{false: "ceiling", true: "approval-mode"}[approval], func(t *testing.T) {
			f, _, d := expansionFixture(t, false, 0)
			e := ensureExpansion(t, f, d)
			original, err := f.store.GetOperation(t.Context(), "org", "cloud", e.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			policy, err := f.store.GetCapacityPolicy(t.Context(), "org", "cloud")
			if err != nil {
				t.Fatal(err)
			}
			if approval {
				policy.RequireExpansionApproval = true
			} else {
				policy.Limits.CPUMillis = 1000
			}
			if _, err = f.svc.SaveCapacityPolicy(t.Context(), f.p, "cloud", cloudinfra.CapacityPolicyInput{Policy: *policy, ExpectedRevision: 1}); err != nil {
				t.Fatal(err)
			}
			reconcileExpansion(t, f)
			if f.provider.creates != 0 {
				t.Fatal("changed policy allowed first effect")
			}
			var count int
			if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_cloud_capacity_reservations WHERE id=$1 AND status='reserved'`, e.OperationID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("reservation lost: %d %v", count, err)
			}
			if approval {
				current, getErr := f.store.GetExpansion(t.Context(), "org", "cloud", e.ID)
				if getErr != nil {
					t.Fatal(getErr)
				}
				if _, err = f.svc.ApproveExpansion(t.Context(), f.p, "cloud", e.ID, current.Revision); err != nil {
					t.Fatalf("current approval: %v", err)
				}
			} else {
				policy.Limits.CPUMillis = 2000
				if _, err = f.svc.SaveCapacityPolicy(t.Context(), f.p, "cloud", cloudinfra.CapacityPolicyInput{Policy: *policy, ExpectedRevision: 2}); err != nil {
					t.Fatal(err)
				}
			}
			reconcileExpansion(t, f)
			reconcileExpansion(t, f)
			got, err := f.store.GetExpansion(t.Context(), "org", "cloud", e.ID)
			if err != nil || got.OperationID != original.ID || f.provider.creates != 1 {
				t.Fatalf("same-operation resume: %+v %v creates=%d", got, err, f.provider.creates)
			}
			resumed, err := f.store.GetOperation(t.Context(), "org", "cloud", e.OperationID)
			if err != nil || resumed.Principal != original.Principal || resumed.Input != original.Input || resumed.Review != original.Review {
				t.Fatalf("accepted identity changed: %+v %v", resumed, err)
			}
		})
	}
}

type expansionBootstrapBarrier struct {
	*infraSecrets
	beforePut func() error
}

func (s *expansionBootstrapBarrier) Put(ctx context.Context, org, id string, raw []byte) error {
	if s.beforePut != nil {
		hook := s.beforePut
		s.beforePut = nil
		if err := hook(); err != nil {
			return err
		}
	}
	return s.infraSecrets.Put(ctx, org, id, raw)
}

func TestExpansionFirstAttemptPolicyBoundary(t *testing.T) {
	for _, approval := range []bool{false, true} {
		t.Run(map[bool]string{false: "ceiling", true: "approval"}[approval], func(t *testing.T) {
			f, _, d := expansionFixture(t, false, 0)
			e := ensureExpansion(t, f, d)
			f.svc.Secrets = &expansionBootstrapBarrier{infraSecrets: f.secrets, beforePut: func() error {
				policy, err := f.store.GetCapacityPolicy(t.Context(), "org", "cloud")
				if err != nil {
					return err
				}
				if approval {
					policy.RequireExpansionApproval = true
				} else {
					policy.Limits.CPUMillis = 1000
				}
				_, err = f.svc.SaveCapacityPolicy(t.Context(), f.p, "cloud", cloudinfra.CapacityPolicyInput{Policy: *policy, ExpectedRevision: policy.Revision})
				return err
			}}
			reconcileExpansion(t, f)
			o, err := f.store.GetOperation(t.Context(), "org", "cloud", e.OperationID)
			if err != nil || o.Attempted || f.provider.creates != 0 {
				t.Fatalf("policy changed before attempt: attempted=%v creates=%d err=%v", o.Attempted, f.provider.creates, err)
			}
			var count int
			if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_cloud_capacity_reservations WHERE id=$1 AND status='reserved'`, e.OperationID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("first-attempt reservation lost: %d %v", count, err)
			}
		})
	}
}

func TestExpansionFirstAttemptConnectionBoundary(t *testing.T) {
	f, _, d := expansionFixture(t, false, 0)
	e := ensureExpansion(t, f, d)
	f.svc.Secrets = &expansionBootstrapBarrier{infraSecrets: f.secrets, beforePut: func() error {
		conn, err := f.connections.GetConnection(t.Context(), "org", "infra-account")
		if err != nil {
			return err
		}
		return f.connections.WithCredentialReplacement(t.Context(), conn, func() error {
			if putErr := f.secrets.Put(t.Context(), "org", conn.ID, []byte("dop_rotated_same_account")); putErr != nil {
				return putErr
			}
			conn.Status = "ready"
			conn.Message = ""
			conn.UpdatedAt = time.Now().UTC()
			return f.connections.UpdateConnection(t.Context(), conn)
		})
	}}
	reconcileExpansion(t, f)
	o, err := f.store.GetOperation(t.Context(), "org", "cloud", e.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if o.Attempted || f.provider.creates != 0 {
		t.Fatalf("rotated connection used: attempted=%v creates=%d", o.Attempted, f.provider.creates)
	}
	assertExpansionReservation(t, f, e.OperationID, 1)
}
func assertExpansionReservation(t *testing.T, f *infraFixture, operation string, want int) {
	t.Helper()
	var count int
	if err := f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_cloud_capacity_reservations WHERE id=$1 AND status='reserved'`, operation).Scan(&count); err != nil || count != want {
		t.Fatalf("retained reservation: got=%d want=%d err=%v", count, want, err)
	}
}
func minimumExpansion(t *testing.T, f *infraFixture) *cloudinfra.Expansion {
	t.Helper()
	var id string
	if err := f.sql.QueryRow(t.Context(), `SELECT id FROM loomplane_cloud_expansions WHERE body->>'minimum'='true' ORDER BY created_at,id LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	e, err := f.store.GetExpansion(t.Context(), "org", "cloud", id)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func lowerExpansionMinimum(t *testing.T, f *infraFixture) {
	t.Helper()
	policy, err := f.store.GetCapacityPolicy(t.Context(), "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	policy.Pools[0].MinServers = 0
	if _, err = f.svc.SaveCapacityPolicy(t.Context(), f.p, "cloud", cloudinfra.CapacityPolicyInput{Policy: *policy, ExpectedRevision: policy.Revision}); err != nil {
		t.Fatal(err)
	}
}
func TestExpansionObsoleteMinimum(t *testing.T) {
	for _, approval := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic", true: "approval"}[approval], func(t *testing.T) {
			f, _, _ := expansionFixture(t, approval, 1)
			reconcileExpansion(t, f)
			original := minimumExpansion(t, f)
			lowerExpansionMinimum(t, f)
			reconcileExpansion(t, f)
			got := minimumExpansion(t, f)
			if f.provider.creates != 0 {
				t.Fatalf("obsolete minimum created %d hosts", f.provider.creates)
			}
			if approval {
				if got.Status != "cancelled" {
					t.Fatalf("obsolete proposal status=%s", got.Status)
				}
				assertExpansionReservation(t, f, original.OperationID, 0)
			} else {
				if got.Status != "blocked" || got.OperationID != original.OperationID {
					t.Fatalf("accepted minimum identity/status: %+v", got)
				}
				assertExpansionReservation(t, f, original.OperationID, 1)
			}
		})
	}
}
func addExpansionPoolHost(t *testing.T, f *infraFixture) {
	t.Helper()
	now := time.Now().UTC()
	if err := f.connections.CreateConnection(t.Context(), &connections.Connection{ID: "other-runtime", OrgID: "org", Cloud: "server", Name: "Other host", Account: "other", Details: map[string]string{"docker_id": "other", "architecture": "amd64"}, Status: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	v, record := ledgerHost(t, f, "other-server", 2000)
	v.RuntimeConnectionID = "other-runtime"
	v.Health = cloudinfra.RuntimeHealth{DockerID: "other", Architecture: "amd64", EffectiveCPUMillis: 2000, EffectiveMemoryBytes: 2 << 30, CheckedAt: now}
	if err := f.store.SaveServer(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	record.ServerStateRevision = v.StateRevision
	ledgerSave(t, f, []cloudinfra.CapacityRecord{record}, nil)
	pool, err := f.store.GetPool(t.Context(), "org", "cloud", "pool")
	if err != nil {
		t.Fatal(err)
	}
	previous := pool.Revision
	pool.Revision++
	pool.Members = append(pool.Members, cloudinfra.PoolMember{ServerID: v.ID, CPUMillis: 2000, MemoryBytes: 2 << 30})
	if err = f.store.SavePool(t.Context(), pool, previous); err != nil {
		t.Fatal(err)
	}
}
func TestExpansionMinimumSatisfiedBeforeApproval(t *testing.T) {
	f, _, _ := expansionFixture(t, true, 1)
	reconcileExpansion(t, f)
	e := minimumExpansion(t, f)
	addExpansionPoolHost(t, f)
	if _, err := f.svc.ApproveExpansion(t.Context(), f.p, "cloud", e.ID, e.Revision); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("obsolete approval error=%v", err)
	}
	reconcileExpansion(t, f)
	got := minimumExpansion(t, f)
	if got.Status != "cancelled" || f.provider.creates != 0 {
		t.Fatalf("satisfied minimum: status=%s creates=%d", got.Status, f.provider.creates)
	}
}
func TestExpansionMinimumWithWorkload(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "revoked"}[revoked], func(t *testing.T) {
			f, authority, d := expansionFixture(t, true, 1)
			reconcileExpansion(t, f)
			e := minimumExpansion(t, f)
			shared := ensureExpansion(t, f, d)
			if shared.ID != e.ID {
				t.Fatal("minimum host was not shared")
			}
			lowerExpansionMinimum(t, f)
			reconcileExpansion(t, f)
			e = minimumExpansion(t, f)
			authority.revoked = revoked
			_, err := f.svc.ApproveExpansion(t.Context(), f.p, "cloud", e.ID, e.Revision)
			if revoked {
				if !errors.Is(err, security.ErrForbidden) {
					t.Fatalf("revoked attached demand approved: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			reconcileExpansion(t, f)
			want := 1
			if revoked {
				want = 0
			}
			if f.provider.creates != want {
				t.Fatalf("attached workload creates=%d want=%d", f.provider.creates, want)
			}
			var count int
			if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_cloud_expansion_demands WHERE expansion_id=$1`, e.ID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("demand lost: %d %v", count, err)
			}
		})
	}
}
func TestExpansionMinimumChangesDuringBootstrap(t *testing.T) {
	for _, host := range []bool{false, true} {
		t.Run(map[bool]string{false: "policy", true: "host"}[host], func(t *testing.T) {
			f, _, _ := expansionFixture(t, false, 1)
			reconcileExpansion(t, f)
			e := minimumExpansion(t, f)
			f.svc.Secrets = &expansionBootstrapBarrier{infraSecrets: f.secrets, beforePut: func() error {
				if host {
					addExpansionPoolHost(t, f)
				} else {
					lowerExpansionMinimum(t, f)
				}
				return nil
			}}
			reconcileExpansion(t, f)
			o, err := f.store.GetOperation(t.Context(), "org", "cloud", e.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			if o.Attempted || f.provider.creates != 0 {
				t.Fatalf("obsolete minimum attempted=%v creates=%d", o.Attempted, f.provider.creates)
			}
			assertExpansionReservation(t, f, e.OperationID, 1)
		})
	}
}

type expansionContextBlocker struct {
	expansionProvider
	cancel  context.CancelFunc
	failure error
}

func (p *expansionContextBlocker) ServerOptions(ctx context.Context, c provider.InfrastructureCredential) (provider.ServerOptions, error) {
	if c.Token == "dop_v1_fixture" {
		if p.failure != nil {
			return provider.ServerOptions{}, p.failure
		}
		p.cancel()
		<-ctx.Done()
		return provider.ServerOptions{}, ctx.Err()
	}
	return p.expansionProvider.ServerOptions(ctx, c)
}
func TestExpansionRecoveryFairness(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "canceled", true: "failed"}[failure], func(t *testing.T) { testExpansionRecoveryFairness(t, failure) })
	}
}
func testExpansionRecoveryFairness(t *testing.T, failure bool) {
	t.Helper()
	f, _, _ := expansionFixture(t, false, 1)
	pool := &cloudinfra.Pool{ID: "pool-z", OrgID: "org", CloudID: "cloud", Name: "Second", Revision: 1}
	if err := f.store.SavePool(t.Context(), pool, 0); err != nil {
		t.Fatal(err)
	}
	conn, err := f.connections.GetConnection(t.Context(), "org", "infra-account")
	if err != nil {
		t.Fatal(err)
	}
	conn.ID = "infra-second"
	conn.Name = "Second infrastructure account"
	if err = f.connections.CreateConnection(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	if err = f.secrets.Put(t.Context(), "org", conn.ID, []byte("dop_second")); err != nil {
		t.Fatal(err)
	}
	policy, err := f.store.GetCapacityPolicy(t.Context(), "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	second := policy.Pools[0]
	second.PoolID = pool.ID
	second.ConnectionID = conn.ID
	policy.Pools = append(policy.Pools, second)
	if _, err = f.svc.SaveCapacityPolicy(t.Context(), f.p, "cloud", cloudinfra.CapacityPolicyInput{Policy: *policy, ExpectedRevision: policy.Revision}); err != nil {
		t.Fatal(err)
	}
	adapter := &expansionContextBlocker{expansionProvider: expansionProvider{f.provider}}
	if failure {
		adapter.failure = errors.New("fixture options unavailable")
	}
	adapters := cloudinfra.NewAdapters()
	if err = adapters.RegisterServer("digitalocean", adapter); err != nil {
		t.Fatal(err)
	}
	f.svc.Adapters = adapters
	for range 3 {
		ctx, cancel := context.WithCancel(t.Context())
		adapter.cancel = cancel
		err = f.svc.ReconcileExpansions(ctx)
		cancel()
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, adapter.failure) {
			t.Fatal(err)
		}
	}
	var count int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_cloud_expansions WHERE pool_id='pool-z' AND operation_id IS NOT NULL`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 || f.provider.creates != 1 {
		t.Fatalf("later pool starved: expansions=%d creates=%d", count, f.provider.creates)
	}
}
