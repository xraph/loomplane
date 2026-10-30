package kubernetes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeclient "k8s.io/client-go/kubernetes"

	"github.com/xraph/loomplane/pkg/provider"
)

var ErrCleanupPermissionsUnavailable = errors.New("cleanup_permissions_unavailable")

const RuntimeAccessOperationAnnotation = "loomplane.io/runtime-access-operation"
const RuntimeAccessDigestAnnotation = "loomplane.io/runtime-access-digest"

// RuntimeAccessReceipt records observed configuration, independently of a response.
type RuntimeAccessReceipt struct {
	Kind            string    `json:"kind"`
	RoleUID         string    `json:"role_uid"`
	ResourceVersion string    `json:"resource_version"`
	RulesDigest     string    `json:"rules_digest"`
	OperationID     string    `json:"operation_id"`
	RequestDigest   string    `json:"request_digest"`
	ObservedAt      time.Time `json:"observed_at"`
}

type RuntimeAccessState struct {
	Attempted      bool                  `json:"attempted"`
	NetworkOutcome string                `json:"network_outcome,omitempty"`
	Receipt        *RuntimeAccessReceipt `json:"receipt,omitempty"`
}

func RuntimeAccessDigest(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func EnrollmentRoleRules(in EnrollmentRequest) ([]rbacv1.PolicyRule, error) {
	rules := append(RuntimeRoleRules(), rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"serviceaccounts"}, ResourceNames: []string{in.ServiceAccountName}, Verbs: []string{"get"}})
	if in.RequireIngress {
		rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"ingresses"}, Verbs: []string{"get", "list", "create", "update", "patch", "delete"}})
	}
	if in.RequireTLS {
		for _, permission := range routePermissions {
			rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{permission.group}, Resources: []string{permission.resource}, Verbs: permission.verbs})
		}
	}
	if in.RuntimeAccess != nil {
		if !in.RuntimeAccess.Valid() {
			return nil, ErrInvalid
		}
		rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{"apps"}, Resources: []string{"replicasets"}, Verbs: []string{"get", "list", "watch", "delete"}}, rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"delete"}})
		if in.RuntimeAccess.HPA {
			rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{"autoscaling"}, Resources: []string{"horizontalpodautoscalers"}, Verbs: []string{"get", "list", "create", "update", "delete"}})
		}
	}
	return rules, nil
}

// ReadRuntimeAccess requires saved object UIDs before reading mutable rules.
func ReadRuntimeAccess(ctx context.Context, k kubeclient.Interface, in provider.RuntimeAccessInput) (*rbacv1.Role, *rbacv1.RoleBinding, error) {
	if in.Namespace == "" || in.NamespaceUID == "" || in.ServiceAccountName == "" || in.ServiceAccountUID == "" || in.RoleName == "" || in.RoleUID == "" || in.RoleBindingName == "" || in.RoleBindingUID == "" {
		return nil, nil, ErrInvalid
	}
	if err := VerifyRuntimeIdentity(ctx, k, in.Namespace, in.NamespaceUID, in.ServiceAccountName, in.ServiceAccountUID); err != nil {
		return nil, nil, err
	}
	role, err := k.RbacV1().Roles(in.Namespace).Get(ctx, in.RoleName, metav1.GetOptions{})
	if err != nil {
		return nil, nil, err
	}
	binding, err := k.RbacV1().RoleBindings(in.Namespace).Get(ctx, in.RoleBindingName, metav1.GetOptions{})
	if err != nil {
		return nil, nil, err
	}
	if string(role.UID) != in.RoleUID || string(binding.UID) != in.RoleBindingUID || role.ResourceVersion == "" || binding.ResourceVersion == "" || role.DeletionTimestamp != nil || binding.DeletionTimestamp != nil || role.Labels[enrollmentLabel] != in.AttachmentID || binding.Labels[enrollmentLabel] != in.AttachmentID || role.Labels[projectLabel] != in.ProjectID || binding.Labels[projectLabel] != in.ProjectID || binding.RoleRef != (rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: in.RoleName}) || !reflect.DeepEqual(binding.Subjects, []rbacv1.Subject{{Kind: "ServiceAccount", Name: in.ServiceAccountName, Namespace: in.Namespace}}) {
		return nil, nil, ErrInvalid
	}
	return role, binding, nil
}

func RuntimeBindingDigest(binding *rbacv1.RoleBinding) string {
	return RuntimeAccessDigest(struct {
		Subjects []rbacv1.Subject
		Ref      rbacv1.RoleRef
	}{binding.Subjects, binding.RoleRef})
}

// ReconcileRuntimeAccess issues one conditional update. An unknown response can
// only recover by observing that exact marked update with its consumed version.
func ReconcileRuntimeAccess(ctx context.Context, k kubeclient.Interface, in provider.RuntimeAccessInput, rules []rbacv1.PolicyRule, operation, digest string, state *RuntimeAccessState, checkpoint func(context.Context) error) error {
	if state == nil || checkpoint == nil || !in.Profile.Valid() || operation == "" || digest == "" || in.RoleResourceVersion == "" || in.RoleRulesDigest == "" {
		return ErrInvalid
	}
	role, binding, err := ReadRuntimeAccess(ctx, k, in)
	if err != nil {
		return err
	}
	if binding.ResourceVersion != in.RoleBindingResourceVersion || RuntimeBindingDigest(binding) != in.RoleBindingSubjectsRefDigest {
		return ErrInvalid
	}
	if !state.Attempted {
		if role.ResourceVersion != in.RoleResourceVersion || RuntimeAccessDigest(role.Rules) != in.RoleRulesDigest {
			return ErrInvalid
		}
		role.Rules = rules
		if role.Annotations == nil {
			role.Annotations = map[string]string{}
		}
		role.Annotations[RuntimeAccessOperationAnnotation] = operation
		role.Annotations[RuntimeAccessDigestAnnotation] = digest
		state.Attempted = true
		state.NetworkOutcome = "unknown"
		if err = checkpoint(ctx); err != nil {
			return err
		}
		role, err = k.RbacV1().Roles(in.Namespace).Update(ctx, role, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
		if !runtimeAccessEffect(role, in, rules, operation, digest) {
			return ErrInvalid
		}
		state.NetworkOutcome = "accepted"
		if err = checkpoint(ctx); err != nil {
			return err
		}
	}
	// Re-read identities and rules after a response, including resumed operations.
	role, binding, err = ReadRuntimeAccess(ctx, k, in)
	if err != nil {
		return err
	}
	if !runtimeAccessEffect(role, in, rules, operation, digest) || binding.ResourceVersion != in.RoleBindingResourceVersion || RuntimeBindingDigest(binding) != in.RoleBindingSubjectsRefDigest {
		return ErrInvalid
	}
	if err = checkpoint(ctx); err != nil {
		return err
	}
	kind := "observed-role-update"
	if state.NetworkOutcome == "accepted" {
		kind = "accepted-role-update"
	}
	state.Receipt = &RuntimeAccessReceipt{Kind: kind, RoleUID: in.RoleUID, ResourceVersion: role.ResourceVersion, RulesDigest: RuntimeAccessDigest(rules), OperationID: operation, RequestDigest: digest, ObservedAt: time.Now().UTC()}
	return checkpoint(ctx)
}

func runtimeAccessEffect(role *rbacv1.Role, in provider.RuntimeAccessInput, rules []rbacv1.PolicyRule, operation, digest string) bool {
	return role != nil && string(role.UID) == in.RoleUID && role.ResourceVersion != "" && role.ResourceVersion != in.RoleResourceVersion && role.DeletionTimestamp == nil && role.Annotations[RuntimeAccessOperationAnnotation] == operation && role.Annotations[RuntimeAccessDigestAnnotation] == digest && reflect.DeepEqual(role.Rules, rules)
}

// CheckRuntimeAccess verifies currently effective permissions before cleanup.
// A stored profile or a historical role receipt cannot replace these probes.
func (c *Client) CheckRuntimeAccess(ctx context.Context, p provider.RuntimeAccessProfile) error {
	if !p.Valid() {
		return ErrCleanupPermissionsUnavailable
	}
	for _, rule := range []struct {
		group, resource string
		verbs           []string
	}{
		{"apps", "deployments", []string{"get", "list", "delete"}},
		{"apps", "replicasets", []string{"get", "list", "watch", "delete"}},
		{"", "pods", []string{"get", "list", "watch", "delete"}},
	} {
		for _, verb := range rule.verbs {
			allowed, err := c.allowedIn(ctx, c.Config.Namespace, rule.group, rule.resource, verb)
			if err != nil || !allowed {
				return errors.Join(ErrCleanupPermissionsUnavailable, err)
			}
		}
	}
	if p.HPA {
		for _, verb := range []string{"get", "list", "create", "update", "delete"} {
			allowed, err := c.allowedIn(ctx, c.Config.Namespace, "autoscaling", "horizontalpodautoscalers", verb)
			if err != nil || !allowed {
				return errors.Join(ErrCleanupPermissionsUnavailable, err)
			}
		}
	}
	return nil
}
