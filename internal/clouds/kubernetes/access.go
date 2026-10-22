package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/xraph/ctrlplane/id"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/recipes"
)

// A process's Role only discovers Services. The catalog refuses anything
// else when a kind registers; the runtime refuses it again.
var (
	accessGroups    = []string{"", "discovery.k8s.io"}
	accessResources = []string{"services", "endpoints", "endpointslices"}
	accessVerbs     = []string{"get", "list", "watch"}
)

// checkAccessPermissions reports "rbac" ready when the connection can give
// a catalog process its own ServiceAccount, Role and RoleBinding in its
// namespace and holds the discovery access those Roles grant, which
// Kubernetes requires of whoever grants it. Without them the detail is
// absent; ordinary workloads don't need them.
func (c *Client) checkAccessPermissions(ctx context.Context, out *Result) error {
	delete(out.Details, "rbac")
	for _, requirement := range []struct {
		group, resource string
		verbs           []string
	}{
		{"", "serviceaccounts", []string{"create", "get", "update", "delete"}},
		{"rbac.authorization.k8s.io", "roles", []string{"create", "get", "update", "delete"}},
		{"rbac.authorization.k8s.io", "rolebindings", []string{"create", "get", "update", "delete"}},
		{"", "services", accessVerbs},
		{"", "endpoints", accessVerbs},
		{"discovery.k8s.io", "endpointslices", accessVerbs},
	} {
		for _, verb := range requirement.verbs {
			allowed, err := c.allowed(ctx, requirement.group, requirement.resource, verb)
			if err != nil {
				return err
			}
			if !allowed {
				return nil
			}
		}
	}
	out.Details["rbac"] = "ready"
	return nil
}

// accessRules reads a workload's LabelKubernetesRules as Role rules. A
// workload without the label has none.
func accessRules(labels map[string]string) ([]rbacv1.PolicyRule, error) {
	raw := labels[owned.LabelKubernetesRules]
	if raw == "" {
		return nil, nil
	}
	var rules []recipes.KubernetesRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil || len(rules) == 0 {
		return nil, errors.New("invalid Kubernetes rules")
	}
	out := make([]rbacv1.PolicyRule, 0, len(rules))
	for _, rule := range rules {
		if len(rule.APIGroups) == 0 || len(rule.Resources) == 0 || len(rule.Verbs) == 0 || !within(rule.APIGroups, accessGroups) || !within(rule.Resources, accessResources) || !within(rule.Verbs, accessVerbs) {
			return nil, errors.New("a Kubernetes rule may only get, list and watch services, endpoints and endpointslices")
		}
		out = append(out, rbacv1.PolicyRule{APIGroups: slices.Clone(rule.APIGroups), Resources: slices.Clone(rule.Resources), Verbs: slices.Clone(rule.Verbs)})
	}
	return out, nil
}

func within(values, allowed []string) bool {
	for _, value := range values {
		if !slices.Contains(allowed, value) {
			return false
		}
	}
	return true
}

// applyAccess gives a workload whose labels carry LabelKubernetesRules its
// own ServiceAccount, Role and RoleBinding, all named after the workload,
// and returns the account's name. Without rules it returns "" and changes
// nothing: what an earlier revision left goes in dropAccess, once the
// workload no longer names the account.
func (r *Runtime) applyAccess(ctx context.Context, c *Client, iid id.ID, labels map[string]string) (serviceAccount string, err error) {
	rules, err := accessRules(labels)
	if err != nil {
		return "", err
	}
	if rules == nil {
		return "", nil
	}
	ns, name := c.Config.Namespace, WorkloadName(iid)
	identity := objectLabels(iid, labels)
	if labels[owned.LabelStateful] == "true" {
		// checkOwner also matches a stateful workload's connection.
		identity[owned.LabelConnection] = labels[owned.LabelConnection]
	}
	meta := metav1.ObjectMeta{Name: name, Namespace: ns, Labels: identity}
	accounts := c.Kube.CoreV1().ServiceAccounts(ns)
	account := &corev1.ServiceAccount{ObjectMeta: *meta.DeepCopy()}
	existingAccount, err := accounts.Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		_, err = accounts.Create(ctx, account, metav1.CreateOptions{})
	case err == nil:
		if checkErr := checkOwner(existingAccount, iid, labels); checkErr != nil {
			return "", checkErr
		}
		account.ResourceVersion = existingAccount.ResourceVersion
		account.Secrets, account.ImagePullSecrets = existingAccount.Secrets, existingAccount.ImagePullSecrets
		_, err = accounts.Update(ctx, account, metav1.UpdateOptions{})
	}
	if err != nil {
		return "", err
	}
	roles := c.Kube.RbacV1().Roles(ns)
	role := &rbacv1.Role{ObjectMeta: *meta.DeepCopy(), Rules: rules}
	existingRole, err := roles.Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		_, err = roles.Create(ctx, role, metav1.CreateOptions{})
	case err == nil:
		if checkErr := checkOwner(existingRole, iid, labels); checkErr != nil {
			return "", checkErr
		}
		role.ResourceVersion = existingRole.ResourceVersion
		_, err = roles.Update(ctx, role, metav1.UpdateOptions{})
	}
	if err != nil {
		return "", err
	}
	bindings := c.Kube.RbacV1().RoleBindings(ns)
	binding := &rbacv1.RoleBinding{ObjectMeta: *meta.DeepCopy(), RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name}, Subjects: []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: name, Namespace: ns}}}
	existingBinding, err := bindings.Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		_, err = bindings.Create(ctx, binding, metav1.CreateOptions{})
	case err == nil:
		if checkErr := checkOwner(existingBinding, iid, labels); checkErr != nil {
			return "", checkErr
		}
		binding.ResourceVersion = existingBinding.ResourceVersion
		_, err = bindings.Update(ctx, binding, metav1.UpdateOptions{})
	}
	if err != nil {
		return "", err
	}
	return name, nil
}

// dropAccess deletes the access objects of a workload whose revision has
// no rules. Apply calls it after the workload is written and ready, so a
// rollback before then restores a template whose account still exists.
func (r *Runtime) dropAccess(ctx context.Context, c *Client, iid id.ID, labels map[string]string) error {
	if labels[owned.LabelKubernetesRules] != "" {
		return nil
	}
	return r.removeAccess(ctx, c, iid, labels)
}

// removeAccess deletes a catalog component's RoleBinding, Role and
// ServiceAccount. It leaves objects another instance owns, and an object
// the connection can't read, which it can't have created either. Other
// workloads never have them, so it doesn't look.
func (r *Runtime) removeAccess(ctx context.Context, c *Client, iid id.ID, labels map[string]string) error {
	if labels[owned.LabelCatalogComponent] == "" {
		return nil
	}
	ns, name := c.Config.Namespace, WorkloadName(iid)
	remove := func(object metav1.Object, err error, deleteFn func(context.Context, string, metav1.DeleteOptions) error) error {
		if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if checkOwner(object, iid, labels) != nil {
			return nil
		}
		if err = deleteFn(ctx, name, deleteOwnedOptions(object)); apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	binding, err := c.Kube.RbacV1().RoleBindings(ns).Get(ctx, name, metav1.GetOptions{})
	if checkErr := remove(binding, err, c.Kube.RbacV1().RoleBindings(ns).Delete); checkErr != nil {
		return checkErr
	}
	role, err := c.Kube.RbacV1().Roles(ns).Get(ctx, name, metav1.GetOptions{})
	if checkErr := remove(role, err, c.Kube.RbacV1().Roles(ns).Delete); checkErr != nil {
		return checkErr
	}
	account, err := c.Kube.CoreV1().ServiceAccounts(ns).Get(ctx, name, metav1.GetOptions{})
	return remove(account, err, c.Kube.CoreV1().ServiceAccounts(ns).Delete)
}
