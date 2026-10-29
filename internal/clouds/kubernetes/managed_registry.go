package kubernetes

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

const managedRegistryLabel = "loomplane.registry"
const managedRegistryHost = "loomplane.registry.hostname"

// Registry images are pinned to the multi-platform index, not a mutable tag.
const managedRegistryImage = "docker.io/library/registry:3.1.2@sha256:d4032d4cc323c1731c707931ecada07a53076eac38e3b61ae57533f170f2c6c3"

// ManagedRegistry contains credentials for one cluster connection's registry.
// Callers must keep these out of deployment checkpoints and log messages.
type ManagedRegistry struct {
	Name, Host     string
	DockerConfig   []byte
	Password, Auth string
}

func registryIdentity(org, connection, namespaceUID string) (name string, identity map[string]string, err error) {
	for _, value := range []string{org, connection, namespaceUID} {
		if value == "" || len(validation.IsValidLabelValue(value)) != 0 {
			return "", nil, errors.New("managed registry needs its organization, connection and namespace identity")
		}
	}
	sum := sha256.Sum256([]byte(org + "/" + connection + "/" + namespaceUID))
	name = fmt.Sprintf("lp-registry-%x", sum[:10])
	return name, map[string]string{managedRegistryLabel: name, owned.LabelOrg: org, owned.LabelConnection: connection}, nil
}

type managedResource[T managedObject] interface {
	Get(context.Context, string, metav1.GetOptions) (T, error)
	Create(context.Context, T, metav1.CreateOptions) (T, error)
	Update(context.Context, T, metav1.UpdateOptions) (T, error)
}

func managedOwner(object metav1.Object, identity map[string]string, host string) error {
	if object.GetDeletionTimestamp() != nil || object.GetAnnotations()[managedRegistryHost] != host {
		return ErrOwned
	}
	return checkJobOwner(object, identity)
}

func ensureManaged[T managedObject](ctx context.Context, api managedResource[T], desired T, identity map[string]string, host string) (T, error) {
	old, err := api.Get(ctx, desired.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		created, createErr := api.Create(ctx, desired, metav1.CreateOptions{})
		if !apierrors.IsAlreadyExists(createErr) {
			return created, apiReason(createErr)
		}
		old, err = api.Get(ctx, desired.GetName(), metav1.GetOptions{})
	}
	if err != nil {
		return old, apiReason(err)
	}
	if checkErr := managedOwner(old, identity, host); checkErr != nil {
		return old, checkErr
	}
	// Keep allocated service addresses and bound storage. Reconcile the
	// registry pod template so credential rotation also restarts its process.
	switch want := any(desired).(type) {
	case *corev1.PersistentVolumeClaim:
		return old, nil
	case *corev1.Service:
		have, ok := any(old).(*corev1.Service)
		if !ok {
			return old, ErrOwned
		}
		want.Spec.ClusterIP, want.Spec.ClusterIPs = have.Spec.ClusterIP, have.Spec.ClusterIPs
		want.Spec.IPFamilies, want.Spec.IPFamilyPolicy = have.Spec.IPFamilies, have.Spec.IPFamilyPolicy
	}
	desired.SetResourceVersion(old.GetResourceVersion())
	updated, err := api.Update(ctx, desired, metav1.UpdateOptions{})
	return updated, apiReason(err)
}

func (c *Client) checkManagedBuildPermissions(ctx context.Context, out *Result) error {
	delete(out.Details, "managed_builds")
	if c.Config.Details["registry_hostname"] == "" {
		return nil
	}
	missing := []string{}
	if out.Details["persistent_storage"] != "ready" {
		missing = append(missing, "managed builds: retained persistent storage")
	}
	if out.Details["route_prerequisites"] != "ready" {
		missing = append(missing, "managed builds: "+out.Details["route_prerequisites"])
	}
	if out.Details["jobs"] != "ready" {
		missing = append(missing, "managed builds: jobs and pod logs permissions")
	}
	for _, item := range []struct{ resource, verb string }{{"pods", "create"}, {"pods", "delete"}, {"pods/exec", "create"}} {
		allowed, err := c.allowed(ctx, "", item.resource, item.verb)
		if err != nil {
			return err
		}
		if !allowed {
			missing = append(missing, item.resource+":"+item.verb)
		}
	}
	out.Details["managed_builds"] = "ready"
	if len(missing) > 0 {
		out.Details["managed_builds"] = "unavailable"
		out.Missing = append(out.Missing, missing...)
	}
	return nil
}

func (c *Client) registrySecret(ctx context.Context, org, connection string, create bool) (*corev1.Secret, error) {
	host := c.Config.Details["registry_hostname"]
	name, identity, err := registryIdentity(org, connection, c.Config.Details["namespace_uid"])
	if err != nil {
		return nil, err
	}
	secrets := c.Kube.CoreV1().Secrets(c.Config.Namespace)
	old, err := secrets.Get(ctx, name+"-auth", metav1.GetOptions{})
	if apierrors.IsNotFound(err) && create {
		password := rand.Text() + rand.Text()
		hash, hashErr := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if hashErr != nil {
			return nil, hashErr
		}
		auth := base64.StdEncoding.EncodeToString([]byte("loomplane:" + password))
		config, marshalErr := json.Marshal(map[string]any{"auths": map[string]any{host: map[string]string{"auth": auth}}})
		if marshalErr != nil {
			return nil, marshalErr
		}
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name + "-auth", Namespace: c.Config.Namespace, Labels: identity, Annotations: map[string]string{managedRegistryHost: host}}, Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{"htpasswd": []byte("loomplane:" + string(hash) + "\n"), "config.json": config, "http-secret": []byte(rand.Text() + rand.Text())}}
		old, err = secrets.Create(ctx, secret, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			old, err = secrets.Get(ctx, name+"-auth", metav1.GetOptions{})
		}
	}
	if err != nil {
		return nil, apiReason(err)
	}
	if checkErr := managedOwner(old, identity, host); checkErr != nil {
		return nil, checkErr
	}
	if old.Type != corev1.SecretTypeOpaque || len(old.Data["htpasswd"]) == 0 || len(old.Data["http-secret"]) == 0 {
		return nil, errors.New("managed registry credentials are incomplete")
	}
	if _, err = registryCredentials(old, name, host); err != nil {
		return nil, err
	}
	return old, nil
}

func registryCredentials(secret *corev1.Secret, name, host string) (*ManagedRegistry, error) {
	var config struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if json.Unmarshal(secret.Data["config.json"], &config) != nil || len(config.Auths) != 1 {
		return nil, errors.New("managed registry credentials are invalid")
	}
	auth := config.Auths[host].Auth
	raw, err := base64.StdEncoding.DecodeString(auth)
	user, password, ok := strings.Cut(string(raw), ":")
	if err != nil || !ok || user != "loomplane" || len(password) < 32 {
		return nil, errors.New("managed registry credentials do not match its hostname")
	}
	hash := strings.TrimSpace(strings.TrimPrefix(string(secret.Data["htpasswd"]), "loomplane:"))
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, errors.New("managed registry password does not match its authentication database")
	}
	return &ManagedRegistry{Name: name, Host: host, DockerConfig: secret.Data["config.json"], Password: password, Auth: auth}, nil
}

// managedPullSecret prepares a workload-owned copy of the native Docker
// credentials. External pull Secrets remain outside Loomplane's ownership.
func (c *Client) managedPullSecret(ctx context.Context, image, name string, identity map[string]string, connection string) (*corev1.Secret, error) {
	host := c.Config.Details["registry_hostname"]
	if host == "" || !strings.HasPrefix(image, host+"/loomplane/") {
		return nil, nil
	}
	shared, err := c.registrySecret(ctx, identity[owned.LabelOrg], connection, false)
	if err != nil {
		return nil, err
	}
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name + "-pull", Namespace: c.Config.Namespace, Labels: maps.Clone(identity)}, Type: corev1.SecretTypeDockerConfigJson, Data: map[string][]byte{corev1.DockerConfigJsonKey: shared.Data["config.json"]}}, nil
}

func (c *Client) applyManagedPull(ctx context.Context, desired, old *corev1.Secret) error {
	secrets := c.Kube.CoreV1().Secrets(c.Config.Namespace)
	var err error
	var kept *corev1.Secret
	if old == nil {
		kept, err = secrets.Create(ctx, desired, metav1.CreateOptions{})
	} else {
		desired.ResourceVersion = old.ResourceVersion
		kept, err = secrets.Update(ctx, desired, metav1.UpdateOptions{})
	}
	if err == nil {
		*desired = *kept
	}
	return apiReason(err)
}

// EnsureManagedRegistry provisions shared persistent storage, credentials and
// a TLS ingress. It never deletes images when an individual workload is removed.
func (c *Client) EnsureManagedRegistry(ctx context.Context, org, connection string) (*ManagedRegistry, error) {
	if c.Config.Details["managed_builds"] != "ready" {
		return nil, errors.New("check the cluster's managed build prerequisites before deploying")
	}
	class, err := c.persistentClass(ctx)
	if err != nil {
		return nil, err
	}
	host, ns := c.Config.Details["registry_hostname"], c.Config.Namespace
	name, identity, err := registryIdentity(org, connection, c.Config.Details["namespace_uid"])
	if err != nil {
		return nil, err
	}
	meta := func(n string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: n, Namespace: ns, Labels: maps.Clone(identity), Annotations: map[string]string{managedRegistryHost: host}}
	}
	// A second connection must not claim the same hostname in this namespace.
	ingresses, err := c.Kube.NetworkingV1().Ingresses(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, ingress := range ingresses.Items {
		for _, rule := range ingress.Spec.Rules {
			if rule.Host == host && ingress.Labels[LabelRoute] != name {
				return nil, errors.New("the registry hostname is already used by another ingress")
			}
		}
	}
	oldDeployment, deploymentErr := c.Kube.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	if deploymentErr != nil && !apierrors.IsNotFound(deploymentErr) {
		return nil, deploymentErr
	}
	if deploymentErr == nil {
		if checkErr := managedOwner(oldDeployment, identity, host); checkErr != nil {
			return nil, checkErr
		}
	}
	oldClaim, claimErr := c.Kube.CoreV1().PersistentVolumeClaims(ns).Get(ctx, name, metav1.GetOptions{})
	if claimErr != nil && !apierrors.IsNotFound(claimErr) {
		return nil, claimErr
	}
	if claimErr == nil {
		if checkErr := managedOwner(oldClaim, identity, host); checkErr != nil {
			return nil, checkErr
		}
	}
	if deploymentErr == nil && apierrors.IsNotFound(claimErr) {
		return nil, errors.New("the managed registry lost its image volume; restore the retained claim before deploying")
	}
	secret, err := c.registrySecret(ctx, org, connection, apierrors.IsNotFound(deploymentErr) && apierrors.IsNotFound(claimErr))
	if err != nil {
		return nil, err
	}
	mode := corev1.PersistentVolumeFilesystem
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: meta(name), Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, VolumeMode: &mode, StorageClassName: &class.Name, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("20Gi")}}}}
	existingClaim, err := ensureManaged(ctx, c.Kube.CoreV1().PersistentVolumeClaims(ns), claim, identity, host)
	if err != nil {
		return nil, err
	}
	if existingClaim.Spec.StorageClassName == nil || *existingClaim.Spec.StorageClassName != class.Name || existingClaim.Spec.VolumeMode == nil || *existingClaim.Spec.VolumeMode != mode || len(existingClaim.Spec.AccessModes) != 1 || existingClaim.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		return nil, errors.New("managed registry storage changed; restore its retained filesystem claim")
	}
	if existingClaim.Status.Phase == corev1.ClaimBound {
		if checkErr := c.checkBoundVolume(ctx, existingClaim); checkErr != nil {
			return nil, checkErr
		}
	}
	serverLabels := maps.Clone(identity)
	// Builders share the registry's ownership labels, but cannot serve traffic.
	serverLabels["loomplane.registry.server"] = "true"
	service := &corev1.Service{ObjectMeta: meta(name), Spec: corev1.ServiceSpec{Selector: serverLabels, Ports: []corev1.ServicePort{{Name: "registry", Port: 5000, TargetPort: intstr.FromInt32(5000)}}}}
	if _, err = ensureManaged(ctx, c.Kube.CoreV1().Services(ns), service, identity, host); err != nil {
		return nil, err
	}
	container := corev1.Container{Name: "registry", Image: managedRegistryImage, Ports: []corev1.ContainerPort{{ContainerPort: 5000}},
		Env:             []corev1.EnvVar{{Name: "REGISTRY_AUTH", Value: "htpasswd"}, {Name: "REGISTRY_AUTH_HTPASSWD_REALM", Value: "Loomplane images"}, {Name: "REGISTRY_AUTH_HTPASSWD_PATH", Value: "/auth/htpasswd"}, {Name: "REGISTRY_HTTP_RELATIVEURLS", Value: "true"}, {Name: "REGISTRY_HTTP_SECRET", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secret.Name}, Key: "http-secret"}}}},
		VolumeMounts:    []corev1.VolumeMount{{Name: "images", MountPath: "/var/lib/registry"}, {Name: "auth", MountPath: "/auth", ReadOnly: true}},
		Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")}, Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")}},
		ReadinessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstr.FromInt32(5000)}}, PeriodSeconds: 5},
		SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}}
	deployment := &appsv1.Deployment{ObjectMeta: meta(name), Spec: appsv1.DeploymentSpec{Replicas: ptr.To(int32(1)), Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}, Selector: &metav1.LabelSelector{MatchLabels: identity}, Template: corev1.PodTemplateSpec{ObjectMeta: meta(name), Spec: corev1.PodSpec{AutomountServiceAccountToken: ptr.To(false), SecurityContext: &corev1.PodSecurityContext{RunAsUser: ptr.To(int64(1000)), RunAsGroup: ptr.To(int64(1000)), FSGroup: ptr.To(int64(1000)), RunAsNonRoot: ptr.To(true)}, Containers: []corev1.Container{container}, Volumes: []corev1.Volume{{Name: "images", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: name}}}, {Name: "auth", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secret.Name, Items: []corev1.KeyToPath{{Key: "htpasswd", Path: "htpasswd"}}}}}}}}}}
	authDigest := sha256.Sum256(secret.Data["htpasswd"])
	deployment.Spec.Template.Labels = serverLabels
	deployment.Spec.Template.Annotations["loomplane.registry.auth"] = fmt.Sprintf("%x", authDigest)
	if _, err = ensureManaged(ctx, c.Kube.AppsV1().Deployments(ns), deployment, identity, host); err != nil {
		return nil, err
	}
	issuer, err := c.EnsureHTTPIssuer(ctx, c.Config.Details["ingress_class"], "")
	if err != nil {
		return nil, err
	}
	if checkErr := c.ApplyRouteIngress(ctx, RouteIngress{RouteID: name, OrgID: org, ProjectID: "shared", EnvironmentID: "shared", Hostname: host, ServiceName: name, ServicePort: 5000, IngressClass: c.Config.Details["ingress_class"], Issuer: issuer, Registry: true}); checkErr != nil {
		return nil, checkErr
	}
	return registryCredentials(secret, name, host)
}

// WaitManagedRegistry waits for both the registry workload and its certificate.
func (c *Client) WaitManagedRegistry(ctx context.Context, registry *ManagedRegistry) error {
	return pollBuild(ctx, func() (bool, error) {
		status, err := c.RouteIngressStatus(ctx, registry.Name, registry.Name)
		if err != nil {
			return false, err
		}
		if status.Certificate.Failed {
			return false, errors.New("registry certificate issuance failed; check its DNS and cert-manager status")
		}
		return status.Certificate.Ready && status.BackendReady, nil
	})
}

func pollBuild(ctx context.Context, check func() (bool, error)) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		ready, err := check()
		if err != nil || ready {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
