package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kuberuntime "k8s.io/apimachinery/pkg/runtime"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

var ErrOwned = errors.New("the matching Kubernetes resource is not owned by this service")

type Session func(context.Context, map[string]string) (*Client, error)
type Runtime struct {
	Session          Session
	Values           owned.Values
	WaitReady        func(context.Context, *Client, string) error
	RecordController func(context.Context, ControllerReceipt) error
	Wait             time.Duration
}

var _ owned.Backend = (*Runtime)(nil)

func WorkloadName(iid id.ID) string { return "lp-" + strings.ReplaceAll(iid.String(), "_", "-") }

func objectLabels(iid id.ID, labels map[string]string) map[string]string {
	return map[string]string{owned.LabelOrg: labels[owned.LabelOrg], owned.LabelProject: labels[owned.LabelProject], owned.LabelEnvironment: labels[owned.LabelEnvironment], owned.LabelInstance: iid.String()}
}
func checkOwner(object metav1.Object, iid id.ID, labels map[string]string) error {
	if labels[owned.LabelStateful] == "true" && (labels[owned.LabelConnection] == "" || object.GetLabels()[owned.LabelConnection] != labels[owned.LabelConnection]) {
		return ErrOwned
	}
	for key, value := range objectLabels(iid, labels) {
		if value == "" || object.GetLabels()[key] != value {
			return ErrOwned
		}
	}
	return nil
}

type workloadObjects struct {
	deployment    *appsv1.Deployment
	stateful      *appsv1.StatefulSet
	service       *corev1.Service
	runtime, pull *corev1.Secret
	ingress       *networkingv1.Ingress
	hpa           *autoscalingv2.HorizontalPodAutoscaler
}

func (r *Runtime) objects(ctx context.Context, c *Client, iid id.ID, labels map[string]string) (*workloadObjects, error) {
	ns, name := c.Config.Namespace, WorkloadName(iid)
	o := &workloadObjects{}
	var err error
	accept := func(object metav1.Object, getErr error) error {
		if apierrors.IsNotFound(getErr) {
			return nil
		}
		if getErr != nil {
			return getErr
		}
		return checkOwner(object, iid, labels)
	}
	o.deployment, err = c.Kube.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	if checkErr := accept(o.deployment, err); checkErr != nil {
		return nil, checkErr
	}
	if err != nil {
		o.deployment = nil
	}
	if labels[owned.LabelStateful] == "true" {
		o.stateful, err = c.Kube.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
		if checkErr := accept(o.stateful, err); checkErr != nil {
			return nil, checkErr
		}
		if err != nil {
			o.stateful = nil
		}
	}
	o.service, err = c.Kube.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{})
	if checkErr := accept(o.service, err); checkErr != nil {
		return nil, checkErr
	}
	if err != nil {
		o.service = nil
	}
	o.runtime, err = c.Kube.CoreV1().Secrets(ns).Get(ctx, name+"-runtime", metav1.GetOptions{})
	if checkErr := accept(o.runtime, err); checkErr != nil {
		return nil, checkErr
	}
	if err != nil {
		o.runtime = nil
	}
	o.pull, err = c.Kube.CoreV1().Secrets(ns).Get(ctx, name+"-pull", metav1.GetOptions{})
	if checkErr := accept(o.pull, err); checkErr != nil {
		return nil, checkErr
	}
	if err != nil {
		o.pull = nil
	}
	if labels[owned.LabelPublic] == "true" || labels[owned.LabelManageIngress] == "true" {
		o.ingress, err = c.Kube.NetworkingV1().Ingresses(ns).Get(ctx, name, metav1.GetOptions{})
		if checkErr := accept(o.ingress, err); checkErr != nil {
			return nil, checkErr
		}
		if err != nil {
			o.ingress = nil
		}
	}
	if labels[owned.LabelManageHPA] == "true" {
		o.hpa, err = c.Kube.AutoscalingV2().HorizontalPodAutoscalers(ns).Get(ctx, name, metav1.GetOptions{})
		if checkErr := accept(o.hpa, err); checkErr != nil {
			return nil, checkErr
		}
		if err != nil {
			o.hpa = nil
		}
	}
	return o, nil
}

func (r *Runtime) Apply(ctx context.Context, iid id.ID, spec cpprovider.ServiceSpec, labels map[string]string) (result *cpprovider.ProvisionResult, err error) {
	if len(spec.Volumes) > 0 {
		return r.applyPersistent(ctx, iid, spec, labels)
	}
	if labels[owned.LabelStateful] == "true" {
		return nil, errors.New("a stateful workload cannot drop its persistent volumes")
	}
	c, err := r.Session(ctx, owned.ForOperation(labels, "apply"))
	if err != nil {
		return nil, err
	}
	ns, name := c.Config.Namespace, WorkloadName(iid)
	if checkErr := c.CheckPullSecret(ctx); checkErr != nil {
		return nil, checkErr
	}
	old, err := r.objects(ctx, c, iid, labels)
	if err != nil {
		return nil, err
	}
	if old.runtime != nil && old.runtime.Annotations[owned.LabelStateful] == "true" {
		return nil, errors.New("a stateful workload cannot become a Deployment")
	}
	mutated := false
	defer func() {
		if err == nil || !mutated {
			return
		}
		rollback, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		err = errors.Join(err, r.restore(rollback, c, iid, labels, old))
	}()
	values := owned.RuntimeValues{}
	if r.Values != nil {
		values, err = r.Values(ctx, labels)
		if err != nil {
			return nil, err
		}
	}
	if values.PullAuth != "" {
		return nil, errors.New("private Kubernetes registry credentials require an existing image_pull_secret; native bearer pull Secrets are unsupported")
	}
	identity := objectLabels(iid, labels)
	meta := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: ns, Labels: maps.Clone(identity)}
	}
	data, err := runtimeSecretData(values)
	if err != nil {
		return nil, err
	}
	// The account comes first so a refused rule changes nothing else.
	account, err := r.applyAccess(ctx, c, iid, labels)
	if err != nil {
		return nil, err
	}
	runtimeSecret := &corev1.Secret{ObjectMeta: meta(name + "-runtime"), Type: corev1.SecretTypeOpaque, Data: data}
	mutated = true
	if old.runtime != nil {
		runtimeSecret.ResourceVersion = old.runtime.ResourceVersion
		_, err = c.Kube.CoreV1().Secrets(ns).Update(ctx, runtimeSecret, metav1.UpdateOptions{})
	} else {
		_, err = c.Kube.CoreV1().Secrets(ns).Create(ctx, runtimeSecret, metav1.CreateOptions{})
	}
	if err != nil {
		return nil, err
	}
	pulls := []corev1.LocalObjectReference{}
	if selected := c.Config.Details["image_pull_secret"]; selected != "" {
		pulls = append(pulls, corev1.LocalObjectReference{Name: selected})
	}
	if old.pull != nil {
		if checkErr := c.Kube.CoreV1().Secrets(ns).Delete(ctx, old.pull.Name, deleteOwnedOptions(old.pull)); checkErr != nil {
			return nil, checkErr
		}
	}

	template, servicePorts, err := runtimePod(spec, labels, values, runtimeSecret, pulls, account)
	if err != nil {
		return nil, err
	}
	template.Labels = maps.Clone(identity)

	replicas := int32(1)
	if raw := labels[owned.LabelReplicas]; raw != "" {
		n, parseErr := strconv.ParseInt(raw, 10, 32)
		if parseErr != nil || n < 1 {
			return nil, errors.New("invalid fixed replica count")
		}
		replicas = int32(n)
	}
	deployment := &appsv1.Deployment{ObjectMeta: meta(name), Spec: appsv1.DeploymentSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: maps.Clone(identity)}, Template: *template}}

	if len(servicePorts) > 0 {
		service := &corev1.Service{ObjectMeta: meta(name), Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: maps.Clone(identity), Ports: servicePorts}}
		if old.service != nil {
			service.ResourceVersion = old.service.ResourceVersion
			service.Spec.ClusterIP = old.service.Spec.ClusterIP
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
	} else if old.service != nil {
		if checkErr := c.Kube.CoreV1().Services(ns).Delete(ctx, name, deleteOwnedOptions(old.service)); checkErr != nil {
			return nil, checkErr
		}
	}
	if old.deployment != nil {
		deployment.ResourceVersion = old.deployment.ResourceVersion
		deployment, err = c.Kube.AppsV1().Deployments(ns).Update(ctx, deployment, metav1.UpdateOptions{})
	} else {
		deployment, err = c.Kube.AppsV1().Deployments(ns).Create(ctx, deployment, metav1.CreateOptions{})
	}
	if err != nil {
		return nil, err
	}
	if labels[owned.LabelPublic] == "true" {
		if len(servicePorts) == 0 || labels[owned.LabelHostname] == "" {
			return nil, errors.New("a public Kubernetes service needs a hostname and port")
		}
		pathType := networkingv1.PathTypePrefix
		ingress := &networkingv1.Ingress{ObjectMeta: meta(name), Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: labels[owned.LabelHostname], IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{Path: "/", PathType: &pathType, Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: name, Port: networkingv1.ServiceBackendPort{Number: servicePorts[0].Port}}}}}}}}}}}
		if class := labels[owned.LabelIngressClass]; class != "" {
			ingress.Spec.IngressClassName = &class
		}
		if tls := labels[owned.LabelTLSSecret]; tls != "" {
			if _, err = c.Kube.CoreV1().Secrets(ns).Get(ctx, tls, metav1.GetOptions{}); err != nil {
				return nil, fmt.Errorf("read the selected TLS secret: %w", err)
			}
			ingress.Spec.TLS = []networkingv1.IngressTLS{{Hosts: []string{labels[owned.LabelHostname]}, SecretName: tls}}
		}
		if old.ingress != nil {
			ingress.ResourceVersion = old.ingress.ResourceVersion
			_, err = c.Kube.NetworkingV1().Ingresses(ns).Update(ctx, ingress, metav1.UpdateOptions{})
		} else {
			_, err = c.Kube.NetworkingV1().Ingresses(ns).Create(ctx, ingress, metav1.CreateOptions{})
		}
		if err != nil {
			return nil, err
		}
	} else if old.ingress != nil {
		if checkErr := c.Kube.NetworkingV1().Ingresses(ns).Delete(ctx, name, deleteOwnedOptions(old.ingress)); checkErr != nil {
			return nil, checkErr
		}
	}
	if labels[owned.LabelManageHPA] == "true" && labels[owned.LabelScaleMin] != labels[owned.LabelScaleMax] {
		hpa, hpaErr := runtimeAutoscaler(iid, labels, ns)
		if hpaErr != nil {
			return nil, hpaErr
		}
		if old.hpa != nil {
			hpa.ResourceVersion = old.hpa.ResourceVersion
			_, err = c.Kube.AutoscalingV2().HorizontalPodAutoscalers(ns).Update(ctx, hpa, metav1.UpdateOptions{})
		} else {
			_, err = c.Kube.AutoscalingV2().HorizontalPodAutoscalers(ns).Create(ctx, hpa, metav1.CreateOptions{})
		}
		if err != nil {
			return nil, err
		}
	} else if old.hpa != nil {
		if checkErr := c.Kube.AutoscalingV2().HorizontalPodAutoscalers(ns).Delete(ctx, name, deleteOwnedOptions(old.hpa)); checkErr != nil {
			return nil, checkErr
		}
	}
	wait := r.WaitReady
	if wait == nil {
		wait = func(ctx context.Context, c *Client, _ string) error { return r.waitReady(ctx, c, deployment) }
	}
	if checkErr := wait(ctx, c, name); checkErr != nil {
		return nil, checkErr
	}
	if recordErr := r.recordController(ctx, c, iid, spec, labels, deployment); recordErr != nil {
		return nil, recordErr
	}
	endpoints := []cpprovider.Endpoint{}
	for _, port := range servicePorts {
		public := labels[owned.LabelPublic] == "true"
		address := "http://" + name + "." + ns + ".svc:" + strconv.Itoa(int(port.Port))
		if public {
			scheme := "http"
			if labels[owned.LabelTLSSecret] != "" {
				scheme = "https"
			}
			address = scheme + "://" + labels[owned.LabelHostname]
		}
		endpoints = append(endpoints, cpprovider.Endpoint{ServiceName: spec.Name, URL: address, Port: int(port.Port), Protocol: "http", Public: public})
	}
	return &cpprovider.ProvisionResult{ProviderRef: name, ServiceRefs: map[string]string{spec.Name: spec.Name}, Endpoints: endpoints}, nil
}

func (r *Runtime) waitReady(ctx context.Context, c *Client, applied *appsv1.Deployment) error {
	name := applied.Name
	wait := r.Wait
	if wait <= 0 {
		wait = 3 * time.Minute
	}
	deadline := time.Now().Add(wait)
	for {
		deployment, err := c.Kube.AppsV1().Deployments(c.Config.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if deployment.UID != applied.UID || deployment.Generation < applied.Generation || !reflect.DeepEqual(deployment.Spec.Template, applied.Spec.Template) {
			return errors.New("the Kubernetes workload changed while waiting for readiness")
		}
		if deploymentReady(deployment) {
			return nil
		}
		for _, condition := range deployment.Status.Conditions {
			if condition.Type == appsv1.DeploymentProgressing && condition.Status == corev1.ConditionFalse && deployment.Status.ObservedGeneration >= applied.Generation {
				return fmt.Errorf("kubernetes rollout failed: %s", condition.Message)
			}
		}
		if time.Now().After(deadline) {
			return errors.New("kubernetes rollout did not become ready")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
func deploymentReady(d *appsv1.Deployment) bool {
	return d.Spec.Replicas != nil && *d.Spec.Replicas > 0 && d.Status.ObservedGeneration >= d.Generation && d.Status.UpdatedReplicas >= *d.Spec.Replicas && d.Status.AvailableReplicas >= *d.Spec.Replicas && d.Status.UnavailableReplicas == 0
}

func (r *Runtime) Status(ctx context.Context, iid id.ID, labels map[string]string) (*cpprovider.InstanceStatus, error) {
	c, err := r.Session(ctx, owned.ForOperation(labels, "read"))
	if err != nil {
		return nil, err
	}
	if labels[owned.LabelStateful] == "true" {
		return r.persistentStatus(ctx, c, iid, labels)
	}
	d, err := c.Kube.AppsV1().Deployments(c.Config.Namespace).Get(ctx, WorkloadName(iid), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &cpprovider.InstanceStatus{State: cpprovider.StateStopped, Metadata: map[string]string{"exists": "false"}}, nil
	}
	if err != nil {
		return nil, err
	}
	if checkErr := checkOwner(d, iid, labels); checkErr != nil {
		return nil, checkErr
	}
	state := cpprovider.StateStarting
	ready := deploymentReady(d)
	if ready {
		state = cpprovider.StateRunning
	}
	if d.Spec.Replicas != nil && *d.Spec.Replicas == 0 {
		state = cpprovider.StateStopped
	}
	for _, condition := range d.Status.Conditions {
		if condition.Type == appsv1.DeploymentProgressing && condition.Status == corev1.ConditionFalse && d.Status.ObservedGeneration >= d.Generation {
			state = cpprovider.StateFailed
		}
	}
	revision := ""
	if len(d.Spec.Template.Spec.Containers) > 0 {
		revision = d.Spec.Template.Spec.Containers[0].Image
	}
	return &cpprovider.InstanceStatus{State: state, Ready: ready, Metadata: map[string]string{"exists": "true", "revision": revision}}, nil
}

func (r *Runtime) Remove(ctx context.Context, iid id.ID, labels map[string]string) error {
	c, err := r.Session(ctx, owned.ForOperation(labels, "remove"))
	if err != nil {
		return err
	}
	objects, err := r.objects(ctx, c, iid, labels)
	if err != nil {
		return err
	}
	ns, name := c.Config.Namespace, WorkloadName(iid)
	if objects.stateful != nil {
		policy := metav1.DeletePropagationForeground
		options := deleteOwnedOptions(objects.stateful)
		options.PropagationPolicy = &policy
		if err := c.Kube.AppsV1().StatefulSets(ns).Delete(ctx, name, options); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if err := r.waitPersistentRemoved(ctx, c, iid, labels); err != nil {
			return err
		}
	}
	remove := func(err error) error {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if objects.ingress != nil {
		if checkErr := remove(c.Kube.NetworkingV1().Ingresses(ns).Delete(ctx, name, deleteOwnedOptions(objects.ingress))); checkErr != nil {
			return checkErr
		}
	}
	if objects.hpa != nil {
		if checkErr := remove(c.Kube.AutoscalingV2().HorizontalPodAutoscalers(ns).Delete(ctx, name, deleteOwnedOptions(objects.hpa))); checkErr != nil {
			return checkErr
		}
	}
	if objects.deployment != nil {
		policy := metav1.DeletePropagationForeground
		options := deleteOwnedOptions(objects.deployment)
		options.PropagationPolicy = &policy
		if checkErr := remove(c.Kube.AppsV1().Deployments(ns).Delete(ctx, name, options)); checkErr != nil {
			return checkErr
		}
	}
	if objects.service != nil {
		if checkErr := remove(c.Kube.CoreV1().Services(ns).Delete(ctx, name, deleteOwnedOptions(objects.service))); checkErr != nil {
			return checkErr
		}
	}
	for _, secret := range []*corev1.Secret{objects.runtime, objects.pull} {
		if secret != nil {
			if checkErr := remove(c.Kube.CoreV1().Secrets(ns).Delete(ctx, secret.Name, deleteOwnedOptions(secret))); checkErr != nil {
				return checkErr
			}
		}
	}
	return r.removeAccess(ctx, c, iid, labels)
}
func (r *Runtime) SetRunning(ctx context.Context, iid id.ID, labels map[string]string, running bool) error {
	replicas := 0
	if running {
		replicas = 1
		if raw := labels[owned.LabelReplicas]; raw != "" {
			n, parseErr := strconv.Atoi(raw)
			if parseErr != nil {
				return parseErr
			}
			replicas = n
		}
	}
	elastic := labels[owned.LabelScaleMin] != "" && labels[owned.LabelScaleMin] != labels[owned.LabelScaleMax]
	if !elastic {
		return r.Scale(ctx, iid, labels, cpprovider.ResourceSpec{Replicas: replicas})
	}
	operation := "stop"
	if running {
		operation = "start"
	}
	c, err := r.Session(ctx, owned.ForOperation(labels, operation))
	if err != nil {
		return err
	}
	ns, name := c.Config.Namespace, WorkloadName(iid)
	hpa, err := c.Kube.AutoscalingV2().HorizontalPodAutoscalers(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		hpa = nil
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if hpa != nil {
		if ownerErr := checkOwner(hpa, iid, labels); ownerErr != nil {
			return ownerErr
		}
	}
	if !running && hpa != nil {
		if deleteErr := c.Kube.AutoscalingV2().HorizontalPodAutoscalers(ns).Delete(ctx, name, deleteOwnedOptions(hpa)); deleteErr != nil {
			return deleteErr
		}
	}
	if scaleErr := r.Scale(ctx, iid, labels, cpprovider.ResourceSpec{Replicas: replicas}); scaleErr != nil {
		return scaleErr
	}
	if running && hpa == nil {
		desired, desiredErr := runtimeAutoscaler(iid, labels, ns)
		if desiredErr != nil {
			return desiredErr
		}
		if _, createErr := c.Kube.AutoscalingV2().HorizontalPodAutoscalers(ns).Create(ctx, desired, metav1.CreateOptions{}); createErr != nil {
			restore, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			return errors.Join(createErr, r.Scale(restore, iid, labels, cpprovider.ResourceSpec{Replicas: 0}))
		}
	}
	return nil
}

func (r *Runtime) Scale(ctx context.Context, iid id.ID, labels map[string]string, spec cpprovider.ResourceSpec) error {
	if spec.Replicas < 0 || spec.Replicas > 10000 {
		return errors.New("invalid Kubernetes replica count")
	}
	c, err := r.Session(ctx, owned.ForOperation(labels, "scale"))
	if err != nil {
		return err
	}
	if labels[owned.LabelStateful] == "true" {
		return r.scalePersistent(ctx, c, iid, labels, spec.Replicas)
	}
	d, err := c.Kube.AppsV1().Deployments(c.Config.Namespace).Get(ctx, WorkloadName(iid), metav1.GetOptions{})
	if err != nil {
		return err
	}
	if checkErr := checkOwner(d, iid, labels); checkErr != nil {
		return checkErr
	}
	replicas := int32(spec.Replicas)
	d.Spec.Replicas = &replicas
	_, err = c.Kube.AppsV1().Deployments(c.Config.Namespace).Update(ctx, d, metav1.UpdateOptions{})
	return err
}

type managedObject interface {
	metav1.Object
	kuberuntime.Object
}

type restoreClient struct {
	get    func(context.Context) (managedObject, error)
	put    func(context.Context, managedObject, bool) error
	remove func(context.Context, managedObject) error
}

func restoreOne(ctx context.Context, iid id.ID, labels map[string]string, old managedObject, client restoreClient) error {
	current, err := client.get(ctx)
	found := !apierrors.IsNotFound(err)
	if found && err != nil {
		return err
	}
	if found {
		if checkErr := checkOwner(current, iid, labels); checkErr != nil {
			return checkErr
		}
	}
	if old == nil {
		if found {
			return client.remove(ctx, current)
		}
		return nil
	}
	restored, ok := old.DeepCopyObject().(managedObject)
	if !ok {
		return errors.New("invalid rollback resource")
	}
	if found {
		restored.SetResourceVersion(current.GetResourceVersion())
	} else {
		restored.SetResourceVersion("")
		restored.SetUID("")
	}
	return client.put(ctx, restored, found)
}

func (r *Runtime) restore(ctx context.Context, c *Client, iid id.ID, labels map[string]string, old *workloadObjects) error {
	if labels[owned.LabelStateful] == "true" {
		if err := r.restorePersistent(ctx, c, iid, labels, old.stateful); err != nil {
			return err
		}
	}
	ns, name := c.Config.Namespace, WorkloadName(iid)
	var failures error
	for _, entry := range []struct {
		name string
		old  *corev1.Secret
	}{{name + "-runtime", old.runtime}, {name + "-pull", old.pull}} {
		var original managedObject
		if entry.old != nil {
			original = entry.old
		}
		client := restoreClient{
			get: func(ctx context.Context) (managedObject, error) {
				return c.Kube.CoreV1().Secrets(ns).Get(ctx, entry.name, metav1.GetOptions{})
			},
			put: func(ctx context.Context, o managedObject, found bool) error {
				value, ok := o.(*corev1.Secret)
				if !ok {
					return errors.New("invalid typed rollback resource")
				}
				if found {
					_, err := c.Kube.CoreV1().Secrets(ns).Update(ctx, value, metav1.UpdateOptions{})
					return err
				}
				_, err := c.Kube.CoreV1().Secrets(ns).Create(ctx, value, metav1.CreateOptions{})
				return err
			},
			remove: func(ctx context.Context, o managedObject) error {
				return c.Kube.CoreV1().Secrets(ns).Delete(ctx, entry.name, deleteOwnedOptions(o))
			},
		}
		failures = errors.Join(failures, restoreOne(ctx, iid, labels, original, client))
	}
	var original managedObject
	if old.service != nil {
		original = old.service
	}
	failures = errors.Join(failures, restoreOne(ctx, iid, labels, original, restoreClient{
		get: func(ctx context.Context) (managedObject, error) {
			return c.Kube.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{})
		},
		put: func(ctx context.Context, o managedObject, found bool) error {
			value, ok := o.(*corev1.Service)
			if !ok {
				return errors.New("invalid typed rollback resource")
			}
			if found {
				_, err := c.Kube.CoreV1().Services(ns).Update(ctx, value, metav1.UpdateOptions{})
				return err
			}
			_, err := c.Kube.CoreV1().Services(ns).Create(ctx, value, metav1.CreateOptions{})
			return err
		},
		remove: func(ctx context.Context, o managedObject) error {
			return c.Kube.CoreV1().Services(ns).Delete(ctx, name, deleteOwnedOptions(o))
		},
	}))
	original = nil
	if old.deployment != nil {
		original = old.deployment
	}
	failures = errors.Join(failures, restoreOne(ctx, iid, labels, original, restoreClient{
		get: func(ctx context.Context) (managedObject, error) {
			return c.Kube.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		},
		put: func(ctx context.Context, o managedObject, found bool) error {
			value, ok := o.(*appsv1.Deployment)
			if !ok {
				return errors.New("invalid typed rollback resource")
			}
			if found {
				_, err := c.Kube.AppsV1().Deployments(ns).Update(ctx, value, metav1.UpdateOptions{})
				return err
			}
			_, err := c.Kube.AppsV1().Deployments(ns).Create(ctx, value, metav1.CreateOptions{})
			return err
		},
		remove: func(ctx context.Context, o managedObject) error {
			return c.Kube.AppsV1().Deployments(ns).Delete(ctx, name, deleteOwnedOptions(o))
		},
	}))
	if labels[owned.LabelPublic] == "true" || labels[owned.LabelManageIngress] == "true" {
		original = nil
		if old.ingress != nil {
			original = old.ingress
		}
		failures = errors.Join(failures, restoreOne(ctx, iid, labels, original, restoreClient{
			get: func(ctx context.Context) (managedObject, error) {
				return c.Kube.NetworkingV1().Ingresses(ns).Get(ctx, name, metav1.GetOptions{})
			},
			put: func(ctx context.Context, o managedObject, found bool) error {
				value, ok := o.(*networkingv1.Ingress)
				if !ok {
					return errors.New("invalid typed rollback resource")
				}
				if found {
					_, err := c.Kube.NetworkingV1().Ingresses(ns).Update(ctx, value, metav1.UpdateOptions{})
					return err
				}
				_, err := c.Kube.NetworkingV1().Ingresses(ns).Create(ctx, value, metav1.CreateOptions{})
				return err
			},
			remove: func(ctx context.Context, o managedObject) error {
				return c.Kube.NetworkingV1().Ingresses(ns).Delete(ctx, name, deleteOwnedOptions(o))
			},
		}))
	}
	if labels[owned.LabelManageHPA] == "true" {
		original = nil
		if old.hpa != nil {
			original = old.hpa
		}
		failures = errors.Join(failures, restoreOne(ctx, iid, labels, original, restoreClient{
			get: func(ctx context.Context) (managedObject, error) {
				return c.Kube.AutoscalingV2().HorizontalPodAutoscalers(ns).Get(ctx, name, metav1.GetOptions{})
			},
			put: func(ctx context.Context, o managedObject, found bool) error {
				value, ok := o.(*autoscalingv2.HorizontalPodAutoscaler)
				if !ok {
					return errors.New("invalid typed rollback resource")
				}
				if found {
					_, err := c.Kube.AutoscalingV2().HorizontalPodAutoscalers(ns).Update(ctx, value, metav1.UpdateOptions{})
					return err
				}
				_, err := c.Kube.AutoscalingV2().HorizontalPodAutoscalers(ns).Create(ctx, value, metav1.CreateOptions{})
				return err
			},
			remove: func(ctx context.Context, o managedObject) error {
				return c.Kube.AutoscalingV2().HorizontalPodAutoscalers(ns).Delete(ctx, name, deleteOwnedOptions(o))
			},
		}))
	}
	return failures
}

func deleteOwnedOptions(o metav1.Object) metav1.DeleteOptions {
	options := metav1.DeleteOptions{}
	if uid := o.GetUID(); uid != "" {
		options.Preconditions = &metav1.Preconditions{UID: &uid}
	}
	return options
}

func runtimeAutoscaler(iid id.ID, labels map[string]string, ns string) (*autoscalingv2.HorizontalPodAutoscaler, error) {
	minCount, minErr := strconv.ParseInt(labels[owned.LabelScaleMin], 10, 32)
	maxCount, maxErr := strconv.ParseInt(labels[owned.LabelScaleMax], 10, 32)
	if minErr != nil || maxErr != nil || minCount < 1 || maxCount < minCount {
		return nil, errors.New("invalid Kubernetes autoscaling range")
	}
	minimum, utilization := int32(minCount), int32(80)
	hpa := &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Name: WorkloadName(iid), Namespace: ns, Labels: objectLabels(iid, labels)}, Spec: autoscalingv2.HorizontalPodAutoscalerSpec{ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: WorkloadName(iid)}, MinReplicas: &minimum, MaxReplicas: int32(maxCount), Metrics: []autoscalingv2.MetricSpec{{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricSource{Name: corev1.ResourceCPU, Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: &utilization}}}}}}
	return hpa, nil
}
