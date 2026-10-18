//go:build integration

package kubernetes_test

import (
	"context"
	"maps"
	"strings"
	"testing"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/testfixtures/cluster"
)

func TestAcceptanceKubernetesPostgresPermissionChecks(t *testing.T) {
	_, _, admin, labels, _ := cluster.New(t)
	ctx := t.Context()
	ns := admin.Config.Namespace
	roleName := "database-recovery"
	sa, err := admin.Kube.CoreV1().ServiceAccounts(ns).Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: roleName}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rules := []rbacv1.PolicyRule{
		{APIGroups: []string{"apps"}, Resources: []string{"deployments", "statefulsets"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		{APIGroups: []string{""}, Resources: []string{"services", "secrets"}, Verbs: []string{"get", "create", "update", "patch", "delete"}},
		{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list", "watch", "create", "delete"}},
		{APIGroups: []string{""}, Resources: []string{"persistentvolumeclaims"}, Verbs: []string{"get", "list", "create", "delete"}},
		{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"networkpolicies"}, Verbs: []string{"get", "list", "create", "update", "delete"}},
	}
	role, err := admin.Kube.RbacV1().Roles(ns).Create(ctx, &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: roleName}, Rules: rules}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	subject := rbacv1.Subject{Kind: "ServiceAccount", Name: sa.Name, Namespace: ns}
	if _, err = admin.Kube.RbacV1().RoleBindings(ns).Create(ctx, &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: roleName}, Subjects: []rbacv1.Subject{subject}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	clusterRoleName := ns + "-postgres-permissions"
	cr, err := admin.Kube.RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: clusterRoleName}, Rules: []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"namespaces"}, ResourceNames: []string{ns}, Verbs: []string{"get"}},
		{APIGroups: []string{"storage.k8s.io"}, Resources: []string{"storageclasses"}, ResourceNames: []string{admin.Config.Details["storage_class"]}, Verbs: []string{"get"}},
		{APIGroups: []string{""}, Resources: []string{"persistentvolumes"}, Verbs: []string{"get", "update"}},
	}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if deleteErr := admin.Kube.RbacV1().ClusterRoleBindings().Delete(cleanup, clusterRoleName, metav1.DeleteOptions{}); deleteErr != nil {
			t.Error(deleteErr)
		}
		if deleteErr := admin.Kube.RbacV1().ClusterRoles().Delete(cleanup, cr.Name, metav1.DeleteOptions{}); deleteErr != nil {
			t.Error(deleteErr)
		}
	})
	if _, err = admin.Kube.RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: clusterRoleName}, Subjects: []rbacv1.Subject{subject}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: cr.Name}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	lifetime := int64(600)
	token, err := admin.Kube.CoreV1().ServiceAccounts(ns).CreateToken(ctx, sa.Name, &authv1.TokenRequest{Spec: authv1.TokenRequestSpec{ExpirationSeconds: &lifetime}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := *admin.Config
	cfg.REST = rest.CopyConfig(admin.Config.REST)
	cfg.REST.CertData, cfg.REST.KeyData = nil, nil
	cfg.REST.CertFile, cfg.REST.KeyFile = "", ""
	cfg.REST.BearerToken, cfg.REST.BearerTokenFile = token.Status.Token, ""
	cfg.Details = maps.Clone(admin.Config.Details)
	cfg.Details["architecture"] = labels["loomplane.architecture"]
	client, err := kubernetes.New(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	check := func(want string) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			result, checkErr := client.Check(ctx)
			if checkErr != nil {
				t.Fatal(checkErr)
			}
			if len(result.Missing) != 0 {
				t.Fatalf("ordinary workload permissions were lost: %+v", result.Missing)
			}
			if result.Details["postgres_recovery_permissions"] == want {
				if want == "unavailable" && !strings.Contains(result.Details["postgres_missing_permissions"], "pods/exec:create") {
					t.Fatalf("exec denial was not identified: %+v", result.Details)
				}
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("database permissions never became %s: %+v", want, result.Details)
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(time.Second):
			}
		}
	}
	check("unavailable")
	role.Rules = append(role.Rules, rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"pods/exec"}, Verbs: []string{"create"}})
	if _, err = admin.Kube.RbacV1().Roles(ns).Update(ctx, role, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	check("ready")
	t.Log("restricted service-account credential retained ordinary workload access, reported missing pods/exec, then qualified recovery permissions after the explicit grant")
}
