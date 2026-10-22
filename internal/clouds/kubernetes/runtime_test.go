package kubernetes

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kuberuntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

func TestRuntime_namespaceSecretsAndFixedReplicas(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	kube := fake.NewClientset()
	client := &Client{Config: &Config{Namespace: "selected"}, Kube: kube}
	runtime := &Runtime{WaitReady: func(context.Context, *Client, string) error { return nil }, Session: func(context.Context, map[string]string) (*Client, error) { return client, nil }, Values: func(context.Context, map[string]string) (owned.RuntimeValues, error) {
		return owned.RuntimeValues{Env: map[string]string{"TOKEN": "runtime-private-value"}}, nil
	}}
	labels := map[string]string{owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelReplicas: "2", owned.LabelHealth: "/_/health"}
	result, err := runtime.Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "main", Image: "registry.example.com/team/api@sha256:fixture", Env: map[string]string{"MODE": "production"}, Ports: []cpprovider.PortSpec{{Container: 8080}}}, labels)
	if err != nil {
		t.Fatal(err)
	}
	name := WorkloadName(iid)
	deployment, err := kube.AppsV1().Deployments("selected").Get(t.Context(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if *deployment.Spec.Replicas != 2 || deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.HTTPGet.Path != "/_/health" {
		t.Fatalf("workload: %+v", deployment.Spec)
	}
	secret, err := kube.CoreV1().Secrets("selected").Get(t.Context(), name+"-runtime", metav1.GetOptions{})
	if err != nil || string(secret.Data["TOKEN"]) != "runtime-private-value" {
		t.Fatalf("runtime secret: %+v %v", secret, err)
	}
	for _, env := range deployment.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "TOKEN" && (env.Value != "" || env.ValueFrom == nil || env.ValueFrom.SecretKeyRef.Name != secret.Name) {
			t.Fatal("secret was stored as a plain environment value")
		}
	}
	if len(result.Endpoints) != 1 || result.Endpoints[0].Public {
		t.Fatalf("private endpoint: %+v", result)
	}
	for _, action := range kube.Actions() {
		if action.GetNamespace() != "selected" {
			t.Fatalf("namespace escaped: %+v", action)
		}
	}
}

func TestRuntime_refusesForeignObjectsBeforeMutation(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	name := WorkloadName(iid)
	kube := fake.NewClientset(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "selected", Labels: map[string]string{owned.LabelOrg: "org_other"}}}, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "selected"}})
	runtime := &Runtime{WaitReady: func(context.Context, *Client, string) error { return nil }, Session: func(context.Context, map[string]string) (*Client, error) {
		return &Client{Config: &Config{Namespace: "selected"}, Kube: kube}, nil
	}}
	_, err := runtime.Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "main", Image: "image"}, map[string]string{owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture"})
	if err == nil {
		t.Fatal("foreign workload was adopted")
	}
	for _, action := range kube.Actions() {
		if action.GetVerb() != "get" {
			t.Fatalf("foreign object led to mutation: %+v", action)
		}
	}
}

func TestRuntime_failedRolloutRestoresSupportingResources(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	name := WorkloadName(iid)
	labels := map[string]string{owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture"}
	meta := metav1.ObjectMeta{Name: name, Namespace: "selected", Labels: objectLabels(iid, labels)}
	replicas := int32(1)
	prior := &appsv1.Deployment{ObjectMeta: meta, Spec: appsv1.DeploymentSpec{Replicas: &replicas, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "old"}}}}}}
	secret := &corev1.Secret{ObjectMeta: meta, Data: map[string][]byte{"TOKEN": []byte("prior-value")}}
	secret.Name = name + "-runtime"
	service := &corev1.Service{ObjectMeta: meta, Spec: corev1.ServiceSpec{ClusterIP: "10.0.0.1", Ports: []corev1.ServicePort{{Port: 8080}}}}
	kube := fake.NewClientset(prior, secret, service)
	r := &Runtime{Session: func(context.Context, map[string]string) (*Client, error) {
		return &Client{Config: &Config{Namespace: "selected"}, Kube: kube}, nil
	}, Values: func(context.Context, map[string]string) (owned.RuntimeValues, error) {
		return owned.RuntimeValues{Env: map[string]string{"TOKEN": "new-value"}}, nil
	}, WaitReady: func(context.Context, *Client, string) error { return errors.New("readiness timeout") }}
	if _, err := r.Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "main", Image: "new", Ports: []cpprovider.PortSpec{{Container: 9090}}}, labels); err == nil {
		t.Fatal("accepted failed rollout")
	}
	restored, err := kube.AppsV1().Deployments("selected").Get(t.Context(), name, metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(restored.Spec, prior.Spec) {
		t.Fatalf("deployment not restored: %+v %v", restored, err)
	}
	restoredSecret, err := kube.CoreV1().Secrets("selected").Get(t.Context(), secret.Name, metav1.GetOptions{})
	if err != nil || string(restoredSecret.Data["TOKEN"]) != "prior-value" {
		t.Fatalf("secret not restored: %+v %v", restoredSecret, err)
	}
	restoredService, err := kube.CoreV1().Services("selected").Get(t.Context(), name, metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(restoredService.Spec, service.Spec) {
		t.Fatalf("service not restored: %+v %v", restoredService, err)
	}
}

func TestRuntime_waitsForNewGenerationBeforeAcceptingFailure(t *testing.T) {
	replicas := int32(1)
	applied := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: "selected", UID: "uid", Generation: 2}, Spec: appsv1.DeploymentSpec{Replicas: &replicas}, Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Message: "old failure"}}}}
	kube := fake.NewClientset()
	var reads atomic.Int32
	kube.PrependReactor("get", "deployments", func(kubetesting.Action) (bool, kuberuntime.Object, error) {
		d := applied.DeepCopy()
		if reads.Add(1) > 1 {
			d.Status = appsv1.DeploymentStatus{ObservedGeneration: 2, UpdatedReplicas: 1, AvailableReplicas: 1}
		}
		return true, d, nil
	})
	r := &Runtime{Wait: 2 * time.Second}
	if err := r.waitReady(t.Context(), &Client{Config: &Config{Namespace: "selected"}, Kube: kube}, applied); err != nil {
		t.Fatal(err)
	}
	if reads.Load() < 2 {
		t.Fatal("stale failure was used")
	}
}

func TestRuntime_waitsThroughAutoscalerReplicaChange(t *testing.T) {
	replicas := int32(1)
	applied := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: "selected", UID: "uid", Generation: 2}, Spec: appsv1.DeploymentSpec{Replicas: &replicas, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{owned.LabelRevision: "reviewed"}}}}}
	current := applied.DeepCopy()
	scaled := int32(3)
	current.Generation = 3
	current.Spec.Replicas = &scaled
	current.Status = appsv1.DeploymentStatus{ObservedGeneration: 3, UpdatedReplicas: 3, AvailableReplicas: 3}
	kube := fake.NewClientset(current)
	if err := (&Runtime{Wait: time.Second}).waitReady(t.Context(), &Client{Config: &Config{Namespace: "selected"}, Kube: kube}, applied); err != nil {
		t.Fatal(err)
	}
}

func TestRuntime_stopsAutoscalingAndRestoresItOnStart(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	name := WorkloadName(iid)
	kube := fake.NewClientset()
	client := &Client{Config: &Config{Namespace: "selected"}, Kube: kube}
	r := &Runtime{Session: func(context.Context, map[string]string) (*Client, error) { return client, nil }, WaitReady: func(context.Context, *Client, string) error { return nil }}
	labels := map[string]string{owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelManageHPA: "true", owned.LabelScaleMin: "1", owned.LabelScaleMax: "3", owned.LabelReplicas: "1"}
	if _, err := r.Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "main", Image: "image", Resources: cpprovider.ResourceSpec{CPUMillis: 100}}, labels); err != nil {
		t.Fatal(err)
	}
	if err := r.SetRunning(t.Context(), iid, labels, false); err != nil {
		t.Fatal(err)
	}
	d, err := kube.AppsV1().Deployments("selected").Get(t.Context(), name, metav1.GetOptions{})
	if err != nil || *d.Spec.Replicas != 0 {
		t.Fatalf("not stopped: %+v %v", d, err)
	}
	if _, err = kube.AutoscalingV2().HorizontalPodAutoscalers("selected").Get(t.Context(), name, metav1.GetOptions{}); err == nil {
		t.Fatal("autoscaler survived stop")
	}
	if err = r.SetRunning(t.Context(), iid, labels, true); err != nil {
		t.Fatal(err)
	}
	d, err = kube.AppsV1().Deployments("selected").Get(t.Context(), name, metav1.GetOptions{})
	if err != nil || *d.Spec.Replicas != 1 {
		t.Fatalf("not started: %+v %v", d, err)
	}
	if _, err = kube.AutoscalingV2().HorizontalPodAutoscalers("selected").Get(t.Context(), name, metav1.GetOptions{}); err != nil {
		t.Fatal("autoscaler not restored", err)
	}
}

func TestKubernetesStatelessComponentIsADeployment(t *testing.T) {
	kube := fake.NewClientset()
	client := &Client{Config: &Config{Namespace: "selected"}, Kube: kube}
	runtime := &Runtime{WaitReady: func(context.Context, *Client, string) error { return nil }, Session: func(context.Context, map[string]string) (*Client, error) { return client, nil }}
	component := func(health string) map[string]string {
		return map[string]string{owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelPublic: "false", owned.LabelHealth: "/readyz", owned.LabelCatalogKind: "sample", owned.LabelCatalogResource: "app", owned.LabelCatalogComponent: "api", owned.LabelHealthPort: health}
	}
	spec := cpprovider.ServiceSpec{Name: "api", Image: "registry.example/sample-api@sha256:fixture", Ports: []cpprovider.PortSpec{{Container: 8080, Protocol: "tcp"}, {Container: 9090, Protocol: "tcp"}}}
	probed := func(health string) (*appsv1.Deployment, *corev1.Service) {
		t.Helper()
		iid := id.New(id.PrefixInstance)
		if _, err := runtime.Apply(t.Context(), iid, spec, component(health)); err != nil {
			t.Fatal(err)
		}
		deployment, err := kube.AppsV1().Deployments("selected").Get(t.Context(), WorkloadName(iid), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		service, err := kube.CoreV1().Services("selected").Get(t.Context(), WorkloadName(iid), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return deployment, service
	}

	deployment, service := probed("8080")
	want := []corev1.ServicePort{{Name: "http-0", Port: 8080, TargetPort: intstr.FromInt(8080)}, {Name: "http-1", Port: 9090, TargetPort: intstr.FromInt(9090)}}
	if service.Spec.Type != corev1.ServiceTypeClusterIP || service.Spec.ClusterIP == corev1.ClusterIPNone || !reflect.DeepEqual(service.Spec.Ports, want) {
		t.Fatalf("component service: %+v", service.Spec)
	}
	probe := deployment.Spec.Template.Spec.Containers[0].ReadinessProbe
	if probe == nil || probe.HTTPGet == nil || probe.HTTPGet.Path != "/readyz" || probe.HTTPGet.Port.IntValue() != 8080 {
		t.Fatalf("component readiness: %+v", probe)
	}

	deployment, _ = probed("9090")
	if probe = deployment.Spec.Template.Spec.Containers[0].ReadinessProbe; probe == nil || probe.HTTPGet == nil || probe.HTTPGet.Port.IntValue() != 9090 {
		t.Fatalf("readiness ignored the declared health port: %+v", probe)
	}
	if _, err := runtime.Apply(t.Context(), id.New(id.PrefixInstance), spec, component("7070")); err == nil {
		t.Fatal("readiness on a port the workload doesn't declare")
	}
}

func TestKubernetesMountsFilesFromTheRuntimeSecret(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	kube := fake.NewClientset()
	client := &Client{Config: &Config{Namespace: "selected"}, Kube: kube}
	runtime := &Runtime{WaitReady: func(context.Context, *Client, string) error { return nil }, Session: func(context.Context, map[string]string) (*Client, error) { return client, nil }, Values: func(context.Context, map[string]string) (owned.RuntimeValues, error) {
		return owned.RuntimeValues{
			Env:       map[string]string{"TOKEN": "runtime-private-value"},
			Files:     map[string][]byte{"/etc/sample/config.yaml": []byte("listen: 8080\n"), "/etc/sample/rules.json": []byte("{}")},
			FileModes: map[string]fs.FileMode{"/etc/sample/rules.json": 0o440},
		}, nil
	}}
	labels := map[string]string{owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelRuntimeFiles: "true"}
	if _, err := runtime.Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "main", Image: "registry.example.com/sample@sha256:fixture", Ports: []cpprovider.PortSpec{{Container: 8080}}}, labels); err != nil {
		t.Fatal(err)
	}
	name := WorkloadName(iid)
	secret, err := kube.CoreV1().Secrets("selected").Get(t.Context(), name+"-runtime", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(secret.Data["file-0"]) != "listen: 8080\n" || string(secret.Data["file-1"]) != "{}" || string(secret.Data["TOKEN"]) != "runtime-private-value" || len(secret.Data) != 3 {
		t.Fatalf("runtime secret data: %q", secret.Data)
	}
	deployment, err := kube.AppsV1().Deployments("selected").Get(t.Context(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod := deployment.Spec.Template.Spec
	container := pod.Containers[0]
	wantMounts := []corev1.VolumeMount{
		{Name: "runtime-files", MountPath: "/etc/sample/config.yaml", SubPath: "file-0", ReadOnly: true},
		{Name: "runtime-files", MountPath: "/etc/sample/rules.json", SubPath: "file-1", ReadOnly: true},
	}
	if !reflect.DeepEqual(container.VolumeMounts, wantMounts) {
		t.Fatalf("volume mounts: %+v", container.VolumeMounts)
	}
	readable, restricted, fallback := int32(0o444), int32(0o440), int32(0o444)
	wantVolumes := []corev1.Volume{{Name: "runtime-files", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
		SecretName: secret.Name, DefaultMode: &fallback,
		Items: []corev1.KeyToPath{{Key: "file-0", Path: "file-0", Mode: &readable}, {Key: "file-1", Path: "file-1", Mode: &restricted}},
	}}}}
	if !reflect.DeepEqual(pod.Volumes, wantVolumes) {
		t.Fatalf("volumes: %+v", pod.Volumes)
	}
	for _, env := range container.Env {
		if strings.HasPrefix(env.Name, "file-") {
			t.Fatalf("a file became an environment variable: %+v", env)
		}
	}
}
