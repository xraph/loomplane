package kubernetes

import (
	"context"
	"errors"
	"testing"

	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	kt "k8s.io/client-go/testing"

	"github.com/xraph/loomplane/pkg/provider"
)

func instancePolicyFixture() *provider.InstanceIsolationPolicy {
	return &provider.InstanceIsolationPolicy{Revision: provider.InstanceIsolationRevision, PodSecurityVersion: "v1.34", CPUMillis: 2000, MemoryMiB: 1024, EphemeralMiB: 128, MaxPods: 4}
}

func TestInstanceIsolationRejectsUnqualifiedPeers(t *testing.T) {
	for _, uid := range []string{"original-peer", "recreated-peer"} {
		t.Run(uid, func(t *testing.T) {
			k, in, state := enrollmentFixture()
			peer := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shared", UID: types.UID(uid), Labels: map[string]string{"kubernetes.io/metadata.name": "shared", "loomplane.io/namespace-uid": "original-peer"}}}
			if err := k.Tracker().Create(corev1.SchemeGroupVersion.WithResource("namespaces"), peer, ""); err != nil {
				t.Fatal(err)
			}
			in.InstancePolicy = instancePolicyFixture()
			in.InstancePolicy.EgressPeers = []provider.InstanceNamespacePeer{{Namespace: "shared", NamespaceUID: "original-peer", PodSelector: map[string]string{"app": "resolver"}, Ports: []provider.InstanceNetworkPort{{Protocol: "UDP", Port: 53}}}}
			if err := EnrollRuntime(t.Context(), k, in, state, func(context.Context, *EnrollmentState) error { return nil }); err == nil {
				t.Fatal("unqualified peer namespace was allowed")
			}
			for _, action := range k.Actions() {
				if action.GetVerb() == "create" || action.GetVerb() == "update" || action.GetVerb() == "patch" {
					t.Fatal("unsupported peer rule caused a mutation")
				}
			}
		})
	}
}

func TestInstanceIsolationMatchesAPIDefaults(t *testing.T) {
	_, in, _ := enrollmentFixture()
	in.InstancePolicy = instancePolicyFixture()
	_, desired, _, err := instanceIsolationObjects(in)
	if err != nil {
		t.Fatal(err)
	}
	observed := desired.DeepCopy()
	// The API server fills container defaults from Max, then requests from defaults.
	for i := range observed.Spec.Limits {
		item := &observed.Spec.Limits[i]
		if item.Default == nil {
			item.Default = corev1.ResourceList{}
		}
		if item.DefaultRequest == nil {
			item.DefaultRequest = corev1.ResourceList{}
		}
		for key, value := range item.Max {
			if _, exists := item.Default[key]; !exists {
				item.Default[key] = value.DeepCopy()
			}
		}
		for key, value := range item.Default {
			if _, exists := item.DefaultRequest[key]; !exists {
				item.DefaultRequest[key] = value.DeepCopy()
			}
		}
	}
	if !sameInstanceIsolationObject(desired, observed) {
		t.Fatal("API defaulting invalidated the reviewed LimitRange")
	}
	observed.Spec.Limits[0].DefaultRequest[corev1.ResourceCPU] = resource.MustParse("1m")
	if sameInstanceIsolationObject(desired, observed) {
		t.Fatal("changed resource default accepted")
	}
}

func TestInstanceNamespaceLostResponse(t *testing.T) {
	k, in, state := enrollmentFixture()
	in.InstancePolicy = instancePolicyFixture()
	lost := errors.New("response lost")
	first := true
	k.PrependReactor("create", "namespaces", func(a kt.Action) (bool, runtime.Object, error) {
		if !first {
			return false, nil, nil
		}
		first = false
		object := a.(kt.CreateAction).GetObject().(*corev1.Namespace).DeepCopy()
		object.UID = "namespace-receipt"
		if err := k.Tracker().Create(corev1.SchemeGroupVersion.WithResource("namespaces"), object, ""); err != nil {
			t.Fatal(err)
		}
		return true, nil, lost
	})
	checkpoint := func(context.Context, *EnrollmentState) error { return nil }
	if err := EnrollRuntime(t.Context(), k, in, state, checkpoint); !errors.Is(err, lost) {
		t.Fatal(err)
	}
	if state.NamespaceUID != "" {
		t.Fatal("lost response fabricated a receipt")
	}
	if err := EnrollRuntime(t.Context(), k, in, state, checkpoint); err != nil {
		t.Fatal(err)
	}
	if state.NamespaceUID != "namespace-receipt" {
		t.Fatal("namespace was replaced")
	}
	ns, err := k.CoreV1().Namespaces().Get(t.Context(), in.Namespace, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ns.Labels["pod-security.kubernetes.io/enforce"] != "restricted" || ns.Labels["pod-security.kubernetes.io/enforce-version"] != "v1.34" {
		t.Fatal("namespace is not restricted")
	}
	quota, err := k.CoreV1().ResourceQuotas(in.Namespace).Get(t.Context(), instanceIsolationObjectName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cpu, pods := quota.Spec.Hard[corev1.ResourceRequestsCPU], quota.Spec.Hard[corev1.ResourcePods]
	if cpu.MilliValue() != 2000 || pods.Value() != 4 {
		t.Fatal("quota differs from reservation")
	}
	if state.Isolation == nil || state.Isolation.Digest == "" {
		t.Fatal("policies were not receipted")
	}
}

func TestInstanceNamespaceUIDChanged(t *testing.T) {
	k, in, state := enrollmentFixture()
	in.InstancePolicy = instancePolicyFixture()
	checkpoint := func(context.Context, *EnrollmentState) error { return nil }
	if err := EnrollRuntime(t.Context(), k, in, state, checkpoint); err != nil {
		t.Fatal(err)
	}
	ns, err := k.CoreV1().Namespaces().Get(t.Context(), in.Namespace, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ns.UID = "replacement"
	if _, err = k.CoreV1().Namespaces().Update(t.Context(), ns, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	before := len(k.Actions())
	if err = EnrollRuntime(t.Context(), k, in, state, checkpoint); err == nil {
		t.Fatal("replacement namespace accepted")
	}
	for _, a := range k.Actions()[before:] {
		if a.GetVerb() != "get" && a.GetVerb() != "list" {
			t.Fatal("replacement caused a write")
		}
	}
}

func TestInstanceIsolationPolicyDriftRejected(t *testing.T) {
	k, in, state := enrollmentFixture()
	in.InstancePolicy = instancePolicyFixture()
	checkpoint := func(context.Context, *EnrollmentState) error { return nil }
	if err := EnrollRuntime(t.Context(), k, in, state, checkpoint); err != nil {
		t.Fatal(err)
	}
	policy, err := k.NetworkingV1().NetworkPolicies(in.Namespace).Get(t.Context(), instanceIsolationObjectName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	policy.Spec.Egress = []networkingv1.NetworkPolicyEgressRule{{}}
	if _, err = k.NetworkingV1().NetworkPolicies(in.Namespace).Update(t.Context(), policy, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = EnrollRuntime(t.Context(), k, in, state, checkpoint); err == nil {
		t.Fatal("allow-all egress accepted")
	}
}

func TestInstanceIsolationQuotaLostResponse(t *testing.T) {
	k, in, state := enrollmentFixture()
	in.InstancePolicy = instancePolicyFixture()
	lost := errors.New("response lost")
	creates := 0
	k.PrependReactor("create", "resourcequotas", func(a kt.Action) (bool, runtime.Object, error) {
		creates++
		object := a.(kt.CreateAction).GetObject().(*corev1.ResourceQuota).DeepCopy()
		object.UID = "quota-receipt"
		if err := k.Tracker().Create(corev1.SchemeGroupVersion.WithResource("resourcequotas"), object, in.Namespace); err != nil {
			t.Fatal(err)
		}
		return true, nil, lost
	})
	checkpoint := func(context.Context, *EnrollmentState) error { return nil }
	if err := EnrollRuntime(t.Context(), k, in, state, checkpoint); !errors.Is(err, lost) {
		t.Fatal(err)
	}
	if err := EnrollRuntime(t.Context(), k, in, state, checkpoint); err != nil {
		t.Fatal(err)
	}
	if creates != 1 {
		t.Fatal("quota create repeated", creates)
	}
}

func TestInstanceLifecycleJobPermissionsAreNarrow(t *testing.T) {
	_, in, _ := enrollmentFixture()
	in.RuntimeAccess = &provider.RuntimeAccessProfile{SchemaVersion: 1, CleanupDependents: true, LifecycleJobs: true}
	rules, err := EnrollmentRoleRules(in)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rule := range rules {
		for _, r := range rule.Resources {
			if r != "jobs" {
				continue
			}
			found = true
			if len(rule.APIGroups) != 1 || rule.APIGroups[0] != "batch" || len(rule.Verbs) != 3 || rule.Verbs[0] != "get" || rule.Verbs[1] != "create" || rule.Verbs[2] != "delete" {
				t.Fatal("jobs received broader permissions", rule)
			}
		}
	}
	if !found {
		t.Fatal("jobs permissions missing")
	}
}

func TestInstanceIsolationCannotBeRemovedOnResume(t *testing.T) {
	k, in, state := enrollmentFixture()
	in.InstancePolicy = instancePolicyFixture()
	checkpoint := func(context.Context, *EnrollmentState) error { return nil }
	if err := EnrollRuntime(t.Context(), k, in, state, checkpoint); err != nil {
		t.Fatal(err)
	}
	in.InstancePolicy = nil
	if err := EnrollRuntime(t.Context(), k, in, state, checkpoint); err == nil {
		t.Fatal("saved isolation could be omitted on resume")
	}
}

func TestInstanceIsolationPinsPolicyBeforeRuntimeAccess(t *testing.T) {
	k, in, state := enrollmentFixture()
	in.InstancePolicy = instancePolicyFixture()
	interrupted := errors.New("restart after namespace")
	if err := EnrollRuntime(t.Context(), k, in, state, func(context.Context, *EnrollmentState) error {
		if state.NamespaceUID != "" {
			return interrupted
		}
		return nil
	}); !errors.Is(err, interrupted) {
		t.Fatal(err)
	}
	in.InstancePolicy.CPUMillis++
	before := len(k.Actions())
	if err := EnrollRuntime(t.Context(), k, in, state, func(context.Context, *EnrollmentState) error { return nil }); err == nil {
		t.Fatal("namespace accepted a different reviewed policy")
	}
	for _, a := range k.Actions()[before:] {
		if a.GetVerb() != "get" && a.GetVerb() != "list" {
			t.Fatal("changed policy granted runtime access")
		}
	}
}

func TestInstanceLifecycleEffectivePermissions(t *testing.T) {
	for _, denied := range []string{"", "get", "create", "delete", "error"} {
		t.Run("denied-"+denied, func(t *testing.T) {
			k := fake.NewClientset()
			seen := map[string]bool{}
			k.PrependReactor("create", "selfsubjectaccessreviews", func(a kt.Action) (bool, runtime.Object, error) {
				review := a.(kt.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
				attr := review.Spec.ResourceAttributes
				if attr.Namespace != "instance" {
					t.Fatal("permission probe escaped namespace")
				}
				allowed := true
				if attr.Group == "batch" && attr.Resource == "jobs" {
					seen[attr.Verb] = true
					if denied == "error" {
						return true, nil, errors.New("unavailable")
					}
					allowed = attr.Verb != denied
				}
				return true, &authv1.SelfSubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{Allowed: allowed}}, nil
			})
			c := &Client{Kube: k, Config: &Config{Namespace: "instance"}}
			err := c.CheckRuntimeAccess(t.Context(), provider.RuntimeAccessProfile{SchemaVersion: 1, CleanupDependents: true, LifecycleJobs: true})
			if denied == "" {
				if err != nil || len(seen) != 3 || !seen["get"] || !seen["create"] || !seen["delete"] {
					t.Fatal("job permissions not verified", seen, err)
				}
			} else if !errors.Is(err, ErrCleanupPermissionsUnavailable) {
				t.Fatal("unverified job permission accepted", err)
			}
		})
	}
}

func TestInstanceLifecyclePermissionsRemainOptIn(t *testing.T) {
	k := fake.NewClientset()
	k.PrependReactor("create", "selfsubjectaccessreviews", func(a kt.Action) (bool, runtime.Object, error) {
		review := a.(kt.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
		if review.Spec.ResourceAttributes.Group == "batch" {
			t.Fatal("ordinary profile requested lifecycle access")
		}
		return true, &authv1.SelfSubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{Allowed: true}}, nil
	})
	c := &Client{Kube: k, Config: &Config{Namespace: "ordinary"}}
	if err := c.CheckRuntimeAccess(t.Context(), provider.RuntimeAccessProfile{SchemaVersion: 1, CleanupDependents: true}); err != nil {
		t.Fatal(err)
	}
}
