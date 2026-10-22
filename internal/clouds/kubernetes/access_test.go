package kubernetes

import (
	"context"
	"encoding/pem"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

const discoveryRules = `[{"api_groups":[""],"resources":["services","endpoints"],"verbs":["get","list","watch"]},{"api_groups":["discovery.k8s.io"],"resources":["endpointslices"],"verbs":["get","list","watch"]}]`

func accessFixture() (*Runtime, *fake.Clientset, cpprovider.ServiceSpec) {
	kube := fake.NewClientset()
	client := &Client{Config: &Config{Namespace: "selected"}, Kube: kube}
	runtime := &Runtime{WaitReady: func(context.Context, *Client, string) error { return nil }, Session: func(context.Context, map[string]string) (*Client, error) { return client, nil }}
	spec := cpprovider.ServiceSpec{Name: "gateway", Image: "registry.example/sample-gateway@sha256:fixture", Ports: []cpprovider.PortSpec{{Container: 8080, Protocol: "tcp"}}}
	return runtime, kube, spec
}

func accessLabels(rules string) map[string]string {
	labels := map[string]string{owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelPublic: "false", owned.LabelCatalogKind: "sample", owned.LabelCatalogResource: "app", owned.LabelCatalogComponent: "gateway"}
	if rules != "" {
		labels[owned.LabelKubernetesRules] = rules
	}
	return labels
}

func accessObjects(t *testing.T, kube *fake.Clientset, name string) (*corev1.ServiceAccount, *rbacv1.Role, *rbacv1.RoleBinding) {
	t.Helper()
	account, err := kube.CoreV1().ServiceAccounts("selected").Get(t.Context(), name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	if err != nil {
		account = nil
	}
	role, err := kube.RbacV1().Roles("selected").Get(t.Context(), name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	if err != nil {
		role = nil
	}
	binding, err := kube.RbacV1().RoleBindings("selected").Get(t.Context(), name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	if err != nil {
		binding = nil
	}
	return account, role, binding
}

func TestKubernetesComponentGetsNamespacedReadRole(t *testing.T) {
	runtime, kube, spec := accessFixture()
	iid := id.New(id.PrefixInstance)
	labels := accessLabels(discoveryRules)
	if _, err := runtime.Apply(t.Context(), iid, spec, labels); err != nil {
		t.Fatal(err)
	}
	name := WorkloadName(iid)
	account, role, binding := accessObjects(t, kube, name)
	if account == nil || role == nil || binding == nil {
		t.Fatalf("access objects: %v %v %v", account, role, binding)
	}
	identity := objectLabels(iid, labels)
	for _, object := range []metav1.Object{account, role, binding} {
		if object.GetNamespace() != "selected" || !reflect.DeepEqual(object.GetLabels(), identity) {
			t.Fatalf("%s: namespace %q labels %v", object.GetName(), object.GetNamespace(), object.GetLabels())
		}
	}
	want := []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"services", "endpoints"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{"discovery.k8s.io"}, Resources: []string{"endpointslices"}, Verbs: []string{"get", "list", "watch"}},
	}
	if !reflect.DeepEqual(role.Rules, want) {
		t.Fatalf("role rules: %+v", role.Rules)
	}
	if binding.RoleRef != (rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: name}) || !reflect.DeepEqual(binding.Subjects, []rbacv1.Subject{{Kind: "ServiceAccount", Name: name, Namespace: "selected"}}) {
		t.Fatalf("role binding: %+v %+v", binding.RoleRef, binding.Subjects)
	}
	deployment, err := kube.AppsV1().Deployments("selected").Get(t.Context(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod := deployment.Spec.Template.Spec
	if pod.ServiceAccountName != name || pod.AutomountServiceAccountToken == nil || !*pod.AutomountServiceAccountToken {
		t.Fatalf("pod account %q automount %v", pod.ServiceAccountName, pod.AutomountServiceAccountToken)
	}
	var order []string
	for _, action := range kube.Actions() {
		if action.GetVerb() == "create" && slices.Contains([]string{"serviceaccounts", "roles", "rolebindings"}, action.GetResource().Resource) {
			order = append(order, action.GetResource().Resource)
		}
	}
	if !slices.Equal(order, []string{"serviceaccounts", "roles", "rolebindings"}) {
		t.Fatalf("created %v", order)
	}

	// A second apply updates the same objects in place.
	kube.ClearActions()
	if _, err = runtime.Apply(t.Context(), iid, spec, labels); err != nil {
		t.Fatal(err)
	}
	order = nil
	for _, action := range kube.Actions() {
		if slices.Contains([]string{"serviceaccounts", "roles", "rolebindings"}, action.GetResource().Resource) && action.GetVerb() != "get" {
			order = append(order, action.GetVerb()+" "+action.GetResource().Resource)
		}
	}
	if !slices.Equal(order, []string{"update serviceaccounts", "update roles", "update rolebindings"}) {
		t.Fatalf("second apply: %v", order)
	}
}

func TestKubernetesComponentRefusesRulesOutsideDiscovery(t *testing.T) {
	for name, rules := range map[string]string{
		"secrets": `[{"api_groups":[""],"resources":["secrets"],"verbs":["get"]}]`,
		"write":   `[{"api_groups":[""],"resources":["services"],"verbs":["create"]}]`,
		"group":   `[{"api_groups":["apps"],"resources":["services"],"verbs":["get"]}]`,
		"empty":   `[]`,
		"invalid": `{`,
	} {
		t.Run(name, func(t *testing.T) {
			runtime, kube, spec := accessFixture()
			if _, err := runtime.Apply(t.Context(), id.New(id.PrefixInstance), spec, accessLabels(rules)); err == nil {
				t.Fatal("applied rules outside discovery")
			}
			for _, action := range kube.Actions() {
				if slices.Contains([]string{"serviceaccounts", "roles", "rolebindings"}, action.GetResource().Resource) && action.GetVerb() != "get" {
					t.Fatalf("wrote %s %s", action.GetVerb(), action.GetResource().Resource)
				}
			}
		})
	}
}

func TestKubernetesComponentWithoutRulesKeepsDefaultAccount(t *testing.T) {
	runtime, kube, spec := accessFixture()
	iid := id.New(id.PrefixInstance)
	if _, err := runtime.Apply(t.Context(), iid, spec, accessLabels("")); err != nil {
		t.Fatal(err)
	}
	name := WorkloadName(iid)
	if account, role, binding := accessObjects(t, kube, name); account != nil || role != nil || binding != nil {
		t.Fatalf("access without rules: %v %v %v", account, role, binding)
	}
	deployment, err := kube.AppsV1().Deployments("selected").Get(t.Context(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if pod := deployment.Spec.Template.Spec; pod.ServiceAccountName != "" || pod.AutomountServiceAccountToken != nil {
		t.Fatalf("pod account %q automount %v", pod.ServiceAccountName, pod.AutomountServiceAccountToken)
	}

	// Rules dropped by a later revision take their objects with them.
	if _, err = runtime.Apply(t.Context(), iid, spec, accessLabels(discoveryRules)); err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.Apply(t.Context(), iid, spec, accessLabels("")); err != nil {
		t.Fatal(err)
	}
	if account, role, binding := accessObjects(t, kube, name); account != nil || role != nil || binding != nil {
		t.Fatalf("leftover access: %v %v %v", account, role, binding)
	}
	deployment, err = kube.AppsV1().Deployments("selected").Get(t.Context(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if pod := deployment.Spec.Template.Spec; pod.ServiceAccountName != "" || pod.AutomountServiceAccountToken != nil {
		t.Fatalf("pod kept account %q automount %v", pod.ServiceAccountName, pod.AutomountServiceAccountToken)
	}
}

func TestKubernetesRemoveDeletesComponentAccess(t *testing.T) {
	runtime, kube, spec := accessFixture()
	iid := id.New(id.PrefixInstance)
	labels := accessLabels(discoveryRules)
	if _, err := runtime.Apply(t.Context(), iid, spec, labels); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Remove(t.Context(), iid, labels); err != nil {
		t.Fatal(err)
	}
	if account, role, binding := accessObjects(t, kube, WorkloadName(iid)); account != nil || role != nil || binding != nil {
		t.Fatalf("remove left access: %v %v %v", account, role, binding)
	}

	// Objects another instance owns keep their name, labels and content.
	other := id.New(id.PrefixInstance)
	name := WorkloadName(other)
	foreign := objectLabels(id.New(id.PrefixInstance), labels)
	seeded := []any{
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "selected", Labels: foreign}},
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "selected", Labels: foreign}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "selected", Labels: foreign}, RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: name}},
	}
	for _, object := range seeded {
		var err error
		switch value := object.(type) {
		case *corev1.ServiceAccount:
			_, err = kube.CoreV1().ServiceAccounts("selected").Create(t.Context(), value, metav1.CreateOptions{})
		case *rbacv1.Role:
			_, err = kube.RbacV1().Roles("selected").Create(t.Context(), value, metav1.CreateOptions{})
		case *rbacv1.RoleBinding:
			_, err = kube.RbacV1().RoleBindings("selected").Create(t.Context(), value, metav1.CreateOptions{})
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	kube.ClearActions()
	if err := runtime.Remove(t.Context(), other, labels); err != nil {
		t.Fatal(err)
	}
	for _, action := range kube.Actions() {
		if action.GetVerb() != "get" && action.GetVerb() != "list" {
			t.Fatalf("touched a foreign object: %s %s", action.GetVerb(), action.GetResource().Resource)
		}
	}
	if account, role, binding := accessObjects(t, kube, name); account == nil || role == nil || binding == nil {
		t.Fatalf("remove deleted foreign access: %v %v %v", account, role, binding)
	}
	if _, err := runtime.Apply(t.Context(), other, spec, labels); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("apply over foreign access: %v", err)
	}
}

func TestKubernetesCheckReportsRBAC(t *testing.T) {
	check := func(denied ...string) (*Result, []string) {
		t.Helper()
		s, paths, mu := clusterFixture(t, false, denied...)
		ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw})
		cfg, err := ParseConfig(kubeconfig(t, s.URL, ca), map[string]string{"namespace": "production"})
		if err != nil {
			t.Fatal(err)
		}
		c, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		result, err := c.Check(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		return result, slices.Clone(*paths)
	}
	result, paths := check()
	if result.Details["rbac"] != "ready" {
		t.Fatalf("rbac: %+v", result.Details)
	}
	want := make([]string, 0, 21)
	for _, resource := range []string{"serviceaccounts", "roles", "rolebindings"} {
		for _, verb := range []string{"create", "get", "update", "delete"} {
			want = append(want, "production:"+resource+":"+verb)
		}
	}
	for _, resource := range []string{"services", "endpoints", "endpointslices"} {
		for _, verb := range []string{"get", "list", "watch"} {
			want = append(want, "production:"+resource+":"+verb)
		}
	}
	for _, permission := range want {
		if !slices.Contains(paths, permission) {
			t.Fatalf("unchecked rbac permission %s", permission)
		}
	}
	for _, denied := range []string{"serviceaccounts:create", "roles:update", "rolebindings:delete", "services:list", "endpoints:watch", "endpointslices:get"} {
		result, _ = check(denied)
		if _, ok := result.Details["rbac"]; ok {
			t.Fatalf("without %s, rbac = %q", denied, result.Details["rbac"])
		}
		if len(result.Missing) > 0 {
			t.Fatalf("without %s the connection is incomplete: %v", denied, result.Missing)
		}
	}
}
