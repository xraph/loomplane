package kubernetes

import (
	"context"
	"encoding/pem"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	cpprovider "github.com/xraph/ctrlplane/provider"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kuberuntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

const jobFixtureName = "lp-job-0123456789ab"

// jobCluster is a namespace whose Job controller finishes each Job at
// once: finish sets its status, and its pod exits with exitCode.
type jobCluster struct {
	kube    *fake.Clientset
	created *batchv1.Job
	// secret is the runtime Secret as last written.
	secret *corev1.Secret
}

func newJobCluster(t *testing.T, exitCode int32, finish func(*batchv1.Job)) (*jobCluster, *Runtime) {
	t.Helper()
	cluster := &jobCluster{kube: fake.NewClientset()}
	cluster.kube.PrependReactor("create", "jobs", func(action kubetesting.Action) (bool, kuberuntime.Object, error) {
		job, _ := action.(kubetesting.CreateAction).GetObject().(*batchv1.Job)
		job.UID = types.UID("job-uid")
		cluster.created = job.DeepCopy()
		finish(job)
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: job.Name + "-x7k2p", Namespace: job.Namespace, Labels: maps.Clone(job.Spec.Template.Labels), OwnerReferences: []metav1.OwnerReference{{Kind: "Job", Name: job.Name, UID: job.UID}}},
			Status:     corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "main", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: exitCode}}}}},
		}
		if err := cluster.kube.Tracker().Add(pod); err != nil {
			t.Error(err)
		}
		return false, nil, nil
	})
	cluster.kube.PrependReactor("create", "secrets", func(action kubetesting.Action) (bool, kuberuntime.Object, error) {
		secret, _ := action.(kubetesting.CreateAction).GetObject().(*corev1.Secret)
		secret.UID = types.UID("secret-uid")
		cluster.secret = secret.DeepCopy()
		return false, nil, nil
	})
	cluster.kube.PrependReactor("update", "secrets", func(action kubetesting.Action) (bool, kuberuntime.Object, error) {
		secret, _ := action.(kubetesting.UpdateAction).GetObject().(*corev1.Secret)
		cluster.secret = secret.DeepCopy()
		return false, nil, nil
	})
	cluster.kube.PrependReactor("get", "pods", func(action kubetesting.Action) (bool, kuberuntime.Object, error) {
		if action.GetSubresource() != "log" {
			return false, nil, nil
		}
		// TailLines counts log entries, and a long line is several, so the
		// log can start inside a secret.
		return true, &kuberuntime.Unknown{Raw: []byte("Generated-value\nconnecting with s3cret-Generated-value\nmigration 0003 finished\n")}, nil
	})
	client := &Client{Config: &Config{Namespace: "selected", Details: map[string]string{"jobs": "ready"}}, Kube: cluster.kube}
	runtime := &Runtime{Session: func(context.Context, map[string]string) (*Client, error) { return client, nil }, Values: func(context.Context, map[string]string) (owned.RuntimeValues, error) {
		return owned.RuntimeValues{Env: map[string]string{"ADMIN_PASSWORD": "s3cret-Generated-value"}}, nil
	}}
	return cluster, runtime
}

func kubernetesJobFixture() (cpprovider.ServiceSpec, map[string]string) {
	spec := cpprovider.ServiceSpec{Name: "main", Image: "registry.example/sample-migrate@sha256:" + strings.Repeat("a", 64), Command: []string{"sample", "migrate"}, Env: map[string]string{"MODE": "migrate"}, Resources: cpprovider.ResourceSpec{CPUMillis: 500, MemoryMB: 256}}
	labels := map[string]string{owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelConnection: "conn_fixture", owned.LabelArchitecture: "arm64", owned.LabelCatalogComponent: "migrate", owned.LabelRevision: "dep/resource:app--migrate"}
	return spec, labels
}

// deletedInBackground reports whether the Job was deleted with Background
// propagation, so its pods go with it.
func (c *jobCluster) deletedInBackground() bool {
	for _, action := range c.kube.Actions() {
		deleted, ok := action.(kubetesting.DeleteAction)
		if !ok || action.GetResource().Resource != "jobs" || deleted.GetName() != jobFixtureName {
			continue
		}
		policy := deleted.GetDeleteOptions().PropagationPolicy
		return policy != nil && *policy == metav1.DeletePropagationBackground
	}
	return false
}

// secretRemoved reports whether the runtime Secret is gone, deleted only
// if it's still the one this attempt made.
func (c *jobCluster) secretRemoved(t *testing.T) bool {
	t.Helper()
	secrets, err := c.kube.CoreV1().Secrets("selected").List(t.Context(), metav1.ListOptions{})
	if err != nil || len(secrets.Items) != 0 {
		return false
	}
	for _, action := range c.kube.Actions() {
		if deleted, ok := action.(kubetesting.DeleteAction); ok && action.GetResource().Resource == "secrets" {
			uid := deleted.GetDeleteOptions().Preconditions
			return uid != nil && uid.UID != nil && *uid.UID == "secret-uid"
		}
	}
	return false
}

func TestKubernetesRunJobCompletes(t *testing.T) {
	cluster, runtime := newJobCluster(t, 0, func(job *batchv1.Job) { job.Status.Succeeded = 1 })
	// A read of the running Job that fails once is tried again.
	failed := false
	cluster.kube.PrependReactor("get", "jobs", func(kubetesting.Action) (bool, kuberuntime.Object, error) {
		if failed || cluster.created == nil {
			return false, nil, nil
		}
		failed = true
		return true, nil, errors.New("connection reset by peer")
	})
	spec, labels := kubernetesJobFixture()
	result, err := runtime.RunJob(t.Context(), jobFixtureName, spec, labels, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.Output != "[redacted]\nconnecting with [redacted]\nmigration 0003 finished\n" || !failed {
		t.Fatalf("result: %+v", result)
	}
	job := cluster.created
	if job == nil || job.Namespace != "selected" {
		t.Fatalf("job: %+v", job)
	}
	s := job.Spec
	if s.BackoffLimit == nil || *s.BackoffLimit != 0 || s.ActiveDeadlineSeconds == nil || *s.ActiveDeadlineSeconds != 630 || s.TTLSecondsAfterFinished == nil || *s.TTLSecondsAfterFinished != 3600 {
		t.Fatalf("job limits: %+v", s)
	}
	pod := s.Template.Spec
	if pod.RestartPolicy != corev1.RestartPolicyNever || pod.NodeSelector["kubernetes.io/arch"] != "arm64" || len(pod.Containers) != 1 || pod.Containers[0].Image != spec.Image || !slices.Equal(pod.Containers[0].Command, spec.Command) {
		t.Fatalf("job pod: %+v", pod)
	}
	for _, labelled := range []map[string]string{job.Labels, s.Template.Labels} {
		for key, want := range map[string]string{owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture", labelJob: jobFixtureName} {
			if labelled[key] != want {
				t.Fatalf("label %s = %q, want %q", key, labelled[key], want)
			}
		}
	}
	for _, env := range pod.Containers[0].Env {
		if env.Name == "ADMIN_PASSWORD" && (env.Value != "" || env.ValueFrom == nil || env.ValueFrom.SecretKeyRef.Name != jobFixtureName+"-runtime") {
			t.Fatal("a secret was stored as a plain environment value")
		}
	}
	if cluster.secret == nil || string(cluster.secret.Data["ADMIN_PASSWORD"]) != "s3cret-Generated-value" || cluster.secret.Labels[labelJob] != jobFixtureName {
		t.Fatalf("runtime secret: %+v", cluster.secret)
	}
	// The Job owns it, so the cluster removes it with the Job when
	// Loomplane can't.
	if owners := cluster.secret.OwnerReferences; len(owners) != 1 || owners[0].Kind != "Job" || owners[0].APIVersion != "batch/v1" || owners[0].Name != jobFixtureName || owners[0].UID != "job-uid" {
		t.Fatalf("runtime secret owners: %+v", owners)
	}
	if !cluster.deletedInBackground() || !cluster.secretRemoved(t) {
		t.Fatal("a finished job was left behind")
	}
	for _, action := range cluster.kube.Actions() {
		if action.GetNamespace() != "selected" {
			t.Fatalf("namespace escaped: %+v", action)
		}
	}
}

func TestKubernetesRunJobDeletesFailedJob(t *testing.T) {
	cluster, runtime := newJobCluster(t, 3, func(job *batchv1.Job) {
		job.Status.Failed = 1
		job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded"}}
	})
	spec, labels := kubernetesJobFixture()
	result, err := runtime.RunJob(t.Context(), jobFixtureName, spec, labels, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 3 || result.Output != "[redacted]\nconnecting with [redacted]\nmigration 0003 finished\n" {
		t.Fatalf("result: %+v", result)
	}
	if !cluster.deletedInBackground() || !cluster.secretRemoved(t) {
		t.Fatal("a failed job was left behind")
	}

	// A Job the cluster stopped at its deadline didn't finish.
	cluster, runtime = newJobCluster(t, 137, func(job *batchv1.Job) {
		job.Status.Failed = 1
		job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "DeadlineExceeded"}}
	})
	if _, err = runtime.RunJob(t.Context(), jobFixtureName, spec, labels, 10*time.Minute); err == nil || err.Error() != "migrate did not finish within 10m0s: [redacted]\nconnecting with [redacted]\nmigration 0003 finished\n" {
		t.Fatalf("deadline: %v", err)
	}
	if !cluster.deletedInBackground() || !cluster.secretRemoved(t) {
		t.Fatal("a job that ran out of time was left behind")
	}

	// Loomplane stops waiting at timeout, before the cluster's deadline
	// deletes the pod, so the error still shows what it wrote.
	cluster, runtime = newJobCluster(t, 0, func(*batchv1.Job) {})
	if _, err = runtime.RunJob(t.Context(), jobFixtureName, spec, labels, 50*time.Millisecond); err == nil || err.Error() != "migrate did not finish within 50ms: [redacted]\nconnecting with [redacted]\nmigration 0003 finished\n" {
		t.Fatalf("local timeout: %v", err)
	}
	if deadline := cluster.created.Spec.ActiveDeadlineSeconds; deadline == nil || *deadline != 31 {
		t.Fatalf("the cluster's deadline isn't 30 s after the timeout: %v", deadline)
	}
	if !cluster.deletedInBackground() || !cluster.secretRemoved(t) {
		t.Fatal("a job that ran out of time was left behind")
	}
}

func TestKubernetesCheckReportsJobPermissions(t *testing.T) {
	check := func(denied ...string) (*Result, []string) {
		t.Helper()
		s, paths, mu := clusterFixture(t, false, denied...)
		ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw})
		cfg, err := ParseConfig(kubeconfig(t, s.URL, ca), map[string]string{"namespace": "production"})
		if err != nil {
			t.Fatal(err)
		}
		c, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		result, err := c.Check(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		return result, slices.Clone(*paths)
	}
	result, paths := check()
	if result.Details["jobs"] != "ready" {
		t.Fatalf("jobs: %+v", result.Details)
	}
	for _, want := range []string{"production:jobs:create", "production:jobs:get", "production:jobs:list", "production:jobs:delete", "production:pods/log:get"} {
		if !slices.Contains(paths, want) {
			t.Fatalf("unchecked job permission %s", want)
		}
	}

	for _, denied := range []string{"jobs:delete", "pods/log:get"} {
		result, _ = check(denied)
		if _, ok := result.Details["jobs"]; ok {
			t.Fatalf("without %s, jobs = %q", denied, result.Details["jobs"])
		}
		if slices.ContainsFunc(result.Missing, func(m string) bool { return strings.HasPrefix(m, "jobs") || strings.HasPrefix(m, "pods/log") }) {
			t.Fatalf("job permissions made the connection incomplete: %v", result.Missing)
		}
	}
}

func TestKubernetesRunJobMapsCommandToEntrypoint(t *testing.T) {
	// A job's command replaces the image's entrypoint and its args are
	// the command, as a process's are.
	cluster, runtime := newJobCluster(t, 0, func(job *batchv1.Job) { job.Status.Succeeded = 1 })
	spec, labels := kubernetesJobFixture()
	spec.Command, spec.Args = []string{"/bin/x"}, []string{"a"}
	if _, err := runtime.RunJob(t.Context(), jobFixtureName, spec, labels, time.Minute); err != nil {
		t.Fatal(err)
	}
	container := cluster.created.Spec.Template.Spec.Containers[0]
	if !slices.Equal(container.Command, []string{"/bin/x"}) || !slices.Equal(container.Args, []string{"a"}) {
		t.Fatalf("command %v, args %v", container.Command, container.Args)
	}

	// Without a command, the image's own entrypoint runs.
	cluster, runtime = newJobCluster(t, 0, func(job *batchv1.Job) { job.Status.Succeeded = 1 })
	spec.Command, spec.Args = nil, nil
	if _, err := runtime.RunJob(t.Context(), jobFixtureName, spec, labels, time.Minute); err != nil {
		t.Fatal(err)
	}
	if container = cluster.created.Spec.Template.Spec.Containers[0]; container.Command != nil || container.Args != nil {
		t.Fatalf("command %v, args %v", container.Command, container.Args)
	}
}
