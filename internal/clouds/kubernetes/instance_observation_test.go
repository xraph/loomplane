package kubernetes

import (
	"context"
	"encoding/json"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func observationEnrollment(t *testing.T, revision string) EnrollmentRequest {
	t.Helper()
	_, in, _ := enrollmentFixture()
	in.InstancePolicy = instancePolicyFixture()
	raw, err := json.Marshal(in.InstancePolicy)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["observation_revision"] = revision
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, in.InstancePolicy); err != nil {
		t.Fatal(err)
	}
	return in
}

func allowsObservation(rules []rbacv1.PolicyRule, group, resource, verb string) bool {
	for _, rule := range rules {
		for _, g := range rule.APIGroups {
			for _, r := range rule.Resources {
				for _, v := range rule.Verbs {
					if (g == group || g == "*") && (r == resource || r == "*") && (v == verb || v == "*") {
						return true
					}
				}
			}
		}
	}
	return false
}

func TestInstanceObservationReviewedEnrollment(t *testing.T) {
	k, _, state := enrollmentFixture()
	in := observationEnrollment(t, "kubernetes-instance-observation-v1")
	if err := EnrollRuntime(t.Context(), k, in, state, func(context.Context, *EnrollmentState) error { return nil }); err != nil {
		t.Fatal(err)
	}
	role, err := k.RbacV1().Roles(in.Namespace).Get(t.Context(), in.ServiceAccountName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct{ group, resource string }{{"metrics.k8s.io", "pods"}, {"apps", "replicasets"}} {
		if !allowsObservation(role.Rules, target.group, target.resource, "get") {
			t.Errorf("reviewed observation missing %s/%s GET", target.group, target.resource)
		}
		for _, verb := range []string{"list", "watch", "create", "update", "patch", "delete"} {
			if allowsObservation(role.Rules, target.group, target.resource, verb) {
				t.Errorf("observation grants %s/%s %s", target.group, target.resource, verb)
			}
		}
	}
	if allowsObservation(role.Rules, "", "serviceaccounts/token", "create") {
		t.Fatal("observer can renew itself")
	}
}

func TestInstanceObservationLegacyEnrollmentUnchanged(t *testing.T) {
	_, in, _ := enrollmentFixture()
	for _, instance := range []bool{false, true} {
		if instance {
			in.InstancePolicy = instancePolicyFixture()
		}
		rules, err := EnrollmentRoleRules(in)
		if err != nil {
			t.Fatal(err)
		}
		if allowsObservation(rules, "metrics.k8s.io", "pods", "get") || allowsObservation(rules, "apps", "replicasets", "get") {
			t.Fatal("unreviewed observation granted")
		}
	}
}

func TestInstanceObservationUnknownRevisionNoEffects(t *testing.T) {
	k, _, state := enrollmentFixture()
	in := observationEnrollment(t, "unreviewed-v2")
	if err := EnrollRuntime(t.Context(), k, in, state, func(context.Context, *EnrollmentState) error { return nil }); err == nil {
		t.Fatal("unreviewed observation revision enrolled")
	}
	if len(k.Actions()) != 0 {
		t.Fatal("invalid policy reached cluster")
	}
}

func TestInstanceObservationCannotUpgradeOnResume(t *testing.T) {
	k, _, state := enrollmentFixture()
	in := observationEnrollment(t, "")
	check := func(context.Context, *EnrollmentState) error { return nil }
	if err := EnrollRuntime(t.Context(), k, in, state, check); err != nil {
		t.Fatal(err)
	}
	k.ClearActions()
	in = observationEnrollment(t, "kubernetes-instance-observation-v1")
	if err := EnrollRuntime(t.Context(), k, in, state, check); err == nil {
		t.Fatal("changed policy silently resumed")
	}
	for _, action := range k.Actions() {
		if action.GetVerb() != "get" {
			t.Fatalf("policy change caused %s", action.GetVerb())
		}
	}
}
