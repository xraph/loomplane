package kubernetes

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"reflect"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/utils/ptr"

	"github.com/xraph/loomplane/internal/backupfiles"
)

const registryMaintenance = "loomplane.registry.maintenance"

type RegistryMaintenanceSession struct {
	client     *Client
	deployment *appsv1.Deployment
	pod        *corev1.Pod
	identity   map[string]string
	operation  string
}

// BeginRegistryMaintenance stops writes before mounting the image filesystem.
// The deployment records the operation ID so a crashed worker resumes offline.
func (c *Client) BeginRegistryMaintenance(ctx context.Context, org, connection, operation string) (*RegistryMaintenanceSession, error) {
	name, identity, err := registryIdentity(org, connection, c.Config.Details["namespace_uid"])
	if err != nil {
		return nil, err
	}
	api := c.Kube.AppsV1().Deployments(c.Config.Namespace)
	d, err := api.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if operationErr := managedOwner(d, identity, c.Config.Details["registry_hostname"]); operationErr != nil {
		return nil, operationErr
	}
	if d.UID == "" || len(d.Spec.Template.Spec.Containers) != 1 || d.Spec.Template.Spec.Containers[0].Image != managedRegistryImage {
		return nil, ErrOwned
	}
	if held := d.Annotations[registryMaintenance]; held != "" && held != operation {
		return nil, ErrOwned
	}
	if d.Annotations[registryMaintenance] != operation || d.Spec.Replicas == nil || *d.Spec.Replicas != 0 {
		d.Annotations[registryMaintenance] = operation
		d.Spec.Replicas = ptr.To(int32(0))
		if _, err = api.Update(ctx, d, metav1.UpdateOptions{}); err != nil {
			return nil, err
		}
		return nil, ErrRegistryPending
	}
	pods := c.Kube.CoreV1().Pods(c.Config.Namespace)
	list, err := pods.List(ctx, metav1.ListOptions{LabelSelector: labels.SelectorFromSet(identity).String()})
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256([]byte(operation))
	helperName := fmt.Sprintf("%s-maint-%x", name, hash[:6])
	for _, p := range list.Items {
		if p.Name != helperName && (p.Status.Phase == corev1.PodPending || p.Status.Phase == corev1.PodRunning || p.DeletionTimestamp != nil) {
			return nil, ErrRegistryPending
		}
	}
	pvc, err := c.Kube.CoreV1().PersistentVolumeClaims(c.Config.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if operationErr := managedOwner(pvc, identity, c.Config.Details["registry_hostname"]); operationErr != nil {
		return nil, operationErr
	}
	if pvc.Status.Phase != corev1.ClaimBound {
		return nil, ErrRegistryPending
	}
	helper, err := pods.Get(ctx, helperName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		object := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: helperName, Namespace: c.Config.Namespace, Labels: maps.Clone(identity), Annotations: map[string]string{managedRegistryHost: c.Config.Details["registry_hostname"], registryMaintenance: operation}}, Spec: corev1.PodSpec{AutomountServiceAccountToken: ptr.To(false), RestartPolicy: corev1.RestartPolicyNever, ActiveDeadlineSeconds: ptr.To(int64(3600)), SecurityContext: d.Spec.Template.Spec.SecurityContext.DeepCopy(), Containers: []corev1.Container{{Name: "maintenance", Image: managedRegistryImage, Command: []string{"sleep", "3600"}, Env: []corev1.EnvVar{podIdentityEnv()}, SecurityContext: d.Spec.Template.Spec.Containers[0].SecurityContext.DeepCopy(), VolumeMounts: []corev1.VolumeMount{{Name: "images", MountPath: "/var/lib/registry"}}}}, Volumes: []corev1.Volume{{Name: "images", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: name}}}}}}
		_, err = pods.Create(ctx, object, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, err
		}
		return nil, ErrRegistryPending
	}
	if err != nil {
		return nil, err
	}
	if operationErr := managedOwner(helper, identity, c.Config.Details["registry_hostname"]); operationErr != nil {
		return nil, operationErr
	}
	if helper.Annotations[registryMaintenance] != operation || len(helper.Spec.Containers) != 1 || helper.Spec.Containers[0].Image != managedRegistryImage || helper.Spec.HostNetwork || helper.Spec.HostPID || helper.Spec.HostIPC || helper.UID == "" {
		return nil, ErrOwned
	}
	container := helper.Spec.Containers[0]
	if helper.Spec.AutomountServiceAccountToken == nil || *helper.Spec.AutomountServiceAccountToken ||
		len(helper.Spec.InitContainers) != 0 || len(helper.Spec.EphemeralContainers) != 0 ||
		len(helper.Spec.Volumes) != 1 || helper.Spec.Volumes[0].Name != "images" ||
		!reflect.DeepEqual(helper.Spec.Volumes[0].VolumeSource, corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: name}}) ||
		!reflect.DeepEqual(container.Command, []string{"sleep", "3600"}) || len(container.Args) != 0 ||
		!reflect.DeepEqual(container.VolumeMounts, []corev1.VolumeMount{{Name: "images", MountPath: "/var/lib/registry"}}) ||
		!reflect.DeepEqual(container.SecurityContext, d.Spec.Template.Spec.Containers[0].SecurityContext) ||
		!reflect.DeepEqual(helper.Spec.SecurityContext, d.Spec.Template.Spec.SecurityContext) {
		return nil, ErrOwned
	}
	if helper.Status.Phase == corev1.PodFailed || helper.Status.Phase == corev1.PodSucceeded {
		err = pods.Delete(ctx, helperName, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &helper.UID}})
		if err != nil {
			return nil, err
		}
		return nil, ErrRegistryPending
	}
	if helper.Status.Phase != corev1.PodRunning || helper.DeletionTimestamp != nil {
		return nil, ErrRegistryPending
	}
	return &RegistryMaintenanceSession{client: c, deployment: d, pod: helper, identity: identity, operation: operation}, nil
}

func (m *RegistryMaintenanceSession) Backup(ctx context.Context, files *backupfiles.Store, key string) (backupfiles.Object, error) {
	if files == nil {
		return backupfiles.Object{}, errors.New("backup storage is not configured")
	}
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		err := execPodAs(execCtx, m.client, m.pod, []string{"sh", "-c", "mkdir -p /var/lib/registry/docker && tar -C /var/lib/registry -cf - docker"}, nil, writer, "registry backup")
		_ = writer.CloseWithError(err)
		done <- err
	}()
	object, err := files.Put(ctx, key, reader)
	_ = reader.CloseWithError(err)
	if err != nil {
		cancel()
	}
	execErr := <-done
	return object, errors.Join(err, execErr)
}

// Restore verifies the entire object and archive paths before altering storage.
// The caller must have persisted a fresh backup of the current registry first.
func (m *RegistryMaintenanceSession) Restore(ctx context.Context, files *backupfiles.Store, object backupfiles.Object) error {
	if files == nil {
		return errors.New("backup storage is not configured")
	}
	archive, err := os.CreateTemp("", "loomplane-registry-restore-*")
	if err != nil {
		return err
	}
	defer func() { _ = archive.Close(); _ = os.Remove(archive.Name()) }()
	if operationErr := files.CopyVerified(ctx, object, archive); operationErr != nil {
		return operationErr
	}
	if _, err = archive.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if operationErr := validateRegistryArchive(tar.NewReader(archive)); operationErr != nil {
		return operationErr
	}
	if _, err = archive.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if operationErr := execPodAs(ctx, m.client, m.pod, []string{"sh", "-c", "rm -rf -- /var/lib/registry/docker"}, nil, io.Discard, "registry restore preparation"); operationErr != nil {
		return operationErr
	}
	return execPodAs(ctx, m.client, m.pod, []string{"tar", "-C", "/var/lib/registry", "-xf", "-"}, archive, io.Discard, "registry restore")
}
func validateRegistryArchive(reader *tar.Reader) error {
	for {
		entry, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(entry.Name)
		if strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\\x00") || (name != "." && name != "docker" && !strings.HasPrefix(name, "docker/")) || (entry.Typeflag != tar.TypeDir && entry.Typeflag != tar.TypeReg) {
			return errors.New("registry backup contains an unsafe archive entry")
		}
	}
}

// CollectGarbage removes blobs no manifest references. Digest-pinned manifests
// and tags remain available for current deployments and rollbacks.
func (m *RegistryMaintenanceSession) CollectGarbage(ctx context.Context) error {
	config := "version: 0.1\nstorage:\n  filesystem:\n    rootdirectory: /var/lib/registry\n"
	if operationErr := execPodAs(ctx, m.client, m.pod, []string{"sh", "-c", "mkdir -p /var/lib/registry/docker/registry/v2/repositories /var/lib/registry/docker/registry/v2/blobs; cat > /tmp/registry-gc.yml"}, strings.NewReader(config), io.Discard, "registry cleanup configuration"); operationErr != nil {
		return operationErr
	}
	return execPodAs(ctx, m.client, m.pod, []string{"registry", "garbage-collect", "/tmp/registry-gc.yml"}, nil, io.Discard, "registry image cleanup")
}

func (c *Client) EndRegistryMaintenance(ctx context.Context, org, connection, operation string) error {
	name, identity, err := registryIdentity(org, connection, c.Config.Details["namespace_uid"])
	if err != nil {
		return err
	}
	api := c.Kube.AppsV1().Deployments(c.Config.Namespace)
	d, err := api.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if operationErr := managedOwner(d, identity, c.Config.Details["registry_hostname"]); operationErr != nil {
		return operationErr
	}
	if held := d.Annotations[registryMaintenance]; held != "" && held != operation {
		return ErrOwned
	}
	hash := sha256.Sum256([]byte(operation))
	helperName := fmt.Sprintf("%s-maint-%x", name, hash[:6])
	pods := c.Kube.CoreV1().Pods(c.Config.Namespace)
	helper, err := pods.Get(ctx, helperName, metav1.GetOptions{})
	if err == nil {
		if helper.Annotations[registryMaintenance] != operation || checkJobOwner(helper, identity) != nil {
			return ErrOwned
		}
		if helper.DeletionTimestamp == nil {
			err = pods.Delete(ctx, helperName, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &helper.UID}})
			if err != nil {
				return err
			}
		}
		return ErrRegistryPending
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	if d.Annotations[registryMaintenance] == operation {
		delete(d.Annotations, registryMaintenance)
		d.Spec.Replicas = ptr.To(int32(1))
		_, err = api.Update(ctx, d, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
		return ErrRegistryPending
	}
	if !deploymentReady(d) {
		return ErrRegistryPending
	}
	return nil
}

func registryDeleteOptions(object metav1.Object) metav1.DeleteOptions {
	uid, version := object.GetUID(), object.GetResourceVersion()
	return metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}, PropagationPolicy: ptr.To(metav1.DeletePropagationForeground)}
}
