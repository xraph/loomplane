//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	spiretypes "github.com/spiffe/spire-api-sdk/proto/spire/api/types"
	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/grove/drivers/pgdriver"
	pgmigrate "github.com/xraph/grove/drivers/pgdriver/pgmigrate"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type workloadJournalIssuer struct {
	workloadidentity.EnrollmentIssuer
	calls, deletes int
	fail           bool
	onCreate       func()
}

func (i *workloadJournalIssuer) RegisterWorkload(_ context.Context, b workloadidentity.WorkloadBinding) (*spiretypes.Entry, error) {
	i.calls++
	if i.onCreate != nil {
		i.onCreate()
	}
	if i.fail {
		return nil, errors.New("response lost")
	}
	return &spiretypes.Entry{Id: b.Binding.EntryID}, nil
}
func (i *workloadJournalIssuer) Revoke(context.Context, string) error { i.deletes++; return nil }

func workloadJournalFixture(t *testing.T) (*pgdriver.PgDB, *workloadidentity.WorkloadStore, workloadidentity.WorkloadIntent) {
	t.Helper()
	db, _, d, _ := installationDatabase(t)
	ca := strings.Repeat("a", 64)
	d.Identity.Account = ca
	raw := `{"provider":"kubernetes","account":"` + ca + `","resource_id":"ns-uid","namespace_uid":"ns-uid"}`
	if _, err := db.Exec(t.Context(), `UPDATE loomplane_federation_destinations SET identity=$1,identity_revision=$2 WHERE id='d'`, raw, d.Identity.Revision()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `UPDATE loomplane_cloud_connections SET details=jsonb_build_object('cluster_ca',$1::text,'namespace_uid','ns-uid','namespace','apps') WHERE id='c'`, ca); err != nil {
		t.Fatal(err)
	}
	r := kube.ControllerReceipt{Scope: d.Scope, ConnectionID: "c", InstanceID: id.New(id.PrefixInstance).String(), PlanID: "plan", RuntimeRef: "attempt", ServiceID: "api", Container: "main", Image: "registry.example/api@sha256:" + strings.Repeat("b", 64), Namespace: "apps", NamespaceUID: "ns-uid", ClusterCA: ca, ServiceAccount: "default", DeploymentUID: "deployment-uid", DeploymentGeneration: 1, RuntimeRevision: "release"}
	if err := kube.NewControllerStore(db).RecordController(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO loomplane_bindings(id,org_id,project_id,environment_id,subject,name,provider,engine,external_id,connection_id,revision,plan_id,created_at,updated_at) VALUES('binding','o','p','e','service','api','kubernetes','ctrlplane',$1,'c',$2,'plan',now(),now())`, r.InstanceID, r.Image); err != nil {
		t.Fatal(err)
	}
	b := workloadidentity.Binding{Destination: *d, Method: "kubernetes", Namespace: "apps", NamespaceUID: "ns-uid", ServiceAccount: "default", PodUID: "pod-uid", Container: "main", ParentURI: "spiffe://example.test/spire/agent/k8s_psat/cluster/node", TTL: time.Minute, CheckedAt: time.Now()}
	in := workloadidentity.WorkloadIntent{BindingID: "binding", Controller: r, Workload: workloadidentity.WorkloadBinding{Binding: b, Workload: domain.WorkloadSelector{Scope: d.Scope, DestinationID: d.ID, ServiceID: "api"}, ContainerImage: r.Image, PruneAfter: time.Now().Add(2 * time.Minute).Truncate(time.Second)}}
	return db, workloadidentity.NewWorkloadStore(db, "example.test"), in
}
func allowWorkload(context.Context) error { return nil }

func TestWorkloadJournalRecoveryAndRetention(t *testing.T) {
	db, s, in := workloadJournalFixture(t)
	ctx := t.Context()
	r, err := s.Reserve(ctx, in, allowWorkload)
	if err != nil {
		t.Fatal(err)
	}
	reopened := workloadidentity.NewWorkloadStore(db, "example.test")
	got, err := reopened.Get(ctx, "o", r.ID)
	if err != nil || got.Phase != "pending" || got.Intent.Workload.Binding.EntryID == "" {
		t.Fatal("durable intent missing", got, err)
	}
	if _, err = reopened.Get(ctx, "other", r.ID); !errors.Is(err, workloadidentity.ErrIdentity) {
		t.Fatal("foreign journal read", err)
	}
	issuer := &workloadJournalIssuer{}
	for range 2 {
		if err = reopened.Issue(ctx, "o", r.ID, issuer, allowWorkload); err != nil {
			t.Fatal(err)
		}
	}
	if issuer.calls != 1 {
		t.Fatal("repeated create", issuer.calls)
	}
	if err = reopened.Revoke(ctx, "o", r.ID, issuer); err != nil {
		t.Fatal(err)
	}
	got, err = reopened.Get(ctx, "o", r.ID)
	if err != nil || got.Phase != "draining" || got.ReleaseAfter.Before(time.Now().Add(4*time.Minute)) {
		t.Fatal("certificate/policy drain missing", got, err)
	}
	for _, query := range []string{`DELETE FROM loomplane_federation_destinations WHERE id='d'`, `DELETE FROM loomplane_workload_controllers`, `DELETE FROM loomplane_cloud_connections WHERE id='c'`} {
		if _, err = db.Exec(ctx, query); err == nil {
			t.Fatal("parent released before drain", query)
		}
	}
	if err = reopened.Issue(ctx, "o", r.ID, issuer, allowWorkload); !errors.Is(err, workloadidentity.ErrIdentity) {
		t.Fatal("revoked registration issued", err)
	}
	result, err := reopened.Cleanup(ctx, "o", 20, issuer)
	if err != nil || result.Deleted != 0 {
		t.Fatal("drain released early", result, err)
	}
	if _, err = db.Exec(ctx, `UPDATE loomplane_workload_registrations SET release_after=clock_timestamp()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	result, err = reopened.Cleanup(ctx, "o", 20, issuer)
	if err != nil || result.Deleted != 1 {
		t.Fatal("drain did not finish", result, err)
	}
	if _, err = reopened.Get(ctx, "o", r.ID); !errors.Is(err, workloadidentity.ErrIdentity) {
		t.Fatal("journal retained after completed cleanup", err)
	}
	if _, err = db.Exec(ctx, `DELETE FROM loomplane_workload_controllers`); err != nil {
		t.Fatal("controller retention not released", err)
	}
}

func TestWorkloadJournalUnknownCreateNeverReissuesOrReleases(t *testing.T) {
	db, s, in := workloadJournalFixture(t)
	ctx := t.Context()
	r, err := s.Reserve(ctx, in, allowWorkload)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &workloadJournalIssuer{fail: true}
	if err = s.Issue(ctx, "o", r.ID, issuer, allowWorkload); err == nil {
		t.Fatal("unknown outcome accepted")
	}
	reopened := workloadidentity.NewWorkloadStore(db, "example.test")
	if err = reopened.Issue(ctx, "o", r.ID, issuer, allowWorkload); !errors.Is(err, workloadidentity.ErrWorkloadUncertain) || issuer.calls != 1 {
		t.Fatal("unknown create repeated", err, issuer.calls)
	}
	if err = reopened.Revoke(ctx, "o", r.ID, issuer); !errors.Is(err, workloadidentity.ErrWorkloadUncertain) {
		t.Fatal("unknown issuance declared revoked", err)
	}
	if _, err = db.Exec(ctx, `UPDATE loomplane_workload_registrations SET next_attempt=clock_timestamp()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	result, err := reopened.Cleanup(ctx, "o", 20, issuer)
	if !errors.Is(err, workloadidentity.ErrWorkloadUncertain) || result.Deleted != 0 || issuer.deletes != 2 {
		t.Fatal("unknown intent discarded", result, err)
	}
	got, err := reopened.Get(ctx, "o", r.ID)
	if err != nil || got.Phase != "revoking" || got.Settled {
		t.Fatal("unknown create lost", got, err)
	}
	if _, err = db.Exec(ctx, `DELETE FROM loomplane_workload_controllers`); err == nil {
		t.Fatal("unknown creation lost receipt")
	}
}

func TestWorkloadJournalRechecksAuthorityAndOwnership(t *testing.T) {
	db, s, in := workloadJournalFixture(t)
	ctx := t.Context()
	denied := func(context.Context) error { return errors.New("denied") }
	if _, err := s.Reserve(ctx, in, denied); err == nil {
		t.Fatal("unauthorized reservation")
	}
	r, err := s.Reserve(ctx, in, allowWorkload)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &workloadJournalIssuer{}
	if err = s.Issue(ctx, "o", r.ID, issuer, denied); err == nil || issuer.calls != 0 {
		t.Fatal("unauthorized create", err)
	}
	if _, err = db.Exec(ctx, `UPDATE loomplane_bindings SET drift='changed' WHERE id='binding'`); err != nil {
		t.Fatal(err)
	}
	if err = s.Issue(ctx, "o", r.ID, issuer, allowWorkload); err == nil || issuer.calls != 0 {
		t.Fatal("changed deployment issued", err)
	}
	result, err := s.Cleanup(ctx, "o", 20, issuer)
	if err != nil || result.Attempted != 1 {
		t.Fatal("stale binding not cleaned", result, err)
	}
}

func TestWorkloadJournalRevokeRacingCreateRetainsLateResult(t *testing.T) {
	_, s, in := workloadJournalFixture(t)
	ctx := t.Context()
	r, err := s.Reserve(ctx, in, allowWorkload)
	if err != nil {
		t.Fatal(err)
	}
	revoker := &workloadJournalIssuer{}
	issuer := &workloadJournalIssuer{onCreate: func() {
		if revokeErr := s.Revoke(ctx, "o", r.ID, revoker); !errors.Is(revokeErr, workloadidentity.ErrWorkloadUncertain) {
			t.Error("inflight create released", revokeErr)
		}
	}}
	if err = s.Issue(ctx, "o", r.ID, issuer, allowWorkload); !errors.Is(err, workloadidentity.ErrIdentity) {
		t.Fatal("late create became ready", err)
	}
	got, err := s.Get(ctx, "o", r.ID)
	if err != nil || !got.Settled || got.Phase != "revoking" {
		t.Fatal("late response not retained for another delete", got, err)
	}
	if err = s.Revoke(ctx, "o", r.ID, revoker); err != nil || revoker.deletes != 2 {
		t.Fatal("late creation not deleted", err)
	}
}

func TestWorkloadJournalAuditFailureAndMigration(t *testing.T) {
	db, s, in := workloadJournalFixture(t)
	ctx := t.Context()
	for _, query := range []string{
		`CREATE FUNCTION reject_workload_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture event unavailable'; END $$`,
		`CREATE TRIGGER reject_workload_event BEFORE INSERT ON loomplane_events FOR EACH ROW WHEN (NEW.type LIKE 'workload.registration.%') EXECUTE FUNCTION reject_workload_event()`,
	} {
		if _, err := db.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Reserve(ctx, in, allowWorkload); err == nil {
		t.Fatal("failed audit committed reservation")
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM loomplane_workload_registrations`).Scan(&count); err != nil || count != 0 {
		t.Fatal("reservation survived failed audit", count, err)
	}
	if _, err := db.Exec(ctx, `DROP TRIGGER reject_workload_event ON loomplane_events`); err != nil {
		t.Fatal(err)
	}
	r, err := s.Reserve(ctx, in, allowWorkload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `CREATE TRIGGER reject_workload_event BEFORE INSERT ON loomplane_events FOR EACH ROW WHEN (NEW.type='workload.registration.issuing') EXECUTE FUNCTION reject_workload_event()`); err != nil {
		t.Fatal(err)
	}
	issuer := &workloadJournalIssuer{}
	if err = s.Issue(ctx, "o", r.ID, issuer, allowWorkload); err == nil || issuer.calls != 0 {
		t.Fatal("failed claim audit reached SPIRE", err)
	}
	if _, err = db.Exec(ctx, `DROP TRIGGER reject_workload_event ON loomplane_events`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `CREATE TRIGGER reject_workload_event BEFORE INSERT ON loomplane_events FOR EACH ROW WHEN (NEW.type='workload.registration.ready') EXECUTE FUNCTION reject_workload_event()`); err != nil {
		t.Fatal(err)
	}
	if err = s.Issue(ctx, "o", r.ID, issuer, allowWorkload); err == nil || issuer.calls != 1 {
		t.Fatal("failed result audit accepted", err)
	}
	got, err := s.Get(ctx, "o", r.ID)
	if err != nil || got.Phase != "issuing" || got.Settled {
		t.Fatal("lost result failed to retain uncertainty", got, err)
	}
	m := federationMigration(t, "20261025000400")
	if err = m.Up(ctx, pgmigrate.New(db)); err != nil {
		t.Fatal("migration replay", err)
	}
	if err = m.Down(ctx, pgmigrate.New(db)); err == nil {
		t.Fatal("downgrade discarded unknown registration")
	}
	if _, err = db.Exec(ctx, `DELETE FROM loomplane_federation_destinations WHERE id='d'`); err == nil {
		t.Fatal("failed downgrade lost retention")
	}
}

func TestWorkloadJournalConcurrentClaim(t *testing.T) {
	_, s, in := workloadJournalFixture(t)
	ctx := t.Context()
	r, err := s.Reserve(ctx, in, allowWorkload)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	issuer := &workloadJournalIssuer{onCreate: func() { close(entered); <-release }}
	result := make(chan error, 1)
	go func() { result <- s.Issue(ctx, "o", r.ID, issuer, allowWorkload) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("create did not start")
	}
	denied := s.Issue(ctx, "o", r.ID, &workloadJournalIssuer{}, allowWorkload)
	close(release)
	if !errors.Is(denied, workloadidentity.ErrWorkloadUncertain) {
		t.Fatal("concurrent claim accepted", denied)
	}
	if err = <-result; err != nil || issuer.calls != 1 {
		t.Fatal("claimed create failed", err)
	}
}

func TestWorkloadJournalImageProof(t *testing.T) {
	_, s, in := workloadJournalFixture(t)
	raw := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:` + strings.Repeat("b", 64) + `","size":1}]}`)
	digest := sha256.Sum256(raw)
	in.Workload.ContainerImage = "runtime.example/api@sha256:" + hex.EncodeToString(digest[:])
	in.OperatingSystem, in.Architecture = "linux", "arm64"
	if _, err := s.Reserve(t.Context(), in, allowWorkload); err == nil {
		t.Fatal("unproven runtime digest accepted")
	}
	in.ImageIndexes = [][]byte{raw}
	if _, err := s.Reserve(t.Context(), in, allowWorkload); err != nil {
		t.Fatal("proven runtime wrapper rejected", err)
	}
	in.ImageIndexes[0] = append(in.ImageIndexes[0], ' ')
	if _, err := s.Reserve(t.Context(), in, allowWorkload); err == nil {
		t.Fatal("changed proof bytes accepted")
	}
}
