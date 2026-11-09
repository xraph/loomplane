package planning

import (
	"testing"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/providerresources"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
	"github.com/xraph/loomplane/pkg/manifest"
)

func TestExistingCompositionChecksUseExactTargetAndRequirement(t *testing.T) {
	kind := recipestest.Sample()
	endpoints := &fakeEndpoints{}
	dialer := &fakeDialer{}
	s := NewService(Deps{Kinds: recipestest.Registry(t), Endpoints: endpoints, Dialer: dialer})
	m := &manifest.Manifest{Resources: map[string]manifest.Resource{"app": {Kind: kind.Name, Uses: map[string]manifest.Use{"postgres": {Resource: "db"}}}, "db": {Kind: "postgres", ExistingResource: syncedResource}}}
	a := Action{Name: "app", Resource: &ResourceConfig{Kind: kind.Name, Target: targetA, Composition: &CompositionConfig{}}}
	verified := map[string]ExistingResourceCheck{syncedResource: {Resource: &providerresources.ProviderResource{ID: syncedResource, EndpointID: prodPG}, Endpoint: &catalogconnect.Endpoint{ID: prodPG, Engine: "postgres"}}}
	checks := map[string]EndpointCheck{}
	s.checkExistingCompositionEndpoints(t.Context(), "org", a, m, verified, checks)
	if len(checks) != 1 || checks[prodPG].Found {
		t.Fatalf("expected only exact target/requirement key: %+v", checks)
	}
	for _, check := range checks {
		if check.Report == nil || check.Report.Version != "16.4" || check.Err != "" {
			t.Fatalf("composed requirement was not probed: %+v", check)
		}
	}
	if len(dialer.asked) != 1 || dialer.asked[0] != targetA || dialer.closed != 1 || len(endpoints.consumers) != 1 || endpoints.consumers[0] != kind.Title {
		t.Fatalf("wrong verifier/consumer: %+v %+v", dialer, endpoints)
	}
}
