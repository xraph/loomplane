package recipes

import "testing"

func TestAuthenticatedProbeIsKindBound(t *testing.T) {
	k, _ := Default().Kind("falkordb")
	k.Components[0].Readiness = &HTTPProbe{Port: "redis", Checker: "falkordb-v1"}
	if err := NewRegistry().Register(k); err != nil {
		t.Fatal(err)
	}
	for _, probe := range []*HTTPProbe{{Port: "redis", Checker: "duckdb-v1"}, {Port: "redis", Checker: "falkordb-v1", Path: "/"}, {Port: "absent", Checker: "falkordb-v1"}} {
		k.Components[0].Readiness = probe
		if err := NewRegistry().Register(k); err == nil {
			t.Fatalf("accepted invalid probe %+v", probe)
		}
	}
}

func TestStandaloneEnginesKeepPrivateAuthenticatedContracts(t *testing.T) {
	for _, name := range []string{"falkordb", "elasticsearch", "opensearch", "duckdb"} {
		kind, ok := Default().Kind(name)
		if name == "duckdb" {
			if ok {
				t.Fatal("unpublished DuckDB image registered")
			}
			kind = DuckDBKind(Image{Repository: "qualified.example/duckdb", Digests: map[string]string{"amd64": "sha256:900ea941ebb2ca68977e2e5ea8b65ed0a6080b1fa24999372a5e42d3e7880a97", "arm64": "sha256:4f9179bb904dc22cc6d883adbcc03bc7842b99faf7cbe22a779fbe1a051b3fbd"}})
		} else if !ok {
			t.Fatal(name)
		}
		registry := NewRegistry()
		if err := registry.Register(kind); err != nil {
			t.Fatal(err)
		}
		if kind.Placement != PlacementServiceProject || kind.Components[0].Volume == nil || kind.Components[0].Readiness.Checker != name+"-v1" {
			t.Fatalf("%s lost private persistent/authenticated contract", name)
		}
		for _, field := range []string{FieldURL, FieldHost, FieldHostPort, FieldPort, FieldPassword} {
			if kind.Service.Connection[field] == "" {
				t.Fatalf("%s lacks explicit %s mapping", name, field)
			}
		}
		if name == "opensearch" || name == "duckdb" {
			if kind.Service.Connection[FieldCACert] != "ca-cert" {
				t.Fatalf("%s lacks reusable public CA", name)
			}
		}
	}
}

func TestStructuredTrustCannotBecomeArbitraryRuntimeFilesOrPrivateOutputs(t *testing.T) {
	original, _ := Default().Kind("opensearch")
	for _, change := range []func(*Kind){
		func(k *Kind) { k.Secrets[1].Generator = "unknown" },
		func(k *Kind) { k.Components[0].Files[4].Path = "/etc/arbitrary.pem" },
		func(k *Kind) { k.Outputs[len(k.Outputs)-1].PublicCA = "password" },
	} {
		kind := original.clone()
		change(&kind)
		if err := NewRegistry().Register(kind); err == nil {
			t.Fatal("accepted invalid structured trust contract")
		}
	}
}

func TestNativeComponentProtectionRequiresQualifiedServiceContract(t *testing.T) {
	original, _ := Default().Kind("falkordb")
	for _, edit := range []func(*Kind){
		func(k *Kind) { k.Service = nil },
		func(k *Kind) { k.Service.Recovery = "postgres" },
		func(k *Kind) { k.Service.BackupFormat = "invented" },
		func(k *Kind) { k.Components = append(k.Components, k.Components[0]) },
	} {
		k := original.clone()
		edit(&k)
		if err := NewRegistry().Register(k); err == nil {
			t.Fatal("unqualified native component admitted")
		}
	}
}
