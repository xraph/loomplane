package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kuberuntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/pkg/manifest"
)

func lifecycleRunFixture() owned.LifecycleJobRun {
	return owned.LifecycleJobRun{OrgID: "org", ProjectID: "project", FleetID: "fleet", InstanceID: "instance", EnvironmentID: "env", OperationID: "operation", Generation: 1, OwnershipEpoch: 1, Name: "migrate", Namespace: "apps", NamespaceUID: "namespace", ConnectionID: "connection", ClusterCA: strings.Repeat("a", 64), Architecture: "arm64", CapabilityRevision: owned.LifecycleCapabilityRevision, Job: manifest.LifecycleJob{Phase: "pre_deploy", Image: "registry.example.test/migrate@sha256:" + strings.Repeat("a", 64), TimeoutSeconds: 60, MaxAttempts: 2}}
}
func lifecycleScopeFixture(run owned.LifecycleJobRun, objects LifecycleObjects) ManagedScope {
	scope := transportScope()
	scope.Instance = run.InstanceID
	scope.LifecycleRunID, scope.LifecycleJobName, scope.LifecycleDigest = run.ID(), run.AttemptName(1), run.Digest()
	scope.LifecycleSpecDigest, scope.LifecycleSecretDigest = objects.Digests()
	return scope
}
func TestLifecycleJobRuntimeKeepsTerminalEvidence(t *testing.T) {
	run := lifecycleRunFixture()
	kube := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: run.Namespace, UID: "namespace"}})
	kube.PrependReactor("create", "*", func(action kubetesting.Action) (bool, kuberuntime.Object, error) {
		create, ok := action.(kubetesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		switch object := create.GetObject().(type) {
		case *corev1.Secret:
			object.UID = "secret-uid"
			object.ResourceVersion = "1"
		case *batchv1.Job:
			object.UID = "job-uid"
			object.ResourceVersion = "1"
		}
		return false, nil, nil
	})
	backend := &LifecycleRuntime{Session: func(_ context.Context, r owned.LifecycleJobRun, _ int, objects LifecycleObjects) (*Client, error) {
		return &Client{Kube: kube, Config: &Config{Namespace: r.Namespace, ManagedWorkload: true, ManagedJournal: &memoryManagedJournal{}, ManagedScope: lifecycleScopeFixture(r, objects)}}, nil
	}}
	receipt, err := backend.CreateLifecycleJob(t.Context(), run, 1)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != "pending" {
		t.Fatal(receipt)
	}
	job, err := kube.BatchV1().Jobs(run.Namespace).Get(t.Context(), receipt.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if job.Spec.TTLSecondsAfterFinished != nil || *job.Spec.BackoffLimit != 0 || *job.Spec.ActiveDeadlineSeconds != 60 || *job.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Fatal("job exceeds bounded execution contract")
	}
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: batchv1.JobReasonDeadlineExceeded}}
	if _, err = kube.BatchV1().Jobs(run.Namespace).UpdateStatus(t.Context(), job, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	observed, err := backend.ObserveLifecycleJob(t.Context(), run, 1)
	if err != nil || observed.State != "failed" || observed.Reason != "deadline_exceeded" {
		t.Fatal(observed, err)
	}
	for _, action := range kube.Actions() {
		if action.GetVerb() == "delete" {
			t.Fatal("observation deleted evidence before receipt")
		}
	}
	foreign := *observed
	foreign.JobUID = "foreign"
	if err = backend.CleanupLifecycleJob(t.Context(), run, foreign); !errors.Is(err, owned.ErrLifecycleAuthority) {
		t.Fatal("foreign UID cleanup", err)
	}
	if err = backend.CleanupLifecycleJob(t.Context(), run, *observed); err != nil {
		t.Fatal(err)
	}
	if _, err = backend.ObserveLifecycleJob(t.Context(), run, 1); !errors.Is(err, owned.ErrLifecyclePending) {
		t.Fatal("missing result did not remain uncertain", err)
	}
}
func TestLifecycleJobTransportExactObjects(t *testing.T) {
	run := lifecycleRunFixture()
	objects, err := lifecycleObjects(run, 1, map[string]string{"SETTING": "private-value"})
	if err != nil {
		t.Fatal(err)
	}
	scope := lifecycleScopeFixture(run, objects)
	if !managedScopeValid(scope) {
		t.Fatal("valid lifecycle scope rejected")
	}
	transport := managedTransport{scope: scope}
	request := func(resource string, object any) error {
		raw, _ := json.Marshal(object)
		path := "/api/v1/namespaces/apps/" + resource
		if resource == "jobs" {
			path = "/apis/batch/v1/namespaces/apps/jobs"
		}
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://cluster.example"+path, bytes.NewReader(raw))
		_, requestErr := transport.request(req)
		return requestErr
	}
	if err = request("jobs", objects.Job); err != nil {
		t.Fatal(err)
	}
	if err = request("secrets", objects.Secret); err != nil {
		t.Fatal(err)
	}
	changed := objects.Job.DeepCopy()
	changed.Spec.Template.Spec.Containers[0].Image = "foreign"
	if err = request("jobs", changed); err == nil {
		t.Fatal("changed spec accepted")
	}
	secret := objects.Secret.DeepCopy()
	secret.Data["SETTING"] = []byte("changed")
	if err = request("secrets", secret); err == nil {
		t.Fatal("changed runtime values accepted")
	}
	secret = objects.Secret.DeepCopy()
	secret.StringData = map[string]string{"SETTING": "bypass"}
	if err = request("secrets", secret); err == nil {
		t.Fatal("stringData overrode reviewed bytes")
	}
	if err = request("services", &corev1.Service{ObjectMeta: objects.Job.ObjectMeta}); err == nil {
		t.Fatal("lifecycle scope created a Service")
	}
	mixed := scope
	mixed.GroupOperation = "prepare"
	if managedScopeValid(mixed) {
		t.Fatal("mixed authority accepted")
	}
}

type lifecycleHistoryJournal struct {
	memoryManagedJournal
	unissued bool
}

func (j *lifecycleHistoryJournal) LifecycleJobUnissued(context.Context, ManagedScope) (bool, error) {
	return j.unissued, nil
}
func TestLifecycleJobCrashBeforeTransmissionCanResume(t *testing.T) {
	run := lifecycleRunFixture()
	kube := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: run.Namespace, UID: "namespace"}})
	kube.PrependReactor("create", "*", func(action kubetesting.Action) (bool, kuberuntime.Object, error) {
		create, ok := action.(kubetesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		switch object := create.GetObject().(type) {
		case *corev1.Secret:
			object.UID = "secret"
		case *batchv1.Job:
			object.UID = "job"
		}
		return false, nil, nil
	})
	history := &lifecycleHistoryJournal{}
	backend := &LifecycleRuntime{Session: func(_ context.Context, r owned.LifecycleJobRun, _ int, objects LifecycleObjects) (*Client, error) {
		return &Client{Kube: kube, Config: &Config{Namespace: r.Namespace, ManagedWorkload: true, ManagedJournal: history, ManagedScope: lifecycleScopeFixture(r, objects)}}, nil
	}}
	if _, err := backend.ObserveLifecycleJob(t.Context(), run, 1); !errors.Is(err, owned.ErrLifecyclePending) {
		t.Fatal(err)
	}
	for _, action := range kube.Actions() {
		if action.GetVerb() == "create" {
			t.Fatal("uncertain issuance repeated")
		}
	}
	history.unissued = true
	receipt, err := backend.ObserveLifecycleJob(t.Context(), run, 1)
	if err != nil || receipt.JobUID != "job" {
		t.Fatal(receipt, err)
	}
}

func TestLifecycleJobCleanupCannotOrphanPods(t *testing.T) {
	run := lifecycleRunFixture()
	objects, err := lifecycleObjects(run, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	transport := managedTransport{scope: lifecycleScopeFixture(run, objects)}
	uid, version := types.UID("job-uid"), "3"
	for _, policy := range []metav1.DeletionPropagation{metav1.DeletePropagationForeground, metav1.DeletePropagationBackground, metav1.DeletePropagationOrphan} {
		options := metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}, PropagationPolicy: &policy}
		raw, _ := json.Marshal(options)
		request, _ := http.NewRequestWithContext(t.Context(), http.MethodDelete, "https://cluster.example/apis/batch/v1/namespaces/apps/jobs/"+objects.Job.Name, bytes.NewReader(raw))
		_, requestErr := transport.request(request)
		if (requestErr == nil) != (policy == metav1.DeletePropagationForeground) {
			t.Fatalf("cleanup propagation %s: %v", policy, requestErr)
		}
	}
}
