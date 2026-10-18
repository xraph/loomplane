package kubernetes

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/xraph/ctrlplane/id"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/owned"
	nativepg "github.com/xraph/loomplane/internal/clouds/postgres"
)

const imageReferenceLabel = "loomplane.image.ref"
const recoveryTargetLabel = "loomplane.recovery.target"
const recoveryProbeLabel = "loomplane.recovery.probe"
const podUIDEnv = "LOOMPLANE_POD_UID"

func podIdentityEnv() corev1.EnvVar {
	return corev1.EnvVar{Name: podUIDEnv, ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.uid"}}}
}
func podCommand(uid string, args []string) []string {
	// Positional arguments stay separate from this fixed identity guard. A pod
	// recreated between the API read and exec cannot receive the native command.
	command := make([]string, 5, 5+len(args))
	copy(command, []string{"sh", "-c", `[ "$LOOMPLANE_POD_UID" = "$1" ] || exit 78; shift; exec "$@"`, "loomplane-exec", uid})
	return append(command, args...)
}

// postgresPod binds every exec to one checked pod, not merely its reusable name.
type postgresPod struct {
	client *Client
	pod    *corev1.Pod
	parent *appsv1.StatefulSet
	labels map[string]string
}

func (r *Runtime) postgresPod(ctx context.Context, iid id.ID, labels map[string]string) (*postgresPod, error) {
	if !nativepg.ValidDatabase(labels[nativepg.LabelDatabase]) || labels[owned.LabelStateful] != "true" || labels[imageReferenceLabel] == "" {
		return nil, errors.New("invalid PostgreSQL runtime identity")
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
	if set.UID == "" || set.DeletionTimestamp != nil || len(set.Spec.Template.Spec.Containers) != 1 || set.Spec.Template.Spec.Containers[0].Image != labels[imageReferenceLabel] || set.Labels[nativepg.LabelDatabase] != labels[nativepg.LabelDatabase] || set.Labels[nativepg.LabelRecovery] != labels[nativepg.LabelRecovery] {
		return nil, ErrOwned
	}
	pod, err := c.Kube.CoreV1().Pods(c.Config.Namespace).Get(ctx, set.Name+"-0", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if checkErr := checkOwner(pod, iid, labels); checkErr != nil {
		return nil, checkErr
	}
	if !metav1.IsControlledBy(pod, set) || pod.Labels[nativepg.LabelDatabase] != labels[nativepg.LabelDatabase] || pod.Labels[nativepg.LabelRecovery] != labels[nativepg.LabelRecovery] || len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Image != labels[imageReferenceLabel] || pod.Spec.HostNetwork || pod.Spec.HostPID || pod.Spec.HostIPC || pod.DeletionTimestamp != nil || pod.UID == "" || pod.Status.Phase != corev1.PodRunning {
		return nil, ErrOwned
	}
	found := false
	for _, env := range pod.Spec.Containers[0].Env {
		if env.Name == "POSTGRES_DB" && env.Value == labels[nativepg.LabelDatabase] && env.ValueFrom == nil {
			found = true
		}
	}
	if !found {
		return nil, ErrOwned
	}
	return &postgresPod{client: c, pod: pod, parent: set, labels: labels}, nil
}
func checkPodSnapshot(actual, expected *corev1.Pod) error {
	if actual.UID != expected.UID || actual.DeletionTimestamp != nil || actual.Status.Phase != corev1.PodRunning || !reflect.DeepEqual(actual.OwnerReferences, expected.OwnerReferences) || !reflect.DeepEqual(actual.Labels, expected.Labels) || !reflect.DeepEqual(actual.Spec.Containers, expected.Spec.Containers) || actual.Spec.HostNetwork || actual.Spec.HostPID || actual.Spec.HostIPC {
		return ErrOwned
	}
	return nil
}
func execPod(ctx context.Context, c *Client, pod *corev1.Pod, args []string, input io.Reader, out io.Writer) error {
	current, err := c.Kube.CoreV1().Pods(c.Config.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if checkErr := checkPodSnapshot(current, pod); checkErr != nil {
		return checkErr
	}
	if c.Config.REST == nil || len(pod.Spec.Containers) != 1 {
		return ErrInvalid
	}
	identityEnv := false
	for _, env := range pod.Spec.Containers[0].Env {
		if env.Name == podUIDEnv && env.Value == "" && env.ValueFrom != nil && env.ValueFrom.FieldRef != nil && env.ValueFrom.FieldRef.FieldPath == "metadata.uid" {
			identityEnv = true
		}
	}
	if !identityEnv {
		return ErrOwned
	}
	request := c.Kube.CoreV1().RESTClient().Post().Namespace(c.Config.Namespace).Resource("pods").Name(pod.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: pod.Spec.Containers[0].Name, Command: podCommand(string(pod.UID), args), Stdin: input != nil, Stdout: true, Stderr: true, TTY: false}, scheme.ParameterCodec)
	cfg := rest.CopyConfig(c.Config.REST)
	cfg.Timeout = 0
	executor, err := remotecommand.NewSPDYExecutor(cfg, http.MethodPost, request.URL())
	if err != nil {
		return err
	}
	if err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: input, Stdout: out, Stderr: io.Discard, Tty: false}); err != nil {
		return fmt.Errorf("native PostgreSQL command failed: %w", err)
	}
	return nil
}
func (s *postgresPod) run(ctx context.Context, args []string, out io.Writer) error {
	return execPod(ctx, s.client, s.pod, args, nil, out)
}
func (s *postgresPod) upload(ctx context.Context, name string, size int64, write func(io.Writer) error) error {
	if name == "" || strings.ContainsAny(name, "/\\\x00") || size < 0 {
		return errors.New("invalid PostgreSQL temporary file")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		archive := tar.NewWriter(writer)
		err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: size, Typeflag: tar.TypeReg})
		if err == nil {
			err = write(archive)
		}
		if err == nil {
			err = archive.Close()
		}
		writer.Close()
		done <- err
	}()
	err := execPod(ctx, s.client, s.pod, []string{"tar", "-xf", "-", "-C", "/tmp"}, reader, io.Discard)
	reader.CloseWithError(err)
	cancel()
	return errors.Join(err, <-done)
}
func (s *postgresPod) database() nativepg.Database {
	return nativepg.Database{Name: s.labels[nativepg.LabelDatabase], Recovery: s.labels[nativepg.LabelRecovery] != "", Run: s.run, Upload: s.upload}
}
func (r *Runtime) ConfigurePostgres(ctx context.Context, iid id.ID, labels map[string]string, password string) error {
	if err := nativepg.ValidatePassword(password); err != nil {
		return err
	}
	s, err := r.postgresPod(ctx, iid, labels)
	if err != nil {
		return err
	}
	return s.database().Configure(ctx, password)
}
func (r *Runtime) BackupPostgres(ctx context.Context, iid id.ID, labels map[string]string, files *backupfiles.Store, key string) (backupfiles.Object, error) {
	if labels[nativepg.LabelRecovery] != "" {
		return backupfiles.Object{}, errors.New("a recovery copy cannot be used as a live backup source")
	}
	s, err := r.postgresPod(ctx, iid, labels)
	if err != nil {
		return backupfiles.Object{}, err
	}
	return s.database().Export(ctx, files, key)
}
func (r *Runtime) RestorePostgres(ctx context.Context, iid id.ID, labels map[string]string, files *backupfiles.Store, object backupfiles.Object) error {
	if labels[nativepg.LabelRecovery] == "" || labels[owned.LabelPublic] != "false" {
		return errors.New("restore requires a private recovery instance")
	}
	s, err := r.postgresPod(ctx, iid, labels)
	if err != nil {
		return err
	}
	if checkErr := r.verifyRecoveryIsolation(ctx, s); checkErr != nil {
		return checkErr
	}
	return s.database().Restore(ctx, files, object)
}
func (r *Runtime) RestoreProbe(ctx context.Context, iid id.ID, labels map[string]string) error {
	s, err := r.postgresPod(ctx, iid, labels)
	if err != nil {
		return err
	}
	return s.database().Probe(ctx)
}
func deleteUID(uid types.UID) metav1.DeleteOptions {
	return metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}
}
