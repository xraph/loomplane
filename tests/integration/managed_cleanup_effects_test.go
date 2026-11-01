//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	"github.com/xraph/grove/driver"
	"github.com/xraph/warden"
	"github.com/xraph/warden/store/memory"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/xraph/loomplane/internal/cloudinfra"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

func TestManagedCleanupEffectsRealAPI(t *testing.T) {
	t.Run("whole", func(t *testing.T) { managedCleanupEffectsRealAPI(t, false) })
	t.Run("shared", func(t *testing.T) { managedCleanupEffectsRealAPI(t, true) })
}
func managedCleanupEffectsRealAPI(t *testing.T, shared bool, modes ...string) {
	t.Helper()
	e := newEnrollmentServiceFixture(t)
	infra, creds := e.service(e.store, e.vault())
	if err := infra.ReconcileClusterOperation(t.Context(), "org", e.op.ID, nil); err != nil {
		t.Fatal(err)
	}
	attachment := e.attachment()
	f := &poolFixture{sql: e.sql, db: e.pg, store: e.store, plans: planning.NewPgStore(e.pg), clouds: e.clouds}
	for _, stmt := range []string{
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('env','org','proj','production','production',now(),now())`,
		`INSERT INTO loomplane_cloud_assignments(org_id,project_id,environment_id,cloud_id,revision) VALUES('org','proj','env','cloud',1)`,
		`INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) VALUES('pool-manifest-proj','org','proj',1,'version: 1','pool','usr',now())`,
	} {
		if _, err := f.sql.Exec(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	var target string
	if err := f.sql.QueryRow(t.Context(), `SELECT id FROM loomplane_cloud_targets WHERE connection_id=$1`, attachment.RuntimeConnectionID).Scan(&target); err != nil {
		t.Fatal(err)
	}
	resource, err := f.store.GetManagedResource(t.Context(), "org", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	if len(modes) == 1 && modes[0] == "legacy" {
		installedLegacyApplication(t, e, f, creds)
		return
	}
	var fixtureArchitectures []string
	if len(modes) > 1 && modes[0] == "application" {
		nodes, readErr := e.bootstrap.Kube.CoreV1().Nodes().List(t.Context(), metav1.ListOptions{})
		if readErr != nil || len(nodes.Items) != 1 {
			t.Fatal("dedicated kind node", readErr)
		}
		fixtureArchitectures = []string{nodes.Items[0].Status.NodeInfo.Architecture}
	}
	_, svc, plan, result, _ := setupDemandForAttachment(t, f, resource, target, attachment.RuntimeConnectionID, attachment.ID, fixtureArchitectures...)
	if len(modes) > 1 && modes[0] == "application" {
		installedApplication(t.Context(), t, e, f, creds, infra, svc, plan, result, modes[1])
		return
	}
	if err = svc.PrepareClusterWorkloadDemands(t.Context(), plan, result); err != nil {
		t.Fatal(err)
	}
	raw := e.handout(creds, attachment)
	cfg, err := kube.ParseConfig(raw, map[string]string{"namespace": attachment.Namespace})
	if err != nil {
		t.Fatal(err)
	}
	saveManagedDemandPlan(t, f, plan, result)
	svc.BindManagedRuntime(ledgerRuntimeFixture{})
	if err = svc.ResolveCapacityIntents(t.Context(), "org", plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at) VALUES('closure-runtime','org','proj','env','production',$1,'old','running','usr',now())`, plan.ID); err != nil {
		t.Fatal(err)
	}
	attempt, err := f.store.BeginWorkloadAttempt(t.Context(), cloudinfra.WorkloadAttempt{Org: "org", Project: "proj", Environment: "env", Service: "api", Plan: plan.ID, Deployment: "closure-runtime"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	input := result.Actions[0].Service.ClusterDemand.Input
	if err = workloadTx(t, f, func(tx driver.Tx) error {
		return f.store.AttachClusterWorkloadAttempt(t.Context(), tx, input, *attempt)
	}); err != nil {
		t.Fatal(err)
	}
	iid := id.New(id.PrefixInstance)
	scope := kube.ManagedScope{Org: "org", Project: "proj", Environment: "env", Service: "api", Plan: plan.ID, Deployment: attempt.Deployment, Action: "service:api", Connection: attachment.RuntimeConnectionID, Instance: iid.String(), RuntimeRef: "org/env/api/runtime_closure", RuntimeRevision: attempt.Deployment + "/service:api", Namespace: attachment.Namespace, NamespaceUID: attachment.NamespaceUID, ClusterCA: attachment.TrustFingerprint, Epoch: attempt.Generation}
	scope.Release = kube.WorkloadReleaseID(scope.RuntimeRef, scope.RuntimeRevision)
	journal := kube.NewControllerStore(f.db).WithClusterCapacity(true)
	if err = journal.OpenManagedScope(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	apply := func(journal *kube.ControllerStore, current kube.ManagedScope, image string, wait time.Duration) error {
		currentConfig := cfg
		currentConfig.ManagedWorkload, currentConfig.ManagedJournal, currentConfig.ManagedScope = true, journal, current
		currentConfig.Details["namespace_uid"] = current.NamespaceUID
		client, clientErr := kube.New(currentConfig)
		if clientErr != nil {
			return clientErr
		}
		runtime := &kube.Runtime{Session: func(context.Context, map[string]string) (*kube.Client, error) { return client, nil }, RecordController: journal.RecordController, Wait: wait}
		labels := map[string]string{owned.LabelOrg: current.Org, owned.LabelProject: current.Project, owned.LabelEnvironment: current.Environment, owned.LabelService: current.Service, owned.LabelPlan: current.Plan, owned.LabelConnection: current.Connection, owned.LabelAction: current.Action, owned.LabelRuntime: current.RuntimeRef, owned.LabelRevision: current.RuntimeRevision}
		_, applyErr := runtime.Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "api", Image: image, Resources: cpprovider.ResourceSpec{CPUMillis: 100, MemoryMB: 32}}, labels)
		return applyErr
	}
	// The disposable kind node already carries this pause image.
	if err = apply(journal, scope, "registry.k8s.io/pause@sha256:ee6521f290b2168b6e0935a181d4cff9be1ac3f505666ef0e3c98fae8199917a", 45*time.Second); err != nil {
		t.Fatal(err)
	}
	_, err = journal.GetController(t.Context(), scope.Org, scope.Connection, scope.Instance, scope.RuntimeRef)
	if err != nil {
		t.Fatal(err)
	}
	binding := &planning.Binding{ID: "closure-binding", OrgID: scope.Org, ProjectID: scope.Project, EnvironmentID: scope.Environment, Subject: planning.ServiceSubject, Name: scope.Service, Provider: "kubernetes", ExternalID: scope.Instance, PlanID: scope.Plan, ConnectionID: scope.Connection, Revision: "ready-A", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err = f.plans.PutBinding(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	if err = f.store.FinishWorkloadAttempt(t.Context(), *attempt, true); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_deployments SET status='succeeded' WHERE id=$1`, attempt.Deployment); err != nil {
		t.Fatal(err)
	}
	removal := &planning.Plan{ID: "closure-remove", OrgID: scope.Org, ProjectID: scope.Project, EnvironmentID: scope.Environment}
	reviewed := &planning.Result{Executable: true, Connections: map[string]string{}, Actions: []planning.Action{{ID: "service:api", Subject: planning.ServiceSubject, Name: "api", Op: planning.Delete, Provider: "kubernetes"}}}
	if shared {
		reviewed = result
		reviewed.Actions[0].Op = planning.Update
		reviewed.Actions[0].Service.CapacityMode = "best_effort"
		if err = svc.PrepareClusterWorkloadDemands(t.Context(), removal, reviewed); err != nil {
			t.Fatal(err)
		}
	}
	if err = svc.PrepareManagedCleanupTargets(t.Context(), removal, reviewed); err != nil {
		t.Fatal(err)
	}
	if len(reviewed.Actions[0].CleanupTargets) != 1 {
		t.Fatal("review did not capture runtime release")
	}
	saveCleanupReview(t, f, removal, reviewed)
	ctx := security.WithPrincipal(t.Context(), security.Principal{OrgID: "org", UserID: "reviewer"})
	if ok, approvalErr := f.plans.Decide(ctx, "org", removal.ID, "approved", "reviewer", "", time.Now()); approvalErr != nil || !ok {
		t.Fatalf("review approval: %v %v", ok, approvalErr)
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at) VALUES('closure-removal','org','proj','env','production',$1,'cleanup','running','reviewer',now())`, removal.ID); err != nil {
		t.Fatal(err)
	}
	var next kube.ManagedScope
	if shared {
		next = scope
		next.Plan, next.Deployment, next.RuntimeRef = removal.ID, "closure-removal", scope.RuntimeRef+"-new"
		run, beginErr := f.store.BeginWorkloadAttempt(t.Context(), cloudinfra.WorkloadAttempt{Org: next.Org, Project: next.Project, Environment: next.Environment, Service: next.Service, Plan: next.Plan, Deployment: next.Deployment}, nil, false)
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		next.Epoch = run.Generation
		next.RuntimeRevision = next.Deployment + "/" + next.Action
		next.Release = kube.WorkloadReleaseID(next.RuntimeRef, next.RuntimeRevision)
		if err = journal.OpenManagedScope(t.Context(), next); err != nil {
			t.Fatal(err)
		}
		if err = apply(journal, next, "registry.k8s.io/pause@sha256:ee6521f290b2168b6e0935a181d4cff9be1ac3f505666ef0e3c98fae8199917a", 45*time.Second); err != nil {
			t.Fatal(err)
		}
		binding.PlanID, binding.Revision = next.Plan, "ready-new"
		if err = f.plans.PutBinding(t.Context(), binding); err != nil {
			t.Fatal(err)
		}
	}
	auth := managedCleanupReviewer(t)
	cleanup := planning.NewManagedCleanupStore(f.db, auth, nil)

	selection := planning.ManagedCleanupSelection{Org: "org", Plan: removal.ID, Action: "service:api", Deployment: "closure-removal"}
	if err = cleanup.ExecuteCleanupEffects(ctx, selection, creds); !errors.Is(err, kube.ErrCleanupPermissionsUnavailable) {
		t.Fatalf("legacy role: %v", err)
	}
	op, err := infra.PreviewCluster(t.Context(), e.principal, "cloud", accessInput(e, attachment))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = infra.ApplyCluster(t.Context(), e.principal, "cloud", op.ID, cloudinfra.ApplyInput{Review: op.Digest}); err != nil {
		t.Fatal(err)
	}
	if err = infra.ReconcileRuntimeAccessOperation(t.Context(), "org", op.ID, nil); err != nil {
		t.Fatal(err)
	}
	enrollmentAuth := &runtimeAccessAuth{denied: e.principal.UserID}
	infra.Authz = enrollmentAuth
	if err = cleanup.ExecuteCleanupEffects(ctx, selection, creds); !errors.Is(err, cloudinfra.ErrRuntimeCredentialDelegationUnavailable) {
		t.Fatalf("revoked enrollment: %v", err)
	}
	enrollmentAuth.denied = ""
	wrong := security.WithPrincipal(ctx, security.Principal{OrgID: "org", UserID: "other"})
	if err = cleanup.ExecuteCleanupEffects(wrong, selection, creds); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("wrong operation actor: %v", err)
	}
	owner, err := e.bootstrap.Kube.AppsV1().Deployments(scope.Namespace).Get(ctx, kube.WorkloadName(iid), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(modes) > 0 {
		runReviewedCleanup(ctx, t, e, creds, cleanup, scope, selection, modes[0])
		return
	}
	attachmentOwner := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: owner.Name, UID: owner.UID}
	if shared {
		sets, readErr := e.bootstrap.Kube.AppsV1().ReplicaSets(scope.Namespace).List(ctx, metav1.ListOptions{})
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, set := range sets.Items {
			if set.Spec.Template.Annotations[kube.WorkloadReleaseAnnotation] == scope.Release {
				attachmentOwner.Kind, attachmentOwner.Name, attachmentOwner.UID = "ReplicaSet", set.Name, set.UID
			}
		}
		if attachmentOwner.Kind != "ReplicaSet" {
			t.Fatal("no old native ReplicaSet")
		}
	}
	foreignConfig, err := e.bootstrap.Kube.CoreV1().ConfigMaps(scope.Namespace).Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "unreviewed-config", OwnerReferences: []metav1.OwnerReference{attachmentOwner}}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if shared {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "lookalike", Namespace: scope.Namespace, Annotations: map[string]string{kube.WorkloadReleaseAnnotation: scope.Release, owned.LabelRevision: scope.RuntimeRevision}, Labels: map[string]string{owned.LabelInstance: scope.Instance, owned.LabelOrg: scope.Org, owned.LabelProject: scope.Project, owned.LabelEnvironment: scope.Environment}}, Spec: corev1.PodSpec{NodeSelector: map[string]string{"fixture": "absent"}, Containers: []corev1.Container{{Name: "api", Image: "registry.k8s.io/pause:3.10"}}}}
		foreign, createErr := e.bootstrap.Kube.CoreV1().Pods(scope.Namespace).Create(ctx, pod, metav1.CreateOptions{})
		if createErr != nil {
			t.Fatal(createErr)
		}
		if err = cleanup.ExecuteCleanupEffects(ctx, selection, creds); !errors.Is(err, kube.ErrManagedAuthority) {
			t.Fatalf("unowned pod: %v", err)
		}
		current, getErr := e.bootstrap.Kube.CoreV1().Pods(scope.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if getErr != nil || current.UID != foreign.UID || current.DeletionTimestamp != nil {
			t.Fatalf("unowned pod removed: %v", getErr)
		}
		uid := foreign.UID
		zero := int64(0)
		if err = e.bootstrap.Kube.CoreV1().Pods(scope.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero, Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
			t.Fatal(err)
		}
	}
	if err = wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 60*time.Second, true, func(call context.Context) (bool, error) {
		effectsErr := cleanup.ExecuteCleanupEffects(call, selection, creds)
		if errors.Is(effectsErr, planning.ErrCleanupConverging) {
			return false, nil
		}
		return effectsErr == nil, effectsErr
	}); err != nil {
		t.Fatal(err)
	}
	if err = wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 15*time.Second, true, func(call context.Context) (bool, error) {
		current, readErr := e.bootstrap.Kube.CoreV1().ConfigMaps(scope.Namespace).Get(call, foreignConfig.Name, metav1.GetOptions{})
		if readErr != nil {
			return false, readErr
		}
		if current.UID != foreignConfig.UID || current.DeletionTimestamp != nil {
			return false, errors.New("unreviewed dependent removed")
		}
		return len(current.OwnerReferences) == 0, nil
	}); err != nil {
		t.Fatal(err)
	}
	d, err := e.bootstrap.Kube.AppsV1().Deployments(attachment.Namespace).Get(ctx, kube.WorkloadName(iid), metav1.GetOptions{})
	resourceDeleted := "deployments"
	if shared {
		if err != nil || d.Spec.Template.Annotations[kube.WorkloadReleaseAnnotation] != next.Release || d.DeletionTimestamp != nil {
			t.Fatalf("shared deployment changed: %v", err)
		}
		resourceDeleted = "replicasets"
	} else {
		if err = wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 15*time.Second, true, func(call context.Context) (bool, error) {
			_, readErr := e.bootstrap.Kube.AppsV1().Deployments(attachment.Namespace).Get(call, kube.WorkloadName(iid), metav1.GetOptions{})
			if apierrors.IsNotFound(readErr) {
				return true, nil
			}
			return false, readErr
		}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := journal.ManagedIssuances(ctx, "org", scope.Release)
	if err != nil {
		t.Fatal(err)
	}
	var accepted bool
	for _, row := range rows {
		if row.Request.Resource == resourceDeleted && row.CleanupGrantID != "" && row.Request.Method == "DELETE" && row.Request.ResourceVersion != "" && row.Request.PropagationPolicy == "Orphan" && row.Outcome != nil && row.Outcome.State == "accepted" && row.Outcome.UID == row.Request.UID {
			accepted = true
		}
	}
	if !accepted {
		t.Fatal("missing accepted version-conditioned deployment cleanup")
	}
	if !shared {
		var captured bool
		if err = f.sql.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances w WHERE w.release_id=$1 AND w.outcome->>'State'='accepted' AND w.request->>'Resource'='deployments' AND EXISTS(SELECT 1 FROM jsonb_array_elements(w.cleanup_children) rs,jsonb_array_elements(w.cleanup_children) pod WHERE rs->>'resource'='replicasets' AND pod->>'resource'='pods' AND rs->>'owner_uid'=w.request->>'UID' AND pod->>'owner_uid'=rs->>'uid' AND pod->>'release'=$1 AND rs->>'release'=$1))`, scope.Release).Scan(&captured); err != nil || !captured {
			t.Fatalf("native ownership was not captured: %v", err)
		}
		recoverCleanupAfterRestart(ctx, t, e, creds, scope, selection, auth, foreignConfig)
	}
	var held bool
	if err = f.sql.QueryRow(ctx, `SELECT NOT released FROM loomplane_cluster_workload_holds WHERE id=$1`, input.ID).Scan(&held); err != nil || !held {
		t.Fatalf("effects released hold: %v", err)
	}
	checkReleaseSettlement(ctx, t, e, creds, infra, scope, input, selection, auth, shared)
}

func managedCleanupReviewer(t *testing.T) *security.Authorizer {
	t.Helper()
	roles := memory.New()
	engine, err := warden.NewEngine(warden.WithStore(roles))
	if err != nil {
		t.Fatal(err)
	}
	auth := security.NewAuthorizer(engine, roles, func(_ context.Context, org, user string) (string, bool, error) {
		return security.MemberOwner, org == "org" && user == "reviewer", nil
	})
	if err = auth.Assign(t.Context(), "org", "reviewer", security.RoleOwner, "test"); err != nil {
		t.Fatal(err)
	}
	return auth
}
