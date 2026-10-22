package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"time"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	klabels "k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/xraph/loomplane/internal/clouds/owned"
	nativepg "github.com/xraph/loomplane/internal/clouds/postgres"
)

const volumeMountAnnotation = "loomplane.volume.mount"
const volumeImageAnnotation = "loomplane.volume.image"

func validatePersistentSpec(spec cpprovider.ServiceSpec, labels map[string]string) error {
	if err := owned.ValidatePersistentVolumes(spec.Volumes); err != nil {
		return err
	}
	if labels[owned.LabelStateful] != "true" || labels[owned.LabelPublic] == "true" || labels[owned.LabelManageHPA] == "true" || (labels[owned.LabelReplicas] != "" && labels[owned.LabelReplicas] != "1") {
		return errors.New("persistent Kubernetes workloads require a private single-writer stateful profile")
	}
	if len(spec.Ports) == 0 {
		return errors.New("a persistent Kubernetes workload needs a native service port")
	}
	for _, v := range spec.Volumes {
		if v.SizeMB < 1 || v.SizeMB > 1<<30 {
			return errors.New("persistent volume needs an explicit supported capacity in MiB")
		}
	}
	return nil
}

func persistentIdentity(iid id.ID, labels map[string]string, role string) (map[string]string, error) {
	expected := objectLabels(iid, labels)
	expected[owned.LabelConnection] = labels[owned.LabelConnection]
	expected[owned.LabelVolumeRole] = role
	for _, v := range expected {
		if v == "" {
			return nil, errors.New("persistent storage needs complete ownership")
		}
	}
	return expected, nil
}

func (c *Client) persistentClass(ctx context.Context) (*storagev1.StorageClass, error) {
	name, uid := c.Config.Details["storage_class"], c.Config.Details["storage_class_uid"]
	if name == "" || uid == "" {
		return nil, errors.New("choose and check a retained storage class before deploying persistent workloads")
	}
	sc, err := c.Kube.StorageV1().StorageClasses().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if string(sc.UID) != uid {
		return nil, errors.New("the reviewed storage class identity changed")
	}
	if checkErr := checkRetainedClass(sc); checkErr != nil {
		return nil, checkErr
	}
	return sc, nil
}

func desiredClaim(iid id.ID, labels map[string]string, ns, class string, v cpprovider.VolumeSpec) (*corev1.PersistentVolumeClaim, error) {
	identity, err := persistentIdentity(iid, labels, v.Name)
	if err != nil {
		return nil, err
	}
	mode := corev1.PersistentVolumeFilesystem
	return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: owned.PersistentVolumeName(iid, v.Name), Namespace: ns, Labels: identity, Annotations: map[string]string{volumeMountAnnotation: v.MountPath}}, Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, StorageClassName: &class, VolumeMode: &mode, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: *resource.NewQuantity(int64(v.SizeMB)*(1<<20), resource.BinarySI)}}}}, nil
}
func checkClaim(actual, want *corev1.PersistentVolumeClaim) error {
	for key, value := range want.Labels {
		if actual.Labels[key] != value {
			return ErrOwned
		}
	}
	if actual.Labels[nativepg.LabelRecovery] != want.Labels[nativepg.LabelRecovery] || actual.Labels[nativepg.LabelDatabase] != want.Labels[nativepg.LabelDatabase] {
		return ErrOwned
	}
	if actual.DeletionTimestamp != nil || len(actual.OwnerReferences) > 0 || actual.Annotations[volumeMountAnnotation] != want.Annotations[volumeMountAnnotation] || actual.Annotations[volumeImageAnnotation] != want.Annotations[volumeImageAnnotation] || actual.Spec.StorageClassName == nil || *actual.Spec.StorageClassName != *want.Spec.StorageClassName || !reflect.DeepEqual(actual.Spec.AccessModes, want.Spec.AccessModes) || actual.Spec.VolumeMode != nil && *actual.Spec.VolumeMode != corev1.PersistentVolumeFilesystem || actual.Spec.Resources.Requests.Storage().Cmp(*want.Spec.Resources.Requests.Storage()) != 0 || actual.Spec.DataSource != nil || actual.Spec.DataSourceRef != nil || actual.Spec.Selector != nil {
		return errors.New("persistent claim layout or ownership changed; use a reviewed migration")
	}
	return nil
}

func (c *Client) checkBoundVolume(ctx context.Context, claim *corev1.PersistentVolumeClaim) error {
	if claim.Spec.VolumeName == "" {
		return nil
	}
	pv, err := c.Kube.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	ref := pv.Spec.ClaimRef
	if claim.UID == "" || ref == nil || ref.UID != claim.UID || ref.Name != claim.Name || ref.Namespace != claim.Namespace || pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain || pv.Spec.VolumeMode != nil && *pv.Spec.VolumeMode != corev1.PersistentVolumeFilesystem || pv.Spec.Capacity.Storage().Cmp(*claim.Spec.Resources.Requests.Storage()) < 0 {
		return errors.New("the bound persistent volume no longer matches the retained claim")
	}
	return nil
}

func (r *Runtime) applyPersistent(ctx context.Context, iid id.ID, spec cpprovider.ServiceSpec, labels map[string]string) (result *cpprovider.ProvisionResult, err error) {
	if checkErr := validatePersistentSpec(spec, labels); checkErr != nil {
		return nil, checkErr
	}
	c, err := r.Session(ctx, owned.ForOperation(labels, "apply"))
	if err != nil {
		return nil, err
	}
	if checkErr := c.CheckPullSecret(ctx); checkErr != nil {
		return nil, checkErr
	}
	old, err := r.objects(ctx, c, iid, labels)
	if err != nil {
		return nil, err
	}
	if old.deployment != nil || old.ingress != nil || old.hpa != nil {
		return nil, errors.New("a persistent workload cannot adopt a Deployment or public/autoscaled workload")
	}
	if old.stateful != nil && (old.stateful.Labels[nativepg.LabelRecovery] != labels[nativepg.LabelRecovery] || old.stateful.Labels[nativepg.LabelDatabase] != labels[nativepg.LabelDatabase]) {
		return nil, ErrOwned
	}
	sc, err := c.persistentClass(ctx)
	if err != nil {
		return nil, err
	}
	ns, name := c.Config.Namespace, WorkloadName(iid)
	identity := objectLabels(iid, labels)
	identity[owned.LabelConnection] = labels[owned.LabelConnection]
	if database := labels[nativepg.LabelDatabase]; database != "" {
		if !nativepg.ValidDatabase(database) || spec.Env["POSTGRES_DB"] != database {
			return nil, errors.New("invalid PostgreSQL database configuration")
		}
		identity[nativepg.LabelDatabase] = database
	}
	if recovery := labels[nativepg.LabelRecovery]; recovery != "" {
		if labels[owned.LabelPublic] != "false" || labels[nativepg.LabelDatabase] == "" {
			return nil, errors.New("recovery requires a private database profile")
		}
		identity[nativepg.LabelRecovery] = recovery
		identity[recoveryTargetLabel] = "true"
	}
	// Claims outlive workloads. Check their recorded roles even when the previous
	// StatefulSet is gone, so reattachment cannot silently select an empty role.
	retained, listErr := c.Kube.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{LabelSelector: klabels.Set{owned.LabelInstance: iid.String()}.String()})
	if listErr != nil {
		return nil, listErr
	}
	roles := map[string]string{}
	for _, v := range spec.Volumes {
		roles[v.Name] = v.MountPath
	}
	for _, claim := range retained.Items {
		role := claim.Labels[owned.LabelVolumeRole]
		expected, identityErr := persistentIdentity(iid, labels, role)
		if identityErr != nil {
			return nil, identityErr
		}
		for key, value := range expected {
			if claim.Labels[key] != value {
				return nil, ErrOwned
			}
		}
		if roles[role] == "" || claim.Annotations[volumeMountAnnotation] != roles[role] {
			return nil, errors.New("retained persistent layout changed; use a reviewed migration")
		}
	}
	claims := make([]*corev1.PersistentVolumeClaim, 0, len(spec.Volumes))
	for _, v := range spec.Volumes {
		want, wantErr := desiredClaim(iid, labels, ns, sc.Name, v)
		if wantErr != nil {
			return nil, wantErr
		}
		want.Annotations[volumeImageAnnotation] = spec.Image
		if value := labels[nativepg.LabelRecovery]; value != "" {
			want.Labels[nativepg.LabelRecovery] = value
		}
		if value := labels[nativepg.LabelDatabase]; value != "" {
			want.Labels[nativepg.LabelDatabase] = value
		}
		existing, getErr := c.Kube.CoreV1().PersistentVolumeClaims(ns).Get(ctx, want.Name, metav1.GetOptions{})
		if getErr != nil && !apierrors.IsNotFound(getErr) {
			return nil, getErr
		}
		if getErr == nil {
			if checkErr := checkClaim(existing, want); checkErr != nil {
				return nil, checkErr
			}
			if checkErr := c.checkBoundVolume(ctx, existing); checkErr != nil {
				return nil, checkErr
			}
		}
		claims = append(claims, want)
	}
	values := owned.RuntimeValues{}
	if r.Values != nil {
		values, err = r.Values(ctx, labels)
		if err != nil {
			return nil, err
		}
	}
	if values.PullAuth != "" {
		return nil, errors.New("private Kubernetes registry credentials require an existing image_pull_secret")
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name + "-runtime", Namespace: ns, Labels: maps.Clone(identity), Annotations: map[string]string{owned.LabelStateful: "true"}}, Type: corev1.SecretTypeOpaque, Data: runtimeSecretData(values)}
	pulls := []corev1.LocalObjectReference{}
	if pull := c.Config.Details["image_pull_secret"]; pull != "" {
		pulls = append(pulls, corev1.LocalObjectReference{Name: pull})
	}
	template, ports, err := runtimePod(spec, labels, values, secret, pulls)
	if err != nil {
		return nil, err
	}
	template.Labels = maps.Clone(identity)
	for _, env := range template.Spec.Containers[0].Env {
		if env.Name == podUIDEnv {
			return nil, errors.New("the pod identity environment key is reserved")
		}
	}
	template.Spec.Containers[0].Env = append(template.Spec.Containers[0].Env, podIdentityEnv())
	automount := false
	template.Spec.AutomountServiceAccountToken = &automount
	port, err := healthPort(spec, labels)
	if err != nil {
		return nil, err
	}
	template.Spec.Containers[0].ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(port)}}, PeriodSeconds: 2, TimeoutSeconds: 2, FailureThreshold: 30}
	switch database, health := labels[nativepg.LabelDatabase], labels[owned.LabelHealth]; {
	case database != "":
		template.Spec.Containers[0].ReadinessProbe.ProbeHandler = corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"pg_isready", "-h", "127.0.0.1", "-U", "postgres", "-d", database}}}
	case health != "":
		// A catalog component declares an HTTP readiness probe.
		template.Spec.Containers[0].ReadinessProbe.ProbeHandler = corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: health, Port: intstr.FromInt(port)}}
	}
	for i, v := range spec.Volumes {
		template.Spec.Volumes = append(template.Spec.Volumes, corev1.Volume{Name: v.Name, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claims[i].Name}}})
		template.Spec.Containers[0].VolumeMounts = append(template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: v.Name, MountPath: v.MountPath})
	}
	if old.stateful != nil {
		if old.stateful.DeletionTimestamp != nil || len(old.stateful.Spec.Template.Spec.Containers) != 1 || !reflect.DeepEqual(old.stateful.Spec.Template.Spec.Volumes, template.Spec.Volumes) || !reflect.DeepEqual(old.stateful.Spec.Template.Spec.Containers[0].VolumeMounts, template.Spec.Containers[0].VolumeMounts) || old.stateful.Spec.Template.Spec.Containers[0].Image != spec.Image {
			return nil, errors.New("persistent image or mount layout changed; use a qualified migration")
		}
	}
	mutated := false
	defer func() {
		if err != nil && mutated {
			rollback, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			err = errors.Join(err, r.restore(rollback, c, iid, labels, old))
		}
	}()
	if labels[nativepg.LabelRecovery] != "" {
		if policyErr := r.ensureRecoveryPolicy(ctx, c, iid, labels, identity); policyErr != nil {
			return nil, policyErr
		}
	}
	for _, want := range claims {
		actual, getErr := c.Kube.CoreV1().PersistentVolumeClaims(ns).Get(ctx, want.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(getErr) {
			actual, getErr = c.Kube.CoreV1().PersistentVolumeClaims(ns).Create(ctx, want, metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(getErr) {
				actual, getErr = c.Kube.CoreV1().PersistentVolumeClaims(ns).Get(ctx, want.Name, metav1.GetOptions{})
			}
		}
		if getErr != nil {
			return nil, getErr
		}
		if checkErr := checkClaim(actual, want); checkErr != nil {
			return nil, checkErr
		}
	}
	// Claims have no owner references and are never rolled back or deleted here.
	mutated = true
	if old.runtime != nil {
		secret.ResourceVersion = old.runtime.ResourceVersion
		_, err = c.Kube.CoreV1().Secrets(ns).Update(ctx, secret, metav1.UpdateOptions{})
	} else {
		_, err = c.Kube.CoreV1().Secrets(ns).Create(ctx, secret, metav1.CreateOptions{})
	}
	if err != nil {
		return nil, err
	}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: maps.Clone(identity)}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: corev1.ClusterIPNone, Selector: maps.Clone(identity), Ports: ports}}
	if old.service != nil {
		if old.service.Spec.ClusterIP != corev1.ClusterIPNone {
			return nil, errors.New("persistent service identity changed")
		}
		service.ResourceVersion = old.service.ResourceVersion
		service.Spec.ClusterIPs = old.service.Spec.ClusterIPs
		service.Spec.IPFamilies = old.service.Spec.IPFamilies
		service.Spec.IPFamilyPolicy = old.service.Spec.IPFamilyPolicy
		_, err = c.Kube.CoreV1().Services(ns).Update(ctx, service, metav1.UpdateOptions{})
	} else {
		_, err = c.Kube.CoreV1().Services(ns).Create(ctx, service, metav1.CreateOptions{})
	}
	if err != nil {
		return nil, err
	}
	one := int32(1)
	set := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: maps.Clone(identity)}, Spec: appsv1.StatefulSetSpec{Replicas: &one, ServiceName: name, Selector: &metav1.LabelSelector{MatchLabels: maps.Clone(identity)}, Template: *template, PodManagementPolicy: appsv1.OrderedReadyPodManagement, UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType}, PersistentVolumeClaimRetentionPolicy: &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{WhenDeleted: appsv1.RetainPersistentVolumeClaimRetentionPolicyType, WhenScaled: appsv1.RetainPersistentVolumeClaimRetentionPolicyType}}}
	if old.stateful != nil {
		set.ResourceVersion = old.stateful.ResourceVersion
		set, err = c.Kube.AppsV1().StatefulSets(ns).Update(ctx, set, metav1.UpdateOptions{})
	} else {
		set, err = c.Kube.AppsV1().StatefulSets(ns).Create(ctx, set, metav1.CreateOptions{})
	}
	if err != nil {
		return nil, err
	}
	if r.WaitReady != nil {
		err = r.WaitReady(ctx, c, name)
	} else {
		err = r.waitPersistent(ctx, c, set)
	}
	if err != nil {
		return nil, err
	}
	for _, want := range claims {
		actual, readErr := c.Kube.CoreV1().PersistentVolumeClaims(ns).Get(ctx, want.Name, metav1.GetOptions{})
		if readErr != nil {
			return nil, readErr
		}
		if checkErr := checkClaim(actual, want); checkErr != nil {
			return nil, checkErr
		}
		if checkErr := c.checkBoundVolume(ctx, actual); checkErr != nil {
			return nil, checkErr
		}
	}
	endpoints := make([]cpprovider.Endpoint, 0, len(spec.Ports))
	for _, p := range spec.Ports {
		if labels[nativepg.LabelRecovery] != "" {
			break
		}
		endpoints = append(endpoints, cpprovider.Endpoint{ServiceName: spec.Name, URL: fmt.Sprintf("tcp://%s.%s.svc:%d", name, ns, p.Container), Port: p.Container, Protocol: "tcp"})
	}
	return &cpprovider.ProvisionResult{ProviderRef: name, ServiceRefs: map[string]string{spec.Name: spec.Name}, Endpoints: endpoints}, nil
}

func persistentReady(set *appsv1.StatefulSet) bool {
	return set.Spec.Replicas != nil && *set.Spec.Replicas == 1 && set.Status.ObservedGeneration >= set.Generation && set.Status.ReadyReplicas == 1 && set.Status.UpdatedReplicas == 1 && set.Status.CurrentRevision != "" && set.Status.CurrentRevision == set.Status.UpdateRevision
}
func (r *Runtime) waitPersistent(ctx context.Context, c *Client, applied *appsv1.StatefulSet) error {
	wait := r.Wait
	if wait <= 0 {
		wait = 3 * time.Minute
	}
	deadline := time.Now().Add(wait)
	for {
		current, err := c.Kube.AppsV1().StatefulSets(c.Config.Namespace).Get(ctx, applied.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if current.UID != applied.UID || current.Generation < applied.Generation || !reflect.DeepEqual(current.Spec.Template, applied.Spec.Template) {
			return errors.New("the stateful workload changed while waiting for readiness")
		}
		if persistentReady(current) {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("persistent workload did not become ready")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
func (r *Runtime) persistentStatus(ctx context.Context, c *Client, iid id.ID, labels map[string]string) (*cpprovider.InstanceStatus, error) {
	set, err := c.Kube.AppsV1().StatefulSets(c.Config.Namespace).Get(ctx, WorkloadName(iid), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &cpprovider.InstanceStatus{State: cpprovider.StateStopped, Metadata: map[string]string{"exists": "false"}}, nil
	}
	if err != nil {
		return nil, err
	}
	if checkErr := checkOwner(set, iid, labels); checkErr != nil {
		return nil, checkErr
	}
	state := cpprovider.StateStarting
	ready := persistentReady(set)
	if ready {
		state = cpprovider.StateRunning
	}
	if set.Spec.Replicas != nil && *set.Spec.Replicas == 0 {
		state = cpprovider.StateStopped
	}
	revision := ""
	if len(set.Spec.Template.Spec.Containers) > 0 {
		revision = set.Spec.Template.Spec.Containers[0].Image
	}
	return &cpprovider.InstanceStatus{State: state, Ready: ready, Metadata: map[string]string{"exists": "true", "revision": revision}}, nil
}
func (r *Runtime) scalePersistent(ctx context.Context, c *Client, iid id.ID, labels map[string]string, n int) error {
	if n < 0 || n > 1 {
		return errors.New("a standalone persistent workload permits zero or one writer")
	}
	set, err := c.Kube.AppsV1().StatefulSets(c.Config.Namespace).Get(ctx, WorkloadName(iid), metav1.GetOptions{})
	if err != nil {
		return err
	}
	if checkErr := checkOwner(set, iid, labels); checkErr != nil {
		return checkErr
	}
	replicas := int32(n)
	set.Spec.Replicas = &replicas
	_, err = c.Kube.AppsV1().StatefulSets(c.Config.Namespace).Update(ctx, set, metav1.UpdateOptions{})
	return err
}
func (r *Runtime) waitPersistentRemoved(ctx context.Context, c *Client, iid id.ID, labels map[string]string) error {
	wait := r.Wait
	if wait <= 0 {
		wait = 3 * time.Minute
	}
	deadline := time.Now().Add(wait)
	for {
		set, err := c.Kube.AppsV1().StatefulSets(c.Config.Namespace).Get(ctx, WorkloadName(iid), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if checkErr := checkOwner(set, iid, labels); checkErr != nil {
			return checkErr
		}
		if time.Now().After(deadline) {
			return errors.New("the stateful workload is still stopping; retained claims were not removed")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
func (r *Runtime) restorePersistent(ctx context.Context, c *Client, iid id.ID, labels map[string]string, old *appsv1.StatefulSet) error {
	var original managedObject
	if old != nil {
		original = old
	}
	ns, name := c.Config.Namespace, WorkloadName(iid)
	return restoreOne(ctx, iid, labels, original, restoreClient{
		get: func(ctx context.Context) (managedObject, error) {
			return c.Kube.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
		},
		put: func(ctx context.Context, o managedObject, found bool) error {
			set, ok := o.(*appsv1.StatefulSet)
			if !ok {
				return errors.New("invalid rollback StatefulSet")
			}
			if found {
				_, err := c.Kube.AppsV1().StatefulSets(ns).Update(ctx, set, metav1.UpdateOptions{})
				return err
			}
			_, err := c.Kube.AppsV1().StatefulSets(ns).Create(ctx, set, metav1.CreateOptions{})
			return err
		},
		remove: func(ctx context.Context, o managedObject) error {
			opts := deleteOwnedOptions(o)
			policy := metav1.DeletePropagationForeground
			opts.PropagationPolicy = &policy
			if err := c.Kube.AppsV1().StatefulSets(ns).Delete(ctx, name, opts); err != nil {
				return err
			}
			return r.waitPersistentRemoved(ctx, c, iid, labels)
		},
	})
}
