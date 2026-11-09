package deployments

import (
	"bytes"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/enginetls"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recovery"
)

func TestRecoveryComponentGeneratesIndependentScopedTrust(t *testing.T) {
	registry := recipes.Default()
	fixture := newComponentFixture(t, registry)
	composition, err := registry.Resolve("opensearch", "", "arm64", "server")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &planning.ResourceConfig{Kind: "opensearch", Version: composition.Version, Provider: "server", Target: "conn", TargetSettings: map[string]string{"architecture": "arm64"}, Component: &planning.ComponentConfig{Kind: "opensearch", Resource: "search", Revision: composition.Revision, Component: composition.Components[0], Protection: recipes.ProtectionNative}}
	binding := &planning.Binding{OrgID: "org", ProjectID: "project", EnvironmentID: "env", Name: "search"}
	descriptor, err := recovery.Describe(cfg, registry)
	if err != nil {
		t.Fatal(err)
	}
	source, sourceReader, err := fixture.target.recoveryRun(t.Context(), binding, cfg, descriptor, "search")
	if err != nil {
		t.Fatal(err)
	}
	clone, cloneReader, err := fixture.target.recoveryRun(t.Context(), binding, cfg, descriptor, "restore-copy")
	if err != nil {
		t.Fatal(err)
	}
	if sourceReader == cloneReader || bytes.Equal(source.Secrets["password"], clone.Secrets["password"]) || bytes.Equal(source.Secrets["tls"], clone.Secrets["tls"]) {
		t.Fatal("recovery reused source authentication")
	}
	if len(fixture.secrets.m) != 0 {
		t.Fatal("rendering recovery material changed the source secret store")
	}
	if _, err = enginetls.Parse(clone.Secrets["tls"], runTLSIdentity(source), time.Now()); err == nil {
		t.Fatal("clone trust accepted under source identity")
	}
	rendered, err := fixture.target.renderFiles(t.Context(), descriptor.Kind, clone)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range rendered {
		if bytes.Contains(file.Data, source.Secrets["password"]) {
			t.Fatal("source password leaked into clone")
		}
		if file.Certificate != nil && len(file.Data) > 0 {
			t.Fatal("certificate bypassed trusted runtime identity")
		}
	}
}
