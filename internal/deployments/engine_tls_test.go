package deployments

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/enginetls"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
)

func TestDeferredEngineCertificateUsesTrustedRuntimeAndNeverEmitsIssuerKey(t *testing.T) {
	kind, _ := recipes.Default().Kind("opensearch")
	composition, err := recipes.Default().Resolve("opensearch", "3.9", "arm64", "kubernetes")
	if err != nil {
		t.Fatal(err)
	}
	identity := enginetls.Identity{Org: "org", Environment: "env", Resource: "search", Kind: "opensearch"}
	raw, err := enginetls.Generate(identity, "password", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := enginetls.Parse(raw, identity, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	run := ComponentRun{OrgID: "org", EnvironmentID: "env", Resource: "search", Kind: "opensearch", Provider: "kubernetes", Component: composition.Components[0], TargetSettings: map[string]string{"namespace": "apps"}, Secrets: map[string][]byte{"tls": raw, "password": []byte("password")}}
	target := &OwnedTarget{Cloud: "kubernetes", Secrets: &memSecrets{m: map[string][]byte{}}}
	files, err := target.renderFiles(t.Context(), kind, run)
	if err != nil {
		t.Fatal(err)
	}
	ref := "org/env/search/runtime_test"
	if err = target.Secrets.Put(t.Context(), ref, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if err = target.keepRuntimeFiles(t.Context(), ref, files); err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{owned.LabelOrg: "org", owned.LabelEnvironment: "env", owned.LabelService: "search", owned.LabelCatalogResource: "search", owned.LabelCatalogKind: "opensearch", owned.LabelCatalogComponent: "opensearch", owned.LabelHealthChecker: "opensearch-v1", owned.LabelRuntime: ref, owned.LabelRuntimeFiles: "true"}
	const path = "/usr/share/opensearch/config/node.pem"
	preflight, err := target.values(owned.RuntimePreflight(t.Context()), labels)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := preflight.Files[path]; exists {
		t.Fatal("preflight emitted a fake certificate")
	}
	for _, data := range preflight.Files {
		if bytes.Contains(data, bundle.CAKey) {
			t.Fatal("preflight emitted issuer key")
		}
	}
	forged := maps.Clone(labels)
	forged[owned.LabelInstance] = id.New(id.PrefixInstance).String()
	if _, err = target.values(t.Context(), forged); err == nil {
		t.Fatal("labels supplied trusted identity")
	}
	iid := id.New(id.PrefixInstance)
	ctx := owned.WithRuntimeIdentity(t.Context(), owned.RuntimeIdentity{Instance: iid, Provider: "kubernetes", Namespace: "apps"})
	trusted := owned.InstanceLabels(forged, iid)
	values, err := target.values(ctx, trusted)
	if err != nil {
		t.Fatal(err)
	}
	again, err := target.values(ctx, trusted)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(values.Files[path], again.Files[path]) {
		t.Fatal("replay changed certificate")
	}
	for _, data := range values.Files {
		if bytes.Contains(data, bundle.CAKey) {
			t.Fatal("runtime emitted issuer key")
		}
	}
	block, _ := pem.Decode(values.Files[path])
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	host := "lp-" + strings.ReplaceAll(iid.String(), "_", "-") + ".apps.svc"
	if err = cert.VerifyHostname(host); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(map[string]string){func(m map[string]string) { m[owned.LabelCatalogResource] = "other" }, func(m map[string]string) { m[owned.LabelRuntime] = "org/foreign/search/runtime_test" }, func(m map[string]string) { m[owned.LabelInstance] = id.New(id.PrefixInstance).String() }} {
		bad := maps.Clone(trusted)
		change(bad)
		if _, err = target.values(ctx, bad); err == nil {
			t.Fatal("accepted forged runtime metadata")
		}
	}
	wrongNamespace := owned.WithRuntimeIdentity(t.Context(), owned.RuntimeIdentity{Instance: iid, Provider: "kubernetes", Namespace: "other"})
	if _, err = target.values(wrongNamespace, trusted); err == nil {
		t.Fatal("accepted changed namespace")
	}
	for i := range files {
		if files[i].Certificate != nil {
			files[i].Path = "/tmp/unreviewed.pem"
		}
	}
	if err = target.keepRuntimeFiles(t.Context(), ref, files); err != nil {
		t.Fatal(err)
	}
	if _, err = target.values(ctx, trusted); err == nil {
		t.Fatal("emitted unreviewed certificate path")
	}
}

func TestEngineStructuredSecretIsStoredOnceValidatedAndPublicOutputUsesRunBundle(t *testing.T) {
	f := newComponentFixture(t, recipes.Default())
	composition, err := recipes.Default().Resolve("opensearch", "3.9", "amd64", "server")
	if err != nil {
		t.Fatal(err)
	}
	job := f.graphJobForEngine(t, "opensearch", "3.9", composition)
	kind, _ := recipes.Default().Kind("opensearch")
	run, _, err := f.target.componentEnv(t.Context(), job, kind)
	if err != nil {
		t.Fatal(err)
	}
	again, _, err := f.target.componentEnv(t.Context(), job, kind)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(run.Secrets["tls"], again.Secrets["tls"]) {
		t.Fatal("replan rotated bundle or password hash")
	}
	reads := f.target.reads(job, kind, "search", nil)
	reads.secrets = run.Secrets
	ca, err := publicEngineCA(reads, "tls")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ca, "PRIVATE KEY") {
		t.Fatal("published private material")
	}
	key := ResourceKey("org", "env", "search", "secret:tls")
	f.secrets.m[key] = []byte("corrupt")
	if _, _, err = f.target.componentEnv(t.Context(), job, kind); err == nil {
		t.Fatal("repaired corrupted material")
	}
	got, err := publicEngineCA(reads, "tls")
	if err != nil || got != ca {
		t.Fatal("public output read a different Vault bundle")
	}
	delete(f.secrets.m, key)
	job.Binding = bound(job, &Outcome{ExternalID: id.New(id.PrefixInstance).String()})
	if _, _, err = f.target.componentEnv(t.Context(), job, kind); err == nil {
		t.Fatal("regenerated missing existing TLS bundle")
	}
}

func (f *componentFixture) graphJobForEngine(t *testing.T, kind, version string, composition *recipes.Composition) *Job {
	t.Helper()
	c := composition.Components[0]
	return f.job("search", &planning.ResourceConfig{Provider: "server", Target: "conn", TargetSettings: map[string]string{"architecture": "amd64"}, Kind: kind, Version: version, Component: &planning.ComponentConfig{Kind: kind, Resource: "search", Revision: composition.Revision, Component: c, Protection: planning.ComponentProtection(c.Component)}})
}

func TestEngineRuntimePreparationRejectsIssuerKeyAsNodeKey(t *testing.T) {
	search, _ := recipes.Default().Kind("opensearch")
	for _, kind := range []recipes.Kind{search, recipes.DuckDBKind(recipes.Image{})} {
		t.Run(kind.Name, func(t *testing.T) {
			identity := enginetls.Identity{Org: "org", Environment: "env", Resource: "search", Kind: kind.Name}
			password := ""
			if kind.Name == "opensearch" {
				password = "password"
			}
			raw, err := enginetls.Generate(identity, password, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			bundle, err := enginetls.Parse(raw, identity, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			run := ComponentRun{OrgID: "org", EnvironmentID: "env", Resource: "search", Kind: kind.Name, Provider: "server", Component: recipes.ResolvedComponent{Component: kind.Components[0]}, Secrets: map[string][]byte{"tls": raw, "password": []byte(password), "token": []byte(strings.Repeat("a", 64))}}
			target := &OwnedTarget{Cloud: "server", Secrets: &memSecrets{m: map[string][]byte{}}}
			files, err := target.renderFiles(t.Context(), kind, run)
			if err != nil {
				t.Fatal(err)
			}
			bundle.NodeKey = append([]byte(nil), bundle.CAKey...)
			corrupt, err := json.Marshal(bundle)
			if err != nil {
				t.Fatal(err)
			}
			run.Secrets["tls"] = corrupt
			if emitted, renderErr := target.renderFiles(t.Context(), kind, run); renderErr == nil || len(emitted) != 0 {
				t.Error("invalid bundle reached rendered runtime files")
			}
			for i := range files {
				if files[i].Certificate != nil {
					files[i].Certificate.Bundle = corrupt
				}
			}
			ref := "org/env/search/runtime_key_separation"
			if err = target.Secrets.Put(t.Context(), ref, []byte("{}")); err != nil {
				t.Fatal(err)
			}
			if err = target.keepRuntimeFiles(t.Context(), ref, files); err != nil {
				t.Fatal(err)
			}
			iid := id.New(id.PrefixInstance)
			labels := owned.InstanceLabels(map[string]string{owned.LabelOrg: "org", owned.LabelEnvironment: "env", owned.LabelService: "search", owned.LabelCatalogResource: "search", owned.LabelCatalogKind: kind.Name, owned.LabelCatalogComponent: kind.Name, owned.LabelHealthChecker: kind.Name + "-v1", owned.LabelRuntime: ref, owned.LabelRuntimeFiles: "true"}, iid)
			ctx := owned.WithRuntimeIdentity(t.Context(), owned.RuntimeIdentity{Instance: iid, Provider: "server"})
			values, valueErr := target.values(ctx, labels)
			if valueErr == nil {
				t.Error("corrupt recorded bundle passed trusted runtime preparation")
			}
			for _, data := range values.Files {
				if bytes.Contains(data, bundle.CAKey) {
					t.Fatal("runtime preparation returned the CA signing key")
				}
			}
		})
	}
}
