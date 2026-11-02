package planning

import (
	"fmt"
	"strings"
	"testing"
)

func TestManagedKubernetesBuildNeedsOnlyCluster(t *testing.T) {
	details := map[string]string{"namespace": "apps", "namespace_uid": "ns-1", "registry_hostname": "images.example.com", "storage_class": "retained", "architecture": "amd64", "managed_builds": "ready"}
	doc := fmt.Sprintf("version: 1\nservices:\n  api: {source: cmd/api, provider: kubernetes, target: %s, public: false}\n", targetA)
	compute := func() *Result {
		return Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: infrastructureProviders(t), Connections: []ConnectionOption{infrastructureConnection(t, targetA, "kubernetes", details)}})
	}
	r := compute()
	if !r.Executable {
		t.Fatalf("managed build required external connections: %+v", r.Problems)
	}
	cfg := action(t, r, "service:api").Service
	if !cfg.ManagedKubernetesBuild() || cfg.TargetSettings["registry_hostname"] != details["registry_hostname"] || cfg.ImageRegistry() == "" {
		t.Fatalf("managed destination was not pinned: %+v", cfg)
	}
	details["managed_builds"] = "unavailable"
	if r = compute(); r.Executable {
		t.Fatal("missing build permissions accepted")
	}
}

func TestManagedKubernetesPromotionKeepsRegistryBoundary(t *testing.T) {
	cfg := &ServiceConfig{Provider: "kubernetes", Target: targetA, CommitSHA: artifactCommit, Source: "cmd/api", TargetSettings: map[string]string{"registry_hostname": "images.example.com", "namespace_uid": "namespace-one", "architecture": "amd64"}, BuildTarget: &BuildTarget{}}
	a := Action{ID: "service:api", Name: "api", Provider: "kubernetes", Op: Update, Subject: ServiceSubject, Service: cfg}
	image := ReleasedImage{Service: "api", Provider: "kubernetes", ConnectionID: targetA, Registry: cfg.ImageRegistry(), CommitSHA: artifactCommit, Architecture: "amd64", Inputs: cfg.BuildInputs(), Image: "images.example.com/loomplane/api@sha256:" + strings.Repeat("b", 64)}
	_, reused := artifactService().reusable("production", nil, a, image, "staging")
	if reused == nil {
		t.Fatal("same-cluster promotion rebuilt its image")
	}
	cfg.TargetSettings["namespace_uid"] = "namespace-two"
	_, reused = artifactService().reusable("production", nil, a, image, "staging")
	if reused != nil {
		t.Fatal("promotion reused an image across namespace replacement")
	}
	cfg.BuildTarget.Builder = targetB
	if cfg.ManagedKubernetesBuild() {
		t.Fatal("managed build overrode an explicit SSH builder")
	}
}
