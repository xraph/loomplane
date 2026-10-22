package kubernetes

import (
	"context"
	"errors"
	"io"

	"github.com/xraph/ctrlplane/id"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/clouds/owned"
	nativepg "github.com/xraph/loomplane/internal/clouds/postgres"
	nativecache "github.com/xraph/loomplane/internal/clouds/redis"
)

// ErrCacheBackupsUnavailable is what backing up, restoring or probing a
// recovery copy of Redis or Valkey on Kubernetes returns. Caches run there
// only as another kind's dependency, and Loomplane has no qualified
// cluster recovery adapter for them.
var ErrCacheBackupsUnavailable = errors.New("backups for Redis and Valkey aren't available on Kubernetes yet")

// cacheExec runs one command in a checked cache pod. Tests replace it,
// since a fake clientset has no exec.
var cacheExec = execPod

// cachePod binds every exec to one checked pod, as postgresPod does.
type cachePod struct {
	client *Client
	pod    *corev1.Pod
	engine string
}

func (r *Runtime) cachePod(ctx context.Context, iid id.ID, labels map[string]string) (*cachePod, error) {
	engine := labels[nativecache.LabelEngine]
	if !nativecache.ValidEngine(engine) || labels[owned.LabelStateful] != "true" || labels[imageReferenceLabel] == "" || labels[nativepg.LabelDatabase] != "" || labels[nativepg.LabelRecovery] != "" {
		return nil, errors.New("invalid Redis/Valkey runtime identity")
	}
	c, err := r.Session(ctx, owned.ForOperation(labels, "read"))
	if err != nil {
		return nil, err
	}
	set, err := c.Kube.AppsV1().StatefulSets(c.Config.Namespace).Get(ctx, WorkloadName(iid), metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if checkErr := checkOwner(set, iid, labels); checkErr != nil {
		return nil, checkErr
	}
	if set.UID == "" || set.DeletionTimestamp != nil || len(set.Spec.Template.Spec.Containers) != 1 || set.Spec.Template.Spec.Containers[0].Image != labels[imageReferenceLabel] || set.Labels[nativecache.LabelEngine] != engine || set.Labels[nativepg.LabelDatabase] != "" || set.Labels[nativepg.LabelRecovery] != "" {
		return nil, ErrOwned
	}
	pod, err := c.Kube.CoreV1().Pods(c.Config.Namespace).Get(ctx, set.Name+"-0", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if checkErr := checkOwner(pod, iid, labels); checkErr != nil {
		return nil, checkErr
	}
	if !metav1.IsControlledBy(pod, set) || pod.Labels[nativecache.LabelEngine] != engine || len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Image != labels[imageReferenceLabel] || pod.Spec.HostNetwork || pod.Spec.HostPID || pod.Spec.HostIPC || pod.DeletionTimestamp != nil || pod.UID == "" || pod.Status.Phase != corev1.PodRunning {
		return nil, ErrOwned
	}
	return &cachePod{client: c, pod: pod, engine: engine}, nil
}

func (s *cachePod) database() nativecache.Database {
	run := func(ctx context.Context, args []string, input io.Reader, out io.Writer) error {
		return cacheExec(ctx, s.client, s.pod, args, input, out)
	}
	return nativecache.Database{Engine: s.engine, Run: func(ctx context.Context, args []string, out io.Writer) error {
		return run(ctx, args, nil, out)
	}, Upload: func(ctx context.Context, name string, size int64, write func(io.Writer) error) error {
		return uploadTemporary(ctx, run, name, size, write)
	}}
}

// configureCache checks the application user's password over pod exec,
// as the server runtime does over Docker exec.
func (r *Runtime) configureCache(ctx context.Context, iid id.ID, labels map[string]string, password string) error {
	s, err := r.cachePod(ctx, iid, labels)
	if err != nil {
		return err
	}
	return s.database().Configure(ctx, password)
}
