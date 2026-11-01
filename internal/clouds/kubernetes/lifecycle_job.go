package kubernetes

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

const lifecycleRunLabel = "loomplane.lifecycle-run"
const lifecycleDigestAnnotation = "loomplane.lifecycle-digest"
const lifecycleEpochLabel = "loomplane.ownership-epoch"

// LifecycleObjects is the exact desired Job and runtime Secret. The session
// factory admits their digests against the persisted operation before use.
type LifecycleObjects struct {
	Job    *batchv1.Job
	Secret *corev1.Secret
}

func (o LifecycleObjects) Digests() (job, secret string) {
	raw, _ := json.Marshal(o.Job.Spec) //nolint:errcheck // native Job fields contain only supported JSON values
	job = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	raw, _ = json.Marshal(o.Secret.Data) //nolint:errcheck // byte maps always encode
	secret = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	return job, secret
}

type LifecycleRuntime struct {
	Session func(context.Context, owned.LifecycleJobRun, int, LifecycleObjects) (*Client, error)
	Values  func(context.Context, owned.LifecycleJobRun) (map[string]string, error)
}

var _ owned.LifecycleJobBackend = (*LifecycleRuntime)(nil)

func lifecycleObjects(run owned.LifecycleJobRun, attempt int, values map[string]string) (LifecycleObjects, error) {
	if err := run.Validate(); err != nil || attempt < 1 || attempt > run.Job.MaxAttempts {
		return LifecycleObjects{}, owned.ErrLifecycleAuthority
	}
	if len(values) > 128 {
		return LifecycleObjects{}, owned.ErrLifecycleAuthority
	}
	total := 0
	for key, value := range values {
		total += len(key) + len(value)
		if len(validation.IsEnvVarName(key)) != 0 || len(key) > 128 || len(value) > 65536 {
			return LifecycleObjects{}, owned.ErrLifecycleAuthority
		}
	}
	if total > 512<<10 {
		return LifecycleObjects{}, owned.ErrLifecycleAuthority
	}
	name := run.AttemptName(attempt)
	identity := map[string]string{owned.LabelOrg: run.OrgID, owned.LabelProject: run.ProjectID, owned.LabelEnvironment: run.EnvironmentID, owned.LabelInstance: run.InstanceID, lifecycleRunLabel: run.ID()[:40], lifecycleEpochLabel: strconv.FormatInt(run.OwnershipEpoch, 10)}
	meta := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: run.Namespace, Labels: maps.Clone(identity), Annotations: map[string]string{lifecycleDigestAnnotation: run.Digest()}}
	}
	secret := &corev1.Secret{ObjectMeta: meta(name + "-runtime"), Type: corev1.SecretTypeOpaque, Data: map[string][]byte{}}
	env := []corev1.EnvVar{}
	for _, key := range slices.Sorted(maps.Keys(values)) {
		secret.Data[key] = []byte(values[key])
		env = append(env, corev1.EnvVar{Name: key, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secret.Name}, Key: key}}})
	}
	cpu, memory := run.Job.EffectiveResources()
	resources := lifecycleResources(cpu, memory)
	automount, backoff, deadline := false, int32(0), int64(run.Job.TimeoutSeconds)
	job := &batchv1.Job{ObjectMeta: meta(name), Spec: batchv1.JobSpec{BackoffLimit: &backoff, ActiveDeadlineSeconds: &deadline, Template: corev1.PodTemplateSpec{ObjectMeta: meta(""), Spec: corev1.PodSpec{AutomountServiceAccountToken: &automount, RestartPolicy: corev1.RestartPolicyNever, NodeSelector: map[string]string{corev1.LabelArchStable: run.Architecture}, Containers: []corev1.Container{{Name: "main", Image: run.Job.Image, ImagePullPolicy: corev1.PullIfNotPresent, Command: slices.Clone(run.Job.Command), Args: slices.Clone(run.Job.Args), Env: env, Resources: resources}}}}}}
	job.Spec.Template.Namespace = ""
	if run.ImagePullSecret != "" {
		job.Spec.Template.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: run.ImagePullSecret}}
	}
	// No TTL: the terminal Job is evidence until its receipt is committed.
	return LifecycleObjects{job, secret}, nil
}
func (r *LifecycleRuntime) session(ctx context.Context, run owned.LifecycleJobRun, attempt int) (*Client, LifecycleObjects, error) {
	if r.Session == nil {
		return nil, LifecycleObjects{}, owned.ErrLifecycleAuthority
	}
	values := map[string]string{}
	if r.Values != nil {
		var err error
		values, err = r.Values(ctx, run)
		if err != nil {
			return nil, LifecycleObjects{}, err
		}
	}
	objects, err := lifecycleObjects(run, attempt, values)
	if err != nil {
		return nil, objects, err
	}
	client, err := r.Session(ctx, run, attempt, objects)
	if err != nil {
		return nil, objects, err
	}
	jobDigest, secretDigest := objects.Digests()
	if client == nil || client.Config == nil || client.Kube == nil || !client.Config.journalRequired() || client.Config.ManagedJournal == nil {
		return nil, objects, owned.ErrLifecycleAuthority
	}
	cfg, scope := client.Config, client.Config.ManagedScope
	if cfg.Details["image_pull_secret"] != run.ImagePullSecret || cfg.Namespace != run.Namespace || scope.Org != run.OrgID || scope.Project != run.ProjectID || scope.Environment != run.EnvironmentID || scope.Instance != run.InstanceID || scope.NamespaceUID != run.NamespaceUID || scope.ClusterCA != run.ClusterCA || scope.Connection != run.ConnectionID || scope.LifecycleRunID != run.ID() || scope.LifecycleJobName != run.AttemptName(attempt) || scope.LifecycleDigest != run.Digest() || scope.LifecycleSpecDigest != jobDigest || scope.LifecycleSecretDigest != secretDigest {
		return nil, objects, owned.ErrLifecycleAuthority
	}
	ns, err := client.Kube.CoreV1().Namespaces().Get(ctx, run.Namespace, metav1.GetOptions{})
	if err != nil {
		return nil, objects, err
	}
	if string(ns.UID) != run.NamespaceUID {
		return nil, objects, owned.ErrLifecycleAuthority
	}
	return client, objects, nil
}
func lifecycleObjectMatches(object, expected metav1.Object) bool {
	if object.GetName() != expected.GetName() || object.GetNamespace() != expected.GetNamespace() || object.GetAnnotations()[lifecycleDigestAnnotation] != expected.GetAnnotations()[lifecycleDigestAnnotation] {
		return false
	}
	for key, value := range expected.GetLabels() {
		if object.GetLabels()[key] != value {
			return false
		}
	}
	return object.GetUID() != ""
}
func (r *LifecycleRuntime) CreateLifecycleJob(ctx context.Context, run owned.LifecycleJobRun, attempt int) (*owned.LifecycleJobReceipt, error) {
	c, wanted, err := r.session(ctx, run, attempt)
	if err != nil {
		return nil, err
	}
	jobs, secrets := c.Kube.BatchV1().Jobs(run.Namespace), c.Kube.CoreV1().Secrets(run.Namespace)
	secret, err := secrets.Get(ctx, wanted.Secret.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		secret, err = secrets.Create(ctx, wanted.Secret, metav1.CreateOptions{})
	}
	if err != nil {
		return nil, err
	}
	if !lifecycleObjectMatches(secret, wanted.Secret) {
		return nil, owned.ErrLifecycleAuthority
	}
	_, wantSecret := wanted.Digests()
	actualSecret, _ := json.Marshal(secret.Data) //nolint:errcheck // byte maps always encode
	if fmt.Sprintf("sha256:%x", sha256.Sum256(actualSecret)) != wantSecret {
		return nil, owned.ErrLifecycleAuthority
	}
	job, err := jobs.Get(ctx, wanted.Job.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		job, err = jobs.Create(ctx, wanted.Job, metav1.CreateOptions{})
	}
	if err != nil {
		return nil, err
	}
	return lifecycleObservation(run, attempt, job, secret, wanted)
}
func (r *LifecycleRuntime) ObserveLifecycleJob(ctx context.Context, run owned.LifecycleJobRun, attempt int) (*owned.LifecycleJobReceipt, error) {
	c, wanted, err := r.session(ctx, run, attempt)
	if err != nil {
		return nil, err
	}
	job, err := c.Kube.BatchV1().Jobs(run.Namespace).Get(ctx, wanted.Job.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return r.recoverUnissuedJob(ctx, c, run, attempt)
	}
	if err != nil {
		return nil, err
	}
	secret, err := c.Kube.CoreV1().Secrets(run.Namespace).Get(ctx, wanted.Secret.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, owned.ErrLifecyclePending
	}
	if err != nil {
		return nil, err
	}
	return lifecycleObservation(run, attempt, job, secret, wanted)
}
func lifecycleObservation(run owned.LifecycleJobRun, attempt int, job *batchv1.Job, secret *corev1.Secret, wanted LifecycleObjects) (*owned.LifecycleJobReceipt, error) {
	if !lifecycleObjectMatches(job, wanted.Job) || !lifecycleObjectMatches(secret, wanted.Secret) || !lifecycleSpecMatches(job, wanted.Job) {
		return nil, owned.ErrLifecycleAuthority
	}
	receipt := &owned.LifecycleJobReceipt{RunID: run.ID(), Digest: run.Digest(), Name: job.Name, NamespaceUID: run.NamespaceUID, JobUID: string(job.UID), SecretUID: string(secret.UID), Attempt: attempt, State: "pending"}
	// Terminal conditions, not an individual pod's exit, establish that no retry
	// pod is still active. Kubernetes backoff is disabled for these jobs.
	for _, condition := range job.Status.Conditions {
		if condition.Status != corev1.ConditionTrue {
			continue
		}
		switch condition.Type {
		case batchv1.JobComplete:
			receipt.State = "succeeded"
		case batchv1.JobFailed:
			receipt.State = "failed"
			if condition.Reason == batchv1.JobReasonDeadlineExceeded {
				receipt.Reason = "deadline_exceeded"
			} else {
				receipt.Reason = "job_failed"
			}
		}
	}
	return receipt, nil
}
func (r *LifecycleRuntime) CleanupLifecycleJob(ctx context.Context, run owned.LifecycleJobRun, receipt owned.LifecycleJobReceipt) error {
	if !receipt.Terminal() || receipt.RunID != run.ID() || receipt.Digest != run.Digest() || receipt.NamespaceUID != run.NamespaceUID || receipt.JobUID == "" || receipt.SecretUID == "" || receipt.Name != run.AttemptName(receipt.Attempt) {
		return owned.ErrLifecycleAuthority
	}
	c, wanted, err := r.session(ctx, run, receipt.Attempt)
	if err != nil {
		return err
	}
	jobs := c.Kube.BatchV1().Jobs(run.Namespace)
	job, err := jobs.Get(ctx, wanted.Job.Name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return err
	default:
		if !lifecycleObjectMatches(job, wanted.Job) || string(job.UID) != receipt.JobUID {
			return owned.ErrLifecycleAuthority
		}
		options := lifecycleDeleteOptions(job)
		policy := metav1.DeletePropagationForeground
		options.PropagationPolicy = &policy
		if err = jobs.Delete(ctx, job.Name, options); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if _, err = jobs.Get(ctx, job.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			return errors.Join(owned.ErrLifecyclePending, err)
		}
	}
	secrets := c.Kube.CoreV1().Secrets(run.Namespace)
	secret, err := secrets.Get(ctx, wanted.Secret.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !lifecycleObjectMatches(secret, wanted.Secret) || string(secret.UID) != receipt.SecretUID {
		return owned.ErrLifecycleAuthority
	}
	if err = secrets.Delete(ctx, secret.Name, lifecycleDeleteOptions(secret)); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if _, err = secrets.Get(ctx, secret.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		return errors.Join(owned.ErrLifecyclePending, err)
	}
	return nil
}

func lifecycleDeleteOptions(object metav1.Object) metav1.DeleteOptions {
	options := deleteOwnedOptions(object)
	if options.Preconditions != nil {
		version := object.GetResourceVersion()
		options.Preconditions.ResourceVersion = &version
	}
	return options
}
