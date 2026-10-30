package kubernetes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

const buildkitImage = "docker.io/moby/buildkit:v0.34.1-rootless@sha256:f68752a5a94acfcdad2d07867b7075601a421638e17fc5f28efc1c814d0c4f89"
const buildOperationLabel = "loomplane.build"

var buildDigest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var buildImageName = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*:[a-f0-9]{24}$`)

// ClusterImageBuild receives the approved, filtered source archive. No Git
// credential or application runtime secret enters the build pod.
type ClusterImageBuild struct {
	Org, Connection, Operation, Dockerfile, Image string
	Source                                        io.Reader
}

// ClusterBuilder runs one rootless BuildKit pod per attempt, with Kata when selected.
type ClusterBuilder struct {
	Client *Client
	Exec   func(context.Context, *Client, *corev1.Pod, []string, io.Reader, io.Writer, string) error
}

func (b *ClusterBuilder) Build(ctx context.Context, in ClusterImageBuild, registry *ManagedRegistry, output io.Writer) (digest string, err error) {
	c := b.Client
	if c == nil || in.Operation == "" || in.Source == nil || in.Dockerfile != ".loomplane-build/Dockerfile" || registry == nil || !strings.HasPrefix(in.Image, registry.Host+"/loomplane/") || !buildImageName.MatchString(in.Image) {
		return "", errors.New("invalid reviewed Kubernetes build inputs")
	}
	if checkErr := c.verifyBuildIsolation(ctx); checkErr != nil {
		return "", checkErr
	}
	architecture := c.Config.Details["architecture"]
	if architecture != "amd64" && architecture != "arm64" {
		return "", errors.New("managed builds need an explicit amd64 or arm64 architecture")
	}
	name, identity, err := registryIdentity(in.Org, in.Connection, c.Config.Details["namespace_uid"])
	if err != nil {
		return "", err
	}
	if name != registry.Name || registry.Host != c.Config.Details["registry_hostname"] {
		return "", ErrOwned
	}
	sum := sha256.Sum256([]byte(in.Operation))
	podName := fmt.Sprintf("lp-build-%x", sum[:12])
	identity[buildOperationLabel] = podName
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	pods := c.Kube.CoreV1().Pods(c.Config.Namespace)
	jobs := c.Kube.BatchV1().Jobs(c.Config.Namespace)
	// A crash can leave the old pod until its active deadline. Never run two
	// commands against it; delete the owned attempt before retrying.
	old, getErr := jobs.Get(ctx, podName, metav1.GetOptions{})
	if getErr == nil {
		if checkErr := managedOwner(old, identity, registry.Host); checkErr != nil {
			return "", checkErr
		}
		options := deleteOwnedOptions(old)
		options.PropagationPolicy = ptr.To(metav1.DeletePropagationForeground)
		if checkErr := jobs.Delete(ctx, podName, options); checkErr != nil && !apierrors.IsNotFound(checkErr) {
			return "", checkErr
		}
		if checkErr := pollBuild(ctx, func() (bool, error) {
			_, e := jobs.Get(ctx, podName, metav1.GetOptions{})
			if apierrors.IsNotFound(e) {
				return true, nil
			}
			return false, e
		}); checkErr != nil {
			return "", checkErr
		}
	} else if !apierrors.IsNotFound(getErr) {
		return "", getErr
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: c.Config.Namespace, Labels: maps.Clone(identity), Annotations: map[string]string{managedRegistryHost: registry.Host}},
		Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, ActiveDeadlineSeconds: ptr.To(int64(1800)), AutomountServiceAccountToken: ptr.To(false), NodeSelector: map[string]string{"kubernetes.io/os": "linux", "kubernetes.io/arch": architecture},
			SecurityContext: &corev1.PodSecurityContext{RunAsUser: ptr.To(int64(1000)), RunAsGroup: ptr.To(int64(1000)), FSGroup: ptr.To(int64(1000)), RunAsNonRoot: ptr.To(true)},
			Containers: []corev1.Container{{Name: "buildkit", Image: buildkitImage, Command: []string{"sh", "-c", "sleep 1800"},
				Env:             []corev1.EnvVar{podIdentityEnv(), {Name: "DOCKER_CONFIG", Value: "/registry"}, {Name: "BUILDKITD_FLAGS", Value: "--oci-worker-no-process-sandbox --oci-worker-snapshotter=native"}},
				SecurityContext: &corev1.SecurityContext{SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined}, AppArmorProfile: &corev1.AppArmorProfile{Type: corev1.AppArmorProfileTypeUnconfined}},
				Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("512Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("4Gi"), corev1.ResourceEphemeralStorage: resource.MustParse("20Gi")}},
				VolumeMounts:    []corev1.VolumeMount{{Name: "source", MountPath: "/workspace"}, {Name: "state", MountPath: "/home/user/.local/share/buildkit"}, {Name: "registry", MountPath: "/registry", ReadOnly: true}}}},
			Volumes: []corev1.Volume{{Name: "source", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: ptr.To(resource.MustParse("2Gi"))}}}, {Name: "state", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: ptr.To(resource.MustParse("18Gi"))}}}, {Name: "registry", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: registry.Name + "-auth", Items: []corev1.KeyToPath{{Key: "config.json", Path: "config.json"}}, DefaultMode: ptr.To(int32(0o440))}}}}}}
	if class := c.Config.Details["build_runtime_class"]; class != "" {
		pod.Spec.RuntimeClassName = ptr.To(class)
	}
	job := &batchv1.Job{ObjectMeta: pod.ObjectMeta, Spec: batchv1.JobSpec{BackoffLimit: ptr.To(int32(0)), ActiveDeadlineSeconds: ptr.To(int64(1800)), TTLSecondsAfterFinished: ptr.To(int32(3600)), Template: corev1.PodTemplateSpec{ObjectMeta: pod.ObjectMeta, Spec: pod.Spec}}}
	job, err = jobs.Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return "", apiReason(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer stop()
		options := deleteOwnedOptions(job)
		options.PropagationPolicy = ptr.To(metav1.DeletePropagationBackground)
		removeErr := jobs.Delete(cleanup, podName, options)
		if !apierrors.IsNotFound(removeErr) {
			err = errors.Join(err, removeErr)
		}
	}()
	if checkErr := pollBuild(ctx, func() (bool, error) {
		currentJob, e := jobs.Get(ctx, podName, metav1.GetOptions{})
		if e != nil {
			return false, e
		}
		if currentJob.UID != job.UID || managedOwner(currentJob, identity, registry.Host) != nil {
			return false, ErrOwned
		}
		for _, condition := range currentJob.Status.Conditions {
			if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
				return false, errors.New("the build job failed before accepting source")
			}
		}
		list, e := pods.List(ctx, metav1.ListOptions{LabelSelector: buildOperationLabel + "=" + podName})
		if e != nil {
			return false, e
		}
		for i := range list.Items {
			current := &list.Items[i]
			if !metav1.IsControlledBy(current, job) || managedOwner(current, identity, registry.Host) != nil {
				return false, ErrOwned
			}
			if current.Status.Phase == corev1.PodFailed || current.Status.Phase == corev1.PodSucceeded {
				return false, errors.New("the build pod stopped before accepting source")
			}
			if current.Status.Phase == corev1.PodRunning {
				if ptr.Deref(current.Spec.RuntimeClassName, "") != c.Config.Details["build_runtime_class"] || current.Spec.HostNetwork || current.Spec.HostPID || current.Spec.HostIPC {
					return false, errors.New("the admitted build pod changed its isolation settings")
				}
				pod = current
				return true, nil
			}
		}
		return false, nil
	}); checkErr != nil {
		return "", checkErr
	}
	if checkErr := c.verifyBuildIsolation(ctx); checkErr != nil {
		return "", checkErr
	}
	exec := b.Exec
	if exec == nil {
		exec = execPodAs
	}
	if checkErr := exec(ctx, c, pod, []string{"tar", "-xf", "-", "-C", "/workspace"}, in.Source, io.Discard, "image build"); checkErr != nil {
		return "", checkErr
	}
	command := `buildctl-daemonless.sh build --frontend dockerfile.v0 --local context=/workspace --local dockerfile=/workspace --opt "filename=$1" --opt "platform=linux/$2" --output "type=image,name=$3,push=true,oci-mediatypes=true" --metadata-file /tmp/loomplane-result.json --progress plain 2>&1`
	if checkErr := exec(ctx, c, pod, []string{"sh", "-c", command, "loomplane-build", in.Dockerfile, architecture, in.Image}, nil, output, "image build"); checkErr != nil {
		return "", checkErr
	}
	var result limitedBuildResult
	if checkErr := exec(ctx, c, pod, []string{"cat", "/tmp/loomplane-result.json"}, nil, &result, "image build"); checkErr != nil {
		return "", checkErr
	}
	var metadata struct {
		Digest string `json:"containerimage.digest"`
	}
	if json.Unmarshal(result.Bytes(), &metadata) != nil || !buildDigest.MatchString(metadata.Digest) {
		return "", errors.New("BuildKit did not return a published image digest")
	}
	return metadata.Digest, nil
}

type limitedBuildResult struct{ bytes.Buffer }

func (b *limitedBuildResult) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 64<<10 {
		return 0, errors.New("build metadata exceeds 64 KiB")
	}
	return b.Buffer.Write(p)
}
