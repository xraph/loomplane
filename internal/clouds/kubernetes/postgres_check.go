package kubernetes

import (
	"context"
	"strings"
)

// Database recovery permissions are optional for ordinary workloads. Actual CNI
// enforcement is verified by a live probe before each backup object is read.
func (c *Client) checkPostgresPermissions(ctx context.Context, out *Result) error {
	out.Details["postgres_recovery_permissions"] = "unavailable"
	delete(out.Details, "postgres_missing_permissions")
	if out.Details["persistent_storage"] != "ready" || out.Details["architecture"] == "" {
		return nil
	}
	missing := []string{}
	for _, requirement := range []struct {
		group, resource string
		verbs           []string
	}{
		{"", "pods/exec", []string{"create"}},
		{"", "pods", []string{"create", "delete"}},
		{"networking.k8s.io", "networkpolicies", []string{"get", "list", "create", "update", "delete"}},
		{"", "persistentvolumeclaims", []string{"delete"}},
		{"", "persistentvolumes", []string{"update"}},
	} {
		namespace := c.Config.Namespace
		if requirement.resource == "persistentvolumes" {
			namespace = ""
		}
		for _, verb := range requirement.verbs {
			allowed, err := c.allowedIn(ctx, namespace, requirement.group, requirement.resource, verb)
			if err != nil {
				return err
			}
			if !allowed {
				missing = append(missing, requirement.resource+":"+verb)
			}
		}
	}
	if len(missing) == 0 {
		out.Details["postgres_recovery_permissions"] = "ready"
	} else {
		out.Details["postgres_missing_permissions"] = strings.Join(missing, ",")
	}
	return nil
}
