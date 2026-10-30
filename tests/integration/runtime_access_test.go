//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
	"github.com/xraph/dispatch/store/memory"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"

	"github.com/xraph/loomplane/internal/cloudinfra"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

func accessInput(f *enrollmentServiceFixture, a *cloudinfra.RuntimeAttachment) cloudinfra.ClusterInput {
	r, err := f.store.GetManagedResource(f.t.Context(), a.OrgID, a.ClusterID)
	if err != nil {
		f.t.Fatal(err)
	}
	return cloudinfra.ClusterInput{Kind: cloudinfra.RuntimeAccessUpgrade, Provider: "digitalocean", ConnectionID: "conn", AccountID: "account", ConnectionGeneration: 1, CloudRevision: 1, ResourceID: "cluster", ResourceRevision: r.Revision, Ref: f.provider.observation.Ref, RuntimeAccess: &provider.RuntimeAccessInput{AttachmentID: a.ID, Profile: provider.RuntimeAccessProfile{SchemaVersion: 1, CleanupDependents: true, HPA: true}}}
}

func TestRuntimeAccessUpgradeRealAPI(t *testing.T) {
	f := newEnrollmentServiceFixture(t)
	svc, credentials := f.service(f.store, f.vault())
	if err := svc.ReconcileClusterOperation(t.Context(), "org", f.op.ID, nil); err != nil {
		t.Fatal(err)
	}
	a := f.attachment()
	original, _ := json.Marshal(a)
	var targetBefore, holdBefore string
	if err := f.sql.QueryRow(t.Context(), `SELECT to_jsonb(t)::text FROM loomplane_cloud_targets t WHERE org_id='org' AND connection_id=$1`, a.RuntimeConnectionID).Scan(&targetBefore); err != nil {
		t.Fatal(err)
	}
	// Persist an existing hold identity fixture. This test does not admit or release it.
	if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_cluster_workload_holds(org_id,cloud_id,resource_id,id,plan_id,service,release_id,body) VALUES('org','cloud','cluster','old-hold','old-plan','worker','old-release',jsonb_build_object('created_at',clock_timestamp(),'input',jsonb_build_object('attachment_id',$1::text,'trust_revision',1,'connection_id',$2::text),'maximum',jsonb_build_object('pods',2)))`, a.ID, a.RuntimeConnectionID); err != nil {
		t.Fatal(err)
	}
	if err := f.sql.QueryRow(t.Context(), `SELECT to_jsonb(h)::text FROM loomplane_cluster_workload_holds h WHERE org_id='org' AND id='old-hold'`).Scan(&holdBefore); err != nil {
		t.Fatal(err)
	}
	raw := f.handout(credentials, a)
	cfg, err := kube.ParseConfig(raw, map[string]string{"namespace": a.Namespace})
	if err != nil {
		t.Fatal(err)
	}
	runtimeClient, err := kube.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	profile := provider.RuntimeAccessProfile{SchemaVersion: 1, CleanupDependents: true, HPA: true}
	if err = runtimeClient.CheckRuntimeAccess(t.Context(), profile); !errors.Is(err, kube.ErrCleanupPermissionsUnavailable) {
		t.Fatalf("legacy cleanup allowed: %v", err)
	}
	op, err := svc.PreviewCluster(t.Context(), f.principal, "cloud", accessInput(f, a))
	if err != nil {
		t.Fatal(err)
	}
	if op.Input.RuntimeAccess.RoleUID == "" || op.Input.RuntimeAccess.RoleResourceVersion == "" {
		t.Fatal("review omitted role pins")
	}
	workflow := orchestration.NewDeployments()
	workflow.BindClusters(svc)
	routes := orchestration.NewPgRunStore(f.pg)
	workflow.BindRunStore(routes)
	worker, err := drt.NewWorker(memory.New(), workflow.RuntimeAccessOptions("access", 1))
	if err != nil {
		t.Fatal(err)
	}
	workflow.BindWorker(orchestration.RuntimeAccessBuild, worker)
	svc.ClusterWorkflow = workflow
	if _, err = svc.ApplyCluster(t.Context(), f.principal, "cloud", op.ID, cloudinfra.ApplyInput{Review: op.Digest}); err != nil {
		t.Fatal(err)
	}
	route, err := routes.ForPlan(t.Context(), "runtime-access:"+op.ID)
	if err != nil || route.Request.BuildID != orchestration.RuntimeAccessBuild || route.Request.WorkflowType != orchestration.RuntimeAccessWorkflow {
		t.Fatalf("route: %+v %v", route, err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_deployment_workflow_runs SET request=jsonb_set(request,'{build_id}','"deployment-v2"') WHERE workflow_id=$1`, route.Request.WorkflowID); err == nil {
		t.Fatal("old route accepted")
	}
	for _, kind := range []durable.TaskKind{durable.TaskWorkflow, durable.TaskActivity, durable.TaskWorkflow} {
		if worked, e := worker.RunOnce(t.Context(), kind); e != nil || !worked {
			t.Fatalf("workflow execution: %v %v", worked, e)
		}
	}
	saved, err := f.store.GetClusterOperation(t.Context(), "org", "cloud", op.ID)
	if err != nil || saved.Status != "succeeded" {
		t.Fatalf("upgrade: %+v %v", saved, err)
	}
	record, err := f.store.GetRuntimeAccess(t.Context(), "org", a.ID)
	if err != nil || record == nil || record.Revision != 1 || record.Receipt.Kind != "accepted-role-update" {
		t.Fatalf("access receipt: %+v %v", record, err)
	}
	current, err := f.store.GetRuntimeAttachment(t.Context(), "org", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(current)
	if string(after) != string(original) {
		t.Fatal("upgrade changed physical/enrollment identity")
	}
	if err = runtimeClient.CheckRuntimeAccess(t.Context(), profile); err != nil {
		t.Fatal(err)
	}
	f.handout(credentials, current)
	var targetAfter, holdAfter string
	if err = f.sql.QueryRow(t.Context(), `SELECT to_jsonb(t)::text FROM loomplane_cloud_targets t WHERE org_id='org' AND connection_id=$1`, a.RuntimeConnectionID).Scan(&targetAfter); err != nil {
		t.Fatal(err)
	}
	if err = f.sql.QueryRow(t.Context(), `SELECT to_jsonb(h)::text FROM loomplane_cluster_workload_holds h WHERE org_id='org' AND id='old-hold'`).Scan(&holdAfter); err != nil {
		t.Fatal(err)
	}
	if targetAfter != targetBefore || holdAfter != holdBefore {
		t.Fatal("upgrade changed old target or hold")
	}
	runtimeAccessAPICheck(t, f.bootstrap, runtimeClient, a.Namespace)
	if err = runtimeClient.CheckRuntimeBoundaries(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = runtimeClient.Kube.RbacV1().Roles(a.Namespace).Get(t.Context(), a.ServiceAccountName, metav1.GetOptions{}); err == nil {
		t.Fatal("runtime gained role reads")
	}
	downgrade := accessInput(f, a)
	downgrade.RuntimeAccess.Profile.HPA = false
	if _, err = svc.PreviewCluster(t.Context(), f.principal, "cloud", downgrade); err == nil {
		t.Fatal("capability downgrade allowed")
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_runtime_access SET revision=revision+1 WHERE org_id='org'`); err == nil {
		t.Fatal("access history mutable")
	}
	role, err := f.bootstrap.Kube.RbacV1().Roles(a.Namespace).Get(t.Context(), a.ServiceAccountName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	role.Annotations[kube.RuntimeAccessDigestAnnotation] = "foreign"
	if _, err = f.bootstrap.Kube.RbacV1().Roles(a.Namespace).Update(t.Context(), role, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	connection, err := credentials.Get(t.Context(), f.principal, a.RuntimeConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ResolveManagedCredential(t.Context(), *connection); !errors.Is(err, kube.ErrCleanupPermissionsUnavailable) {
		t.Fatalf("changed role handed out: %v", err)
	}
}

func TestRuntimeAccessFreshProfileRealAPI(t *testing.T) {
	profile := &provider.RuntimeAccessProfile{SchemaVersion: 1, CleanupDependents: true}
	f := newEnrollmentServiceFixtureWithProfile(t, profile)
	svc, credentials := f.service(f.store, f.vault())
	if err := svc.ReconcileClusterOperation(t.Context(), "org", f.op.ID, nil); err != nil {
		t.Fatal(err)
	}
	a := f.attachment()
	record, err := f.store.GetRuntimeAccess(t.Context(), "org", a.ID)
	if err != nil || record == nil || !reflect.DeepEqual(record.Input.Profile, *profile) || record.Receipt.Kind != "enrolled-role" {
		t.Fatalf("fresh receipt: %+v %v", record, err)
	}
	f.handout(credentials, a)
}

func TestRuntimeAccessStaleReviewRealAPI(t *testing.T) {
	f := newEnrollmentServiceFixture(t)
	svc, _ := f.service(f.store, f.vault())
	if err := svc.ReconcileClusterOperation(t.Context(), "org", f.op.ID, nil); err != nil {
		t.Fatal(err)
	}
	a := f.attachment()
	op, err := svc.PreviewCluster(t.Context(), f.principal, "cloud", accessInput(f, a))
	if err != nil {
		t.Fatal(err)
	}
	role, err := f.bootstrap.Kube.RbacV1().Roles(a.Namespace).Get(t.Context(), a.ServiceAccountName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if role.Annotations == nil {
		role.Annotations = map[string]string{}
	}
	role.Annotations["test-change"] = "changed"
	if _, err = f.bootstrap.Kube.RbacV1().Roles(a.Namespace).Update(t.Context(), role, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ApplyCluster(t.Context(), f.principal, "cloud", op.ID, cloudinfra.ApplyInput{Review: op.Digest}); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("stale review accepted: %v", err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cluster_operations SET status='succeeded' WHERE id=$1`, op.ID); err == nil {
		t.Fatal("old protocol changed access operation")
	}
}

type runtimeAccessCrashStore struct {
	*cloudinfra.PgStore
	crash bool
}

func (s *runtimeAccessCrashStore) SaveClusterCheckpoint(ctx context.Context, o *cloudinfra.ClusterOperation, revision int64) error {
	if s.crash && o.RuntimeAccess != nil && o.RuntimeAccess.NetworkOutcome == "accepted" {
		s.crash = false
		panic(errEnrollmentProcessLost)
	}
	return s.PgStore.SaveClusterCheckpoint(ctx, o, revision)
}

func TestRuntimeAccessLostResponseRecoveryRealAPI(t *testing.T) {
	f := newEnrollmentServiceFixture(t)
	svc, _ := f.service(f.store, f.vault())
	if err := svc.ReconcileClusterOperation(t.Context(), "org", f.op.ID, nil); err != nil {
		t.Fatal(err)
	}
	a := f.attachment()
	op, err := svc.PreviewCluster(t.Context(), f.principal, "cloud", accessInput(f, a))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ApplyCluster(t.Context(), f.principal, "cloud", op.ID, cloudinfra.ApplyInput{Review: op.Digest}); err != nil {
		t.Fatal(err)
	}
	svc.Clusters = &runtimeAccessCrashStore{PgStore: f.store, crash: true}
	enrollmentMustCrash(t, func() {
		if e := svc.ReconcileRuntimeAccessOperation(t.Context(), "org", op.ID, nil); e != nil {
			t.Fatal(e)
		}
	})
	role, err := f.bootstrap.Kube.RbacV1().Roles(a.Namespace).Get(t.Context(), a.ServiceAccountName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `BEGIN; SELECT set_config('loomplane.runtime_access_protocol','runtime-access-v1',true)`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cluster_operations SET body=jsonb_set(body,'{lease_expires_at}',to_jsonb($2::text)) WHERE id=$1`, op.ID, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `COMMIT`); err != nil {
		t.Fatal(err)
	}
	if err = svc.ReconcileRuntimeAccessOperation(t.Context(), "org", op.ID, nil); err != nil {
		t.Fatal(err)
	}
	saved, err := f.store.GetClusterOperation(t.Context(), "org", "cloud", op.ID)
	if err != nil || saved.Status != "succeeded" || saved.RuntimeAccess.NetworkOutcome != "unknown" || saved.RuntimeAccess.Receipt.Kind != "observed-role-update" {
		t.Fatalf("observed recovery: %+v %v", saved, err)
	}
	after, err := f.bootstrap.Kube.RbacV1().Roles(a.Namespace).Get(t.Context(), a.ServiceAccountName, metav1.GetOptions{})
	if err != nil || after.ResourceVersion != role.ResourceVersion {
		t.Fatal("recovery rewrote role", err)
	}
}

func runtimeAccessAPICheck(t *testing.T, admin, runtime *kube.Client, namespace string) {
	t.Helper()
	zero := int32(0)
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "access-proof", Namespace: namespace}, Spec: appsv1.ReplicaSetSpec{Replicas: &zero, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "access-proof"}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "access-proof"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "pause", Image: "registry.k8s.io/pause:3.10"}}}}}}
	if _, err := runtime.Kube.AppsV1().ReplicaSets(namespace).Create(t.Context(), rs, metav1.CreateOptions{}); !apierrors.IsForbidden(err) {
		t.Fatal("runtime gained RS create", err)
	}
	created, err := admin.Kube.AppsV1().ReplicaSets(namespace).Create(t.Context(), rs, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.Kube.AppsV1().ReplicaSets(namespace).List(t.Context(), metav1.ListOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, getErr := runtime.Kube.AppsV1().ReplicaSets(namespace).Get(t.Context(), created.Name, metav1.GetOptions{})
		if getErr != nil {
			return getErr
		}
		if current.UID != created.UID {
			return errors.New("fixture RS identity changed")
		}
		return runtime.Kube.AppsV1().ReplicaSets(namespace).Delete(t.Context(), created.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &current.UID, ResourceVersion: &current.ResourceVersion}})
	}); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "access-proof", Namespace: namespace}, Spec: corev1.PodSpec{SchedulingGates: []corev1.PodSchedulingGate{{Name: "test.loomplane.io/hold"}}, Containers: []corev1.Container{{Name: "pause", Image: "registry.k8s.io/pause:3.10"}}}}
	if _, err = runtime.Kube.CoreV1().Pods(namespace).Create(t.Context(), pod, metav1.CreateOptions{}); !apierrors.IsForbidden(err) {
		t.Fatal("runtime gained pod create", err)
	}
	actual, err := admin.Kube.CoreV1().Pods(namespace).Create(t.Context(), pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.Kube.CoreV1().Pods(namespace).Delete(t.Context(), actual.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &actual.UID}}); err != nil {
		t.Fatal(err)
	}
	hpa := &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Name: "access-proof", Namespace: namespace}, Spec: autoscalingv2.HorizontalPodAutoscalerSpec{ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "access-proof"}, MaxReplicas: 2}}
	hpa, err = runtime.Kube.AutoscalingV2().HorizontalPodAutoscalers(namespace).Create(t.Context(), hpa, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.Kube.AutoscalingV2().HorizontalPodAutoscalers(namespace).List(t.Context(), metav1.ListOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = runtime.Kube.AutoscalingV2().HorizontalPodAutoscalers(namespace).Delete(t.Context(), hpa.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &hpa.UID}}); err != nil {
		t.Fatal(err)
	}
}

type runtimeAccessAuth struct{ denied string }

func (a *runtimeAccessAuth) Require(_ context.Context, p security.Principal, _ security.Permission, _ string) error {
	if p.UserID == a.denied {
		return security.ErrForbidden
	}
	return nil
}
func (a *runtimeAccessAuth) RequireCurrentMembership(_ context.Context, p security.Principal) error {
	if p.UserID == a.denied {
		return security.ErrForbidden
	}
	return nil
}

func TestRuntimeAccessCurrentAuthorityRealAPI(t *testing.T) {
	for _, who := range []string{"upgrader", "manager"} {
		t.Run(who, func(t *testing.T) {
			f := newEnrollmentServiceFixture(t)
			svc, _ := f.service(f.store, f.vault())
			if err := svc.ReconcileClusterOperation(t.Context(), "org", f.op.ID, nil); err != nil {
				t.Fatal(err)
			}
			a := f.attachment()
			actor := f.principal
			actor.UserID = "upgrader"
			auth := &runtimeAccessAuth{}
			svc.Authz = auth
			op, err := svc.PreviewCluster(t.Context(), actor, "cloud", accessInput(f, a))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = svc.ApplyCluster(t.Context(), actor, "cloud", op.ID, cloudinfra.ApplyInput{Review: op.Digest}); err != nil {
				t.Fatal(err)
			}
			auth.denied = who
			if err = svc.ReconcileRuntimeAccessOperation(t.Context(), "org", op.ID, nil); err != nil {
				t.Fatal(err)
			}
			saved, err := f.store.GetClusterOperation(t.Context(), "org", "cloud", op.ID)
			if err != nil || saved.Status != "needs_attention" {
				t.Fatalf("revoked operation: %+v %v", saved, err)
			}
			record, err := f.store.GetRuntimeAccess(t.Context(), "org", a.ID)
			if err != nil || record != nil {
				t.Fatal("revoked authority published capability", err)
			}
			role, err := f.bootstrap.Kube.RbacV1().Roles(a.Namespace).Get(t.Context(), a.ServiceAccountName, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if who == "upgrader" && role.ResourceVersion != op.Input.RuntimeAccess.RoleResourceVersion {
				t.Fatal("revoked upgrade changed role")
			}
			if who == "manager" && saved.Error != "cleanup_permissions_unavailable" {
				t.Fatal("enrollment delegation blocker lost")
			}
		})
	}
}

func TestRuntimeAccessInsufficientManagementPermissionRealAPI(t *testing.T) {
	f := newEnrollmentServiceFixture(t)
	svc, credentials := f.service(f.store, f.vault())
	if err := svc.ReconcileClusterOperation(t.Context(), "org", f.op.ID, nil); err != nil {
		t.Fatal(err)
	}
	a := f.attachment()
	raw := f.handout(credentials, a)
	cfg, err := kube.ParseConfig(raw, map[string]string{"namespace": a.Namespace})
	if err != nil {
		t.Fatal(err)
	}
	op, err := svc.PreviewCluster(t.Context(), f.principal, "cloud", accessInput(f, a))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ApplyCluster(t.Context(), f.principal, "cloud", op.ID, cloudinfra.ApplyInput{Review: op.Digest}); err != nil {
		t.Fatal(err)
	}
	f.provider.access.Token = cfg.REST.BearerToken
	f.provider.access.ClientCertificate = nil
	f.provider.access.ClientKey = nil
	if err = svc.ReconcileRuntimeAccessOperation(t.Context(), "org", op.ID, nil); err != nil {
		t.Fatal(err)
	}
	saved, err := f.store.GetClusterOperation(t.Context(), "org", "cloud", op.ID)
	if err != nil || saved.Status != "needs_attention" || saved.RuntimeAccess != nil && saved.RuntimeAccess.Attempted {
		t.Fatalf("unprivileged access attempted: %+v %v", saved, err)
	}
	role, err := f.bootstrap.Kube.RbacV1().Roles(a.Namespace).Get(t.Context(), a.ServiceAccountName, metav1.GetOptions{})
	if err != nil || role.ResourceVersion != op.Input.RuntimeAccess.RoleResourceVersion {
		t.Fatal("unprivileged upgrade changed role", err)
	}
}
