package deployments

import (
	"strings"
	"testing"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestFederationWorkloadDeploymentUsesCurrentServiceBinding(t *testing.T) {
	b := planning.Binding{ID: "binding", OrgID: "org", ProjectID: "project", EnvironmentID: "env", Subject: planning.ServiceSubject, Name: "api", Provider: "kubernetes", Engine: provider.CtrlPlane, ExternalID: id.New(id.PrefixInstance).String(), ConnectionID: "connection", Revision: "registry.example/api@sha256:" + strings.Repeat("a", 64)}
	got, err := FederationWorkloadDeployment(&b)
	if err != nil {
		t.Fatal(err)
	}
	if got.BindingID != b.ID || got.ServiceID != b.Name || got.Container != mainService || got.InstanceID != b.ExternalID || got.Image != b.Revision || got.ConnectionID != b.ConnectionID || got.OrgID != b.OrgID || got.ProjectID != b.ProjectID || got.EnvironmentID != b.EnvironmentID {
		t.Fatalf("incorrect binding projection: %+v", got)
	}
	for name, change := range map[string]func(*planning.Binding){
		"resource":         func(b *planning.Binding) { b.Subject = planning.ResourceSubject },
		"other provider":   func(b *planning.Binding) { b.Provider = "miget" },
		"other engine":     func(b *planning.Binding) { b.Engine = provider.Loomplane },
		"missing instance": func(b *planning.Binding) { b.ExternalID = "" },
		"wrong id kind":    func(b *planning.Binding) { b.ExternalID = id.New(id.PrefixRelease).String() },
		"drifted":          func(b *planning.Binding) { b.Drift = planning.DriftChanged },
		"missing scope":    func(b *planning.Binding) { b.OrgID = "" },
		"mutable image":    func(b *planning.Binding) { b.Revision = "api:latest" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := b
			change(&candidate)
			if _, err := FederationWorkloadDeployment(&candidate); err == nil {
				t.Fatal("invalid deployed binding accepted")
			}
		})
	}
	if _, err := FederationWorkloadDeployment(nil); err == nil {
		t.Fatal("nil binding accepted")
	}
}
