//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	"github.com/xraph/grove/driver"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/cloudinfra"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
)

func TestManagedWorkloadJournalRealPending(t *testing.T) {
	path := os.Getenv("LOOMPLANE_TEST_MANAGED_KUBECONFIG")
	if path == "" {
		t.Skip("requires disposable managed kind cluster")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"guaranteed", "best_effort"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			namespace := "journal-" + uuid.NewString()[:8]
			cfg, err := kube.ParseConfig(raw, map[string]string{"namespace": namespace})
			if err != nil {
				t.Fatal(err)
			}
			admin, err := kube.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			ns, err := admin.Kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, metav1.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = admin.Kube.CoreV1().Namespaces().Delete(context.Background(), namespace, metav1.DeleteOptions{})
			}()
			ca := cfg.Details["cluster_ca"]
			f, svc, plan, result, _ := managedDemandFixture(t, map[string]string{"namespace": namespace, "namespace_uid": string(ns.UID), "cluster_ca": ca})
			result.Actions[0].Service.CapacityMode = mode
			if err = svc.PrepareClusterWorkloadDemands(ctx, plan, result); err != nil {
				t.Fatal(err)
			}
			saveManagedDemandPlan(t, f, plan, result)
			err = svc.ResolveCapacityIntents(ctx, "org", plan.ID)
			var blocked *cloudinfra.CapacityAdmissionError
			if !errors.As(err, &blocked) || blocked.Block.Reason != "managed_workload_execution_unqualified" {
				t.Fatalf("activation gate changed: %v", err)
			}
			if _, err = f.sql.Exec(ctx, `INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at) VALUES('journal-deployment','org','proj','env','production',$1,'reviewed','running','usr',now())`, plan.ID); err != nil {
				t.Fatal(err)
			}
			attempt, err := f.store.BeginWorkloadAttempt(ctx, cloudinfra.WorkloadAttempt{Org: "org", Project: "proj", Environment: "env", Service: "api", Plan: plan.ID, Deployment: "journal-deployment"}, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			demand := result.Actions[0].Service.ClusterDemand
			if mode == "guaranteed" {
				if err = workloadTx(t, f, func(tx driver.Tx) error { return f.store.AttachClusterWorkloadAttempt(ctx, tx, demand.Input, *attempt) }); err != nil {
					t.Fatal(err)
				}
			}
			iid := id.New(id.PrefixInstance)
			scope := kube.ManagedScope{Org: "org", Project: "proj", Environment: "env", Service: "api", Plan: plan.ID, Deployment: attempt.Deployment, Action: "service:api", Connection: "cluster-runtime", Instance: iid.String(), RuntimeRef: "org/env/api/runtime_journal", RuntimeRevision: attempt.Deployment + "/service:api", Namespace: namespace, NamespaceUID: string(ns.UID), ClusterCA: ca, Epoch: attempt.Generation}
			scope.Release = kube.WorkloadReleaseID(scope.RuntimeRef, scope.RuntimeRevision)
			journal := kube.NewControllerStore(f.db)
			if err = journal.OpenManagedScope(ctx, scope); err != nil {
				t.Fatal(err)
			}
			cfg.ManagedWorkload, cfg.ManagedJournal, cfg.ManagedScope = true, journal, scope
			cfg.Details["namespace_uid"] = scope.NamespaceUID
			client, err := kube.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			runtime := &kube.Runtime{Session: func(context.Context, map[string]string) (*kube.Client, error) { return client, nil }, RecordController: journal.RecordController, Wait: time.Nanosecond, Values: func(context.Context, map[string]string) (owned.RuntimeValues, error) {
				return owned.RuntimeValues{Env: map[string]string{"TOKEN": "never-persist-raw-secret"}}, nil
			}}
			labels := map[string]string{owned.LabelOrg: scope.Org, owned.LabelProject: scope.Project, owned.LabelEnvironment: scope.Environment, owned.LabelService: scope.Service, owned.LabelPlan: scope.Plan, owned.LabelConnection: scope.Connection, owned.LabelAction: scope.Action, owned.LabelRuntime: scope.RuntimeRef, owned.LabelRevision: scope.RuntimeRevision, owned.LabelManageHPA: "true", owned.LabelScaleMin: "1", owned.LabelScaleMax: "4"}
			// The existing runtime sends the Deployment and persists its mutation receipt
			// before readiness. No readiness callback or capacity gate override is used.
			_, err = runtime.Apply(ctx, iid, cpprovider.ServiceSpec{Name: "api", Image: "registry.invalid/api@sha256:" + strings.Repeat("a", 64), Resources: cpprovider.ResourceSpec{CPUMillis: 500, MemoryMB: 128}}, labels)
			if err == nil {
				t.Fatal("unpullable Pending workload unexpectedly ready")
			}
			t.Logf("runtime readiness result: %v", err)
			deployment, err := admin.Kube.AppsV1().Deployments(namespace).Get(ctx, kube.WorkloadName(iid), metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if deployment.Status.AvailableReplicas != 0 {
				t.Fatal("expected Pending deployment")
			}
			receipt, err := journal.GetController(ctx, scope.Org, scope.Connection, scope.Instance, scope.RuntimeRef)
			if err != nil {
				t.Fatal(err)
			}
			if receipt.ReleaseID != scope.Release || receipt.DeploymentUID != string(deployment.UID) {
				t.Fatal("physical receipt not bound to exact release")
			}
			entries, err := journal.ManagedIssuances(ctx, scope.Org, scope.Release)
			if err != nil || len(entries) < 3 {
				t.Fatalf("issuance journal: %d %v", len(entries), err)
			}
			for _, entry := range entries {
				if entry.Outcome == nil || entry.Outcome.State != "accepted" || entry.Scope != scope {
					t.Fatalf("unbound accepted outcome: %+v", entry)
				}
			}
			encoded, _ := json.Marshal(entries)
			if strings.Contains(string(encoded), "never-persist-raw-secret") || strings.Contains(string(encoded), "TOKEN") {
				t.Fatal("journal leaked runtime body")
			}
			var holds int
			if err = f.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_cluster_workload_holds WHERE plan_id=$1`, plan.ID).Scan(&holds); err != nil {
				t.Fatal(err)
			}
			if (mode == "guaranteed" && holds != 1) || (mode == "best_effort" && holds != 0) {
				t.Fatalf("hold mode %s count %d", mode, holds)
			}
			request := entries[0].Request
			unresolved, err := journal.Issue(ctx, scope, request)
			if err != nil {
				t.Fatal(err)
			}
			delayed, err := journal.Issue(ctx, scope, request)
			if err != nil {
				t.Fatal(err)
			}
			oldScope := scope
			next, err := f.store.BeginWorkloadAttempt(ctx, *attempt, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "guaranteed" {
				if err = workloadTx(t, f, func(tx driver.Tx) error { return f.store.AttachClusterWorkloadAttempt(ctx, tx, demand.Input, *next) }); err != nil {
					t.Fatal(err)
				}
			}
			scope.Epoch = next.Generation
			if err = journal.OpenManagedScope(ctx, scope); err != nil {
				t.Fatal(err)
			}
			if _, err = journal.Issue(ctx, oldScope, request); err == nil {
				t.Fatal("stale attempt issued after takeover")
			}
			if err = journal.RecordController(kube.WithManagedScope(ctx, oldScope), *receipt); err == nil {
				t.Fatal("stale attempt published after takeover")
			}
			if err = journal.RevokeManagedScope(ctx, scope); err != nil {
				t.Fatal(err)
			}
			if _, err = journal.Issue(ctx, scope, request); err == nil {
				t.Fatal("issuance after revocation")
			}
			if err = journal.OpenManagedScope(ctx, scope); err == nil {
				t.Fatal("revoked release reopened")
			}
			if err = journal.RecordController(kube.WithManagedScope(ctx, scope), *receipt); err == nil {
				t.Fatal("late receipt revived authority")
			}
			if err = journal.Resolve(ctx, delayed, oldScope, *entries[0].Outcome); err != nil {
				t.Fatal("accepted delayed outcome lost", err)
			}
			authority, err := journal.GetManagedAuthority(ctx, scope.Org, scope.Release)
			if err != nil || authority.Scope != scope || authority.RevokedAt == nil {
				t.Fatalf("authority recovery: %+v %v", authority, err)
			}
			if err = f.store.FinishWorkloadAttempt(ctx, *next, true); err != nil {
				t.Fatal(err)
			}
			var settled bool
			if err = f.sql.QueryRow(ctx, `SELECT settled FROM loomplane_workload_attempts WHERE org_id='org' AND service='api'`).Scan(&settled); err != nil || settled {
				t.Fatalf("uncertain issuance settled: %t %v", settled, err)
			}
			saved, err := journal.ManagedIssuances(ctx, scope.Org, scope.Release)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range saved {
				if entry.ID == unresolved && entry.Outcome != nil {
					t.Fatal("unrelated success closed uncertain issuance")
				}
				if entry.ID == delayed && entry.Outcome == nil {
					t.Fatal("revocation discarded accepted outcome")
				}
			}
		})
	}
}
