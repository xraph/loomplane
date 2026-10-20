package access

import (
	"reflect"
	"testing"
	"time"

	domain "github.com/xraph/loomplane/pkg/federation"
)

func policyFixture(t *testing.T) (domain.PolicyInput, time.Time) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	target := domain.Scope{OrgID: "org", ProjectID: "target-project", EnvironmentID: "production"}
	source := domain.Scope{OrgID: "org", ProjectID: "source-project", EnvironmentID: "production"}
	export := domain.ServiceExport{Metadata: domain.Metadata{Scope: target, ID: "api", Generation: 1}, ServiceID: "orders", DestinationIDs: []string{"cluster-b", "cluster-a"}, Port: 8443, Protocol: domain.HTTP, IdentityProfile: domain.WorkloadIdentity}
	revision, err := ExportRevision(export)
	if err != nil {
		t.Fatal(err)
	}
	expiry := now.Add(time.Hour)
	grant := domain.AccessGrant{Metadata: domain.Metadata{Scope: target, ID: "checkout-orders", Generation: 1}, Source: domain.WorkloadSelector{Scope: source, DestinationID: "caller-cluster", ServiceID: "checkout"}, ExportID: export.ID, ExportRevision: revision, Port: export.Port, Protocol: export.Protocol, HTTPMethods: []string{"POST", "GET"}, HTTPPaths: []string{"/orders"}, OwnerID: "admin", ExpiresAt: &expiry}
	grant.ApprovedRevision, err = GrantRevision(grant)
	if err != nil {
		t.Fatal(err)
	}
	return domain.PolicyInput{Scope: source, Generation: 1, Exports: []domain.ServiceExport{export}, Grants: []domain.AccessGrant{grant}}, now
}
func TestDefaultDeny(t *testing.T) {
	in, now := policyFixture(t)
	in.Grants = nil
	policy, err := CompileAt(in, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.Grants) != 0 || len(policy.Exports) != 0 || policy.Scope != in.Scope || policy.Generation != 1 || policy.Revision == "" {
		t.Fatalf("invalid deny policy: %+v", policy)
	}
	for _, mutate := range []func(*domain.AccessGrant){
		func(g *domain.AccessGrant) { g.ApprovedRevision = "" },
		func(g *domain.AccessGrant) { g.HTTPMethods = append(g.HTTPMethods, "DELETE") },
		func(g *domain.AccessGrant) { g.Source.ServiceID = "other" },
		func(g *domain.AccessGrant) { g.ExportRevision = "changed" },
		func(g *domain.AccessGrant) { g.Generation++ },
		func(g *domain.AccessGrant) { v := now; g.RevokedAt = &v },
		func(g *domain.AccessGrant) { v := now; g.ExpiresAt = &v },
	} {
		in, now = policyFixture(t)
		mutate(&in.Grants[0])
		policy, err = CompileAt(in, now)
		if err != nil {
			t.Fatal(err)
		}
		if len(policy.Grants) != 0 || len(policy.Exports) != 0 {
			t.Fatal("unapproved, changed or expired grant authorized")
		}
	}
}
func TestPolicyPinsExactExport(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*domain.ServiceExport)
	}{
		{"destination", func(e *domain.ServiceExport) { e.DestinationIDs = []string{"other"} }},
		{"service", func(e *domain.ServiceExport) { e.ServiceID = "other" }},
		{"generation", func(e *domain.ServiceExport) { e.Generation++ }},
		{"identity", func(e *domain.ServiceExport) { e.IdentityProfile = domain.GatewayIdentity }},
		{"port", func(e *domain.ServiceExport) { e.Port++ }},
		{"protocol", func(e *domain.ServiceExport) { e.Protocol = domain.TCP }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, now := policyFixture(t)
			tc.mutate(&in.Exports[0])
			policy, err := CompileAt(in, now)
			if err != nil {
				t.Fatal(err)
			}
			if len(policy.Grants) != 0 {
				t.Fatal("export change retained grant")
			}
		})
	}
	in, now := policyFixture(t)
	in.Exports = nil
	policy, err := CompileAt(in, now)
	if err != nil || len(policy.Grants) != 0 {
		t.Fatalf("missing export: %+v %v", policy, err)
	}
}
func TestPolicyCompilationIsDeterministicAndOwnsInput(t *testing.T) {
	in, now := policyFixture(t)
	first, err := CompileAt(in, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Grants) != 1 || len(first.Exports) != 1 {
		t.Fatal("approved grant omitted")
	}
	in.Exports[0].DestinationIDs = []string{"cluster-a", "cluster-b"}
	in.Grants[0].HTTPMethods = []string{"GET", "POST"}
	second, err := CompileAt(in, now)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("ordering changed compiled policy")
	}
	in.Grants[0].HTTPMethods[0] = "DELETE"
	in.Exports[0].DestinationIDs[0] = "replacement"
	*in.Grants[0].ExpiresAt = now.Add(time.Second)
	if first.Grants[0].HTTPMethods[0] != "GET" || first.Exports[0].DestinationIDs[0] != "cluster-a" || !first.Grants[0].ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatal("input mutated compiled bundle")
	}
	if first.ExpiresAt.Sub(now) != domain.MaxPolicyLease {
		t.Fatal("policy lease not bounded")
	}
}
func TestPolicyRejectsAmbiguousScopeAndDuplicates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*domain.PolicyInput)
	}{
		{"foreign export", func(in *domain.PolicyInput) { in.Exports[0].OrgID = "foreign" }},
		{"foreign grant", func(in *domain.PolicyInput) { in.Grants[0].OrgID = "foreign" }},
		{"foreign source", func(in *domain.PolicyInput) { in.Grants[0].Source.OrgID = "foreign" }},
		{"duplicate export", func(in *domain.PolicyInput) { in.Exports = append(in.Exports, in.Exports[0]) }},
		{"duplicate grant", func(in *domain.PolicyInput) { in.Grants = append(in.Grants, in.Grants[0]) }},
		{"zero generation", func(in *domain.PolicyInput) { in.Generation = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, now := policyFixture(t)
			tc.mutate(&in)
			if _, err := CompileAt(in, now); err == nil {
				t.Fatal("ambiguous policy accepted")
			}
		})
	}
}
func TestApprovalRevisionIncludesEveryPermission(t *testing.T) {
	in, _ := policyFixture(t)
	original := in.Grants[0]
	for _, mutate := range []func(*domain.AccessGrant){
		func(g *domain.AccessGrant) { g.Port++ }, func(g *domain.AccessGrant) { g.OwnerID = "other" },
		func(g *domain.AccessGrant) { g.HTTPPaths = []string{"/admin"} }, func(g *domain.AccessGrant) { g.Source.EnvironmentID = "staging" },
		func(g *domain.AccessGrant) { v := g.ExpiresAt.Add(time.Hour); g.ExpiresAt = &v },
	} {
		g := original
		mutate(&g)
		revision, err := GrantRevision(g)
		if err != nil {
			t.Fatal(err)
		}
		if revision == original.ApprovedRevision {
			t.Fatal("permission changed without invalidating approval")
		}
	}
}

func TestPolicyApprovedMismatchDoesNotAuthorize(t *testing.T) {
	for _, mutate := range []func(*domain.AccessGrant){
		func(g *domain.AccessGrant) { g.Port++ },
		func(g *domain.AccessGrant) { g.ProjectID = "unrelated" },
		func(g *domain.AccessGrant) { g.Protocol = domain.TCP; g.HTTPMethods = nil; g.HTTPPaths = nil },
	} {
		in, now := policyFixture(t)
		mutate(&in.Grants[0])
		var err error
		in.Grants[0].ApprovedRevision, err = GrantRevision(in.Grants[0])
		if err != nil {
			t.Fatal(err)
		}
		policy, err := CompileAt(in, now)
		if err != nil || len(policy.Grants) != 0 {
			t.Fatalf("approved mismatch authorized: %+v %v", policy, err)
		}
	}
}

func TestPolicyScopeVisibilityAndLease(t *testing.T) {
	in, now := policyFixture(t)
	expiry := now.Add(20 * time.Second)
	in.Grants[0].ExpiresAt = &expiry
	var err error
	in.Grants[0].ApprovedRevision, err = GrantRevision(in.Grants[0])
	if err != nil {
		t.Fatal(err)
	}
	source, err := CompileAt(in, now)
	if err != nil || len(source.Grants) != 1 || !source.ExpiresAt.Equal(expiry) {
		t.Fatalf("source policy: %+v %v", source, err)
	}
	renewed, err := CompileAt(in, now.Add(time.Second))
	if err != nil || renewed.Revision != source.Revision {
		t.Fatal("lease renewal changed content revision")
	}
	in.Scope = in.Exports[0].Scope
	target, err := CompileAt(in, now)
	if err != nil || len(target.Grants) != 1 {
		t.Fatal("target policy omitted inbound grant")
	}
	in.ProjectID = "unrelated"
	other, err := CompileAt(in, now)
	if err != nil || len(other.Grants) != 0 || len(other.Exports) != 0 {
		t.Fatal("unrelated scope received policy")
	}
}

func TestPolicyRejectsMalformedRules(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*domain.PolicyInput)
	}{
		{"duplicate destination", func(in *domain.PolicyInput) { in.Exports[0].DestinationIDs = []string{"a", "a"} }},
		{"no destination", func(in *domain.PolicyInput) { in.Exports[0].DestinationIDs = nil }},
		{"invalid identity", func(in *domain.PolicyInput) { in.Exports[0].IdentityProfile = "anonymous" }},
		{"duplicate method", func(in *domain.PolicyInput) { in.Grants[0].HTTPMethods = []string{"GET", "GET"} }},
		{"path alias", func(in *domain.PolicyInput) { in.Grants[0].HTTPPaths = []string{"/a/../admin"} }},
		{"wrong selectors", func(in *domain.PolicyInput) { in.Grants[0].GRPCMethods = []string{"/shop.Orders/Get"} }},
		{"wildcard service", func(in *domain.PolicyInput) { in.Grants[0].Source.ServiceID = "*" }},
		{"zero export generation", func(in *domain.PolicyInput) { in.Exports[0].Generation = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, now := policyFixture(t)
			tc.mutate(&in)
			if _, err := CompileAt(in, now); err == nil {
				t.Fatal("invalid rule accepted")
			}
		})
	}
}
