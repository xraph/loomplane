package planning

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
)

func TestBuildIsolationAdmission(t *testing.T) {
	for _, cloud := range []string{"server", "kubernetes"} {
		for _, mode := range []string{"unknown", "shared", "dedicated", "kata", "unavailable", "pooled", "image"} {
			t.Run(cloud+"/"+mode, func(t *testing.T) {
				details := map[string]string{"registry_hostname": "images.example.com", "namespace_uid": "ns", "managed_builds": "ready", "architecture": "amd64"}
				c := infrastructureConnection(t, targetA, cloud, details)
				delete(c.Details, "build_workers")
				switch mode {
				case "shared":
					c.Details["build_workers"] = "shared"
				case "dedicated", "pooled":
					c.Details["build_workers"] = "dedicated"
				case "kata", "unavailable":
					c.Details["build_runtime_class"] = "isolated-builds"
					c.Details["build_runtime_identity"] = "checked-identity"
					if mode == "kata" {
						c.Details["build_isolation"] = "ready"
					}
				}
				c.Pooled = mode == "pooled"
				build := ""
				if mode == "image" {
					build = ", build: {image: 'example.com/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}"
				}
				doc := fmt.Sprintf("version: 1\nservices:\n  api: {source: cmd/api, provider: %s, target: %s, public: false%s}\n", cloud, targetA, build)
				r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: infrastructureProviders(t), Connections: []ConnectionOption{c}})
				blocked := slices.ContainsFunc(r.Problems, func(f Finding) bool { return f.Code == "build-isolation-required" })
				wantAllowed := mode == "dedicated" || mode == "image" || (cloud == "kubernetes" && mode == "kata")
				if blocked == wantAllowed {
					t.Fatalf("policy mismatch: %+v", r.Problems)
				}
				if cloud == "kubernetes" && mode == "kata" {
					cfg := action(t, r, "service:api").Service
					if cfg.BuildTarget.BuilderSettings["build_runtime_class"] != "isolated-builds" || cfg.BuildTarget.BuilderSettings["build_runtime_identity"] != "checked-identity" {
						t.Fatal("runtime was not pinned")
					}
				}
			})
		}
	}
}

func TestBuildPolicyDoesNotChangeRuntimePlacement(t *testing.T) {
	details := map[string]string{"endpoint": "https://cluster.example.com", "namespace_uid": "ns", "build_workers": "shared", "build_runtime_class": "kata", "build_runtime_identity": "checked"}
	runtime := ReviewedConnectionSettings("kubernetes", details)
	build := ReviewedBuildSettings("kubernetes", details)
	if runtime["build_runtime_identity"] != "" || runtime["build_workers"] != "" || build["build_runtime_identity"] != "checked" {
		t.Fatal("build policy leaked into runtime placement")
	}
	details["build_runtime_identity"] = "replaced"
	if !reflect.DeepEqual(runtime, ReviewedConnectionSettings("kubernetes", details)) || reflect.DeepEqual(build, ReviewedBuildSettings("kubernetes", details)) {
		t.Fatal("runtime drift did not remain confined to the build snapshot")
	}
}
