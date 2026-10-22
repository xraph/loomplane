package kubernetes

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"regexp"
	"time"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

// labelJob names the job a Job and its runtime Secret run, so a retry
// finds what an earlier attempt left.
const labelJob = "loomplane.job"

// jobName is a job's name: "lp-job-" and twelve hex digits.
var jobName = regexp.MustCompile(`^lp-job-[0-9a-f]{12}$`)

// jobTTL is how long the cluster keeps a finished Job that Loomplane
// couldn't delete, in seconds.
const jobTTL = 3600

var _ owned.JobBackend = (*Runtime)(nil)

// checkJobPermissions reports "jobs" ready when the connection can run
// catalog jobs in its namespace: create, read, list and delete Jobs, and
// read their pods' logs. Without them the detail is absent; ordinary
// workloads don't need them.
func (c *Client) checkJobPermissions(ctx context.Context, out *Result) error {
	delete(out.Details, "jobs")
	for _, requirement := range []struct {
		group, resource string
		verbs           []string
	}{
		{"batch", "jobs", []string{"create", "get", "list", "delete"}},
		{"", "pods/log", []string{"get"}},
	} {
		for _, verb := range requirement.verbs {
			allowed, err := c.allowed(ctx, requirement.group, requirement.resource, verb)
			if err != nil {
				return err
			}
			if !allowed {
				return nil
			}
		}
	}
	out.Details["jobs"] = "ready"
	return nil
}

// jobLabels are a job's identity: its organization, project and
// environment, as a workload's, and its name.
func jobLabels(name string, labels map[string]string) map[string]string {
	identity := objectLabels(id.Nil, labels)
	delete(identity, owned.LabelInstance)
	identity[labelJob] = name
	return identity
}

func checkJobOwner(object metav1.Object, identity map[string]string) error {
	for key, value := range identity {
		if value == "" || object.GetLabels()[key] != value {
			return ErrOwned
		}
	}
	return nil
}

// RunJob runs a one-shot container as a Job in the connection's namespace:
// one pod that never restarts, stopped by the cluster at timeout, with
// its secret values in a runtime Secret as a workload's are. It polls the
// Job each second, reads the end of its pod's log once it's done, and
// deletes the Job and the Secret however it ends.
func (r *Runtime) RunJob(ctx context.Context, name string, spec cpprovider.ServiceSpec, labels map[string]string, timeout time.Duration) (result owned.JobResult, err error) {
	if !jobName.MatchString(name) || timeout <= 0 {
		return result, errors.New("a job needs a job name and a timeout")
	}
	if len(spec.Volumes) > 0 || len(spec.Ports) > 0 || labels[owned.LabelPublic] == "true" || labels[owned.LabelStateful] == "true" {
		return result, errors.New("a job runs without volumes or ports")
	}
	component := cmp.Or(labels[owned.LabelCatalogComponent], name)
	c, err := r.Session(ctx, owned.ForOperation(labels, "apply"))
	if err != nil {
		return result, err
	}
	if c.Config.Details["jobs"] != "ready" {
		return result, errors.New("this cluster connection can't run catalog jobs: it needs to create, read, list and delete Jobs and read pod logs in its namespace; check the connection")
	}
	if checkErr := c.CheckPullSecret(ctx); checkErr != nil {
		return result, checkErr
	}
	values := owned.RuntimeValues{}
	if r.Values != nil {
		if values, err = r.Values(ctx, labels); err != nil {
			return result, err
		}
	}
	if values.PullAuth != "" {
		return result, errors.New("private Kubernetes registry credentials require an existing image_pull_secret; native bearer pull Secrets are unsupported")
	}
	ns := c.Config.Namespace
	identity := jobLabels(name, labels)
	jobs, secrets := c.Kube.BatchV1().Jobs(ns), c.Kube.CoreV1().Secrets(ns)
	// An earlier attempt that never finished can leave its Job.
	if removeErr := r.removeLeftoverJob(ctx, c, name, identity); removeErr != nil {
		return result, removeErr
	}
	meta := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: ns, Labels: maps.Clone(identity)}
	}
	runtimeSecret := &corev1.Secret{ObjectMeta: meta(name + "-runtime"), Type: corev1.SecretTypeOpaque, Data: map[string][]byte{}}
	for key, value := range values.Env {
		runtimeSecret.Data[key] = []byte(value)
	}
	old, err := secrets.Get(ctx, runtimeSecret.Name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return result, err
	case checkJobOwner(old, identity) != nil:
		return result, ErrOwned
	default:
		// An earlier attempt's. This attempt makes its own, which only its
		// own cleanup deletes.
		if deleteErr := secrets.Delete(ctx, old.Name, deleteOwnedOptions(old)); deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
			return result, deleteErr
		}
	}
	kept, err := secrets.Create(ctx, runtimeSecret, metav1.CreateOptions{})
	if err != nil {
		return result, err
	}
	var created *batchv1.Job
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if created != nil {
			options := deleteOwnedOptions(created)
			policy := metav1.DeletePropagationBackground
			options.PropagationPolicy = &policy
			if deleteErr := jobs.Delete(cleanup, name, options); deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
				err = errors.Join(err, fmt.Errorf("delete the %s job: %w", component, deleteErr))
			}
		}
		if deleteErr := secrets.Delete(cleanup, kept.Name, deleteOwnedOptions(kept)); deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
			err = errors.Join(err, fmt.Errorf("delete the %s job's runtime values: %w", component, deleteErr))
		}
	}()
	pulls := []corev1.LocalObjectReference{}
	if selected := c.Config.Details["image_pull_secret"]; selected != "" {
		pulls = append(pulls, corev1.LocalObjectReference{Name: selected})
	}
	template, _, err := runtimePod(spec, labels, values, runtimeSecret, pulls)
	if err != nil {
		return result, err
	}
	template.Labels = maps.Clone(identity)
	template.Spec.RestartPolicy = corev1.RestartPolicyNever
	backoff, ttl := int32(0), int32(jobTTL)
	deadline := int64(math.Ceil(timeout.Seconds()))
	job := &batchv1.Job{ObjectMeta: meta(name), Spec: batchv1.JobSpec{BackoffLimit: &backoff, ActiveDeadlineSeconds: &deadline, TTLSecondsAfterFinished: &ttl, Template: *template}}
	if created, err = jobs.Create(ctx, job, metav1.CreateOptions{}); err != nil {
		created = nil
		return result, err
	}
	// The Job owns its runtime values, so the cluster deletes them with
	// it when Loomplane can't.
	kept.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: created.Name, UID: created.UID}}
	updated, err := secrets.Update(ctx, kept, metav1.UpdateOptions{})
	if err != nil {
		return result, err
	}
	kept = updated
	finished, late, err := r.waitJob(ctx, c, created, component, timeout)
	if err != nil {
		return result, err
	}
	exitCode, output := r.jobPod(ctx, c, finished, spec.Name, values.Env)
	switch {
	case late:
		return result, jobLate(component, timeout, output)
	case succeeded(finished):
		return owned.JobResult{ExitCode: 0, Output: output}, nil
	case exitCode == 0:
		return result, fmt.Errorf("the %s job failed: %s", component, output)
	}
	return owned.JobResult{ExitCode: exitCode, Output: output}, nil
}

// jobLate is the error of a job that ran out of time, with what it wrote.
func jobLate(component string, timeout time.Duration, output string) error {
	if output == "" {
		return fmt.Errorf("%s did not finish within %s", component, timeout)
	}
	return fmt.Errorf("%s did not finish within %s: %s", component, timeout, output)
}

func succeeded(job *batchv1.Job) bool {
	if job.Status.Succeeded > 0 {
		return true
	}
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// waitJob polls a Job each second until it succeeds or fails. late is a
// Job the cluster stopped at its deadline, or still running well past it.
// A failed read is tried again until then.
func (r *Runtime) waitJob(ctx context.Context, c *Client, created *batchv1.Job, component string, timeout time.Duration) (job *batchv1.Job, late bool, err error) {
	deadline := time.Now().Add(timeout + time.Minute)
	for {
		job, err = c.Kube.BatchV1().Jobs(created.Namespace).Get(ctx, created.Name, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err) || err == nil && created.UID != "" && job.UID != created.UID:
			return nil, false, fmt.Errorf("the %s job was deleted before it finished", component)
		case err != nil:
			if time.Now().After(deadline) {
				return nil, false, err
			}
		case succeeded(job):
			return job, false, nil
		default:
			for _, condition := range job.Status.Conditions {
				if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
					return job, condition.Reason == batchv1.JobReasonDeadlineExceeded, nil
				}
			}
			if job.Status.Failed > 0 {
				return job, false, nil
			}
			if time.Now().After(deadline) {
				return job, true, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// jobPod reads how a finished Job's pod ended: its container's exit code
// and the end of its log, redacted. A log that can't be read says so,
// rather than fail a job that ran.
func (r *Runtime) jobPod(ctx context.Context, c *Client, job *batchv1.Job, container string, secrets map[string]string) (exitCode int, output string) {
	unread := func(err error) string {
		return owned.JobOutput([]byte("(its output couldn't be read: "+err.Error()+")"), secrets)
	}
	pods, err := c.Kube.CoreV1().Pods(job.Namespace).List(ctx, metav1.ListOptions{LabelSelector: labelJob + "=" + job.Name})
	if err != nil {
		return 0, unread(err)
	}
	var pod *corev1.Pod
	for i := range pods.Items {
		candidate := &pods.Items[i]
		if !ownedBy(candidate, job) {
			continue
		}
		if pod == nil || pod.CreationTimestamp.Before(&candidate.CreationTimestamp) {
			pod = candidate
		}
	}
	if pod == nil {
		return 0, "(its pod is gone, so its output couldn't be read)"
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == container && status.State.Terminated != nil {
			exitCode = int(status.State.Terminated.ExitCode)
		}
	}
	lines := int64(2000)
	stream, err := c.Kube.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: container, TailLines: &lines}).Stream(ctx)
	if err != nil {
		return exitCode, unread(err)
	}
	defer stream.Close()
	tail := owned.NewJobTail(secrets)
	if _, err = io.Copy(tail, stream); err != nil {
		return exitCode, unread(err)
	}
	return exitCode, tail.Output(secrets)
}

func ownedBy(pod *corev1.Pod, job *batchv1.Job) bool {
	for _, owner := range pod.OwnerReferences {
		if owner.Kind == "Job" && owner.Name == job.Name && (job.UID == "" || owner.UID == job.UID) {
			return true
		}
	}
	return false
}

// removeLeftoverJob deletes a Job this job's earlier attempt left, and
// waits for it to go, and refuses one that's someone else's.
func (r *Runtime) removeLeftoverJob(ctx context.Context, c *Client, name string, identity map[string]string) error {
	jobs := c.Kube.BatchV1().Jobs(c.Config.Namespace)
	old, err := jobs.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if checkErr := checkJobOwner(old, identity); checkErr != nil {
		return checkErr
	}
	options := deleteOwnedOptions(old)
	policy := metav1.DeletePropagationBackground
	options.PropagationPolicy = &policy
	if deleteErr := jobs.Delete(ctx, name, options); deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
		return deleteErr
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, getErr := jobs.Get(ctx, name, metav1.GetOptions{}); apierrors.IsNotFound(getErr) {
			return nil
		} else if getErr != nil {
			return getErr
		}
		if time.Now().After(deadline) {
			return errors.New("an earlier attempt's job is still being deleted; retry the deployment")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
