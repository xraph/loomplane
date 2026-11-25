package deployments

import (
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/planning"
)

const doImageDigest = "sha256:" + "ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12"

// doImageConfig is a service the plan builds as an image. It pins one,
// which Build takes as it is, so these tests need no builder.
func doImageConfig() *planning.ServiceConfig {
	cfg := apiConfig()
	cfg.BuildTarget = &planning.BuildTarget{Image: "registry.digitalocean.com/acme/loomplane/api@" + doImageDigest}
	return cfg
}

func doImageExecutor(d *DigitalOcean) *DigitalOcean {
	d.Builder = &ImageBuilder{}
	return d
}

func TestDigitalOcean_runsTheImageNotTheRepository(t *testing.T) {
	f, client := newFakeAppPlatform(t)
	f.registries = []string{"acme"}
	cfg := doImageConfig()
	out, err := doImageExecutor(newDO(client)).Apply(t.Context(), doJob(cfg, "release"))
	if err != nil {
		t.Fatal(err)
	}
	if out.Image != cfg.BuildTarget.Image || out.Revision != "dep-app-api-production-23456789" {
		t.Fatalf("outcome %+v", out)
	}
	svc := f.specs[0]["services"].([]any)[0].(map[string]any)
	for _, key := range []string{"github", "gitlab", "dockerfile_path", "source_dir", "run_command"} {
		if _, set := svc[key]; set {
			t.Fatalf("an image service has %s: %v", key, svc)
		}
	}
	image := svc["image"].(map[string]any)
	if image["registry_type"] != "DOCR" || image["repository"] != "loomplane/api" || image["digest"] != doImageDigest {
		t.Fatalf("image %v", image)
	}
	// App Platform wants the registry's name only when there are several.
	if _, set := image["registry"]; set {
		t.Fatalf("one registry, but the spec names it: %v", image)
	}
	if svc["http_port"] != float64(8080) || svc["instance_count"] != float64(2) {
		t.Fatalf("service %v", svc)
	}
}

func TestDigitalOcean_namesTheRegistryWhenTheAccountHasSeveral(t *testing.T) {
	f, client := newFakeAppPlatform(t)
	f.registries = []string{"other", "acme"}
	if _, err := doImageExecutor(newDO(client)).Apply(t.Context(), doJob(doImageConfig(), "")); err != nil {
		t.Fatal(err)
	}
	image := f.specs[0]["services"].([]any)[0].(map[string]any)["image"].(map[string]any)
	if image["registry"] != "acme" {
		t.Fatalf("image %v", image)
	}
}

func TestDigitalOcean_refusesARegistryOutsideTheAccount(t *testing.T) {
	f, client := newFakeAppPlatform(t)
	f.registries = []string{"other"}
	_, err := doImageExecutor(newDO(client)).Apply(t.Context(), doJob(doImageConfig(), ""))
	if err == nil || !strings.Contains(err.Error(), "acme isn't in the account") {
		t.Fatalf("err %v", err)
	}
	if len(f.specs) != 0 || len(f.created) != 0 {
		t.Fatalf("set up an app it can't pull for: specs %v, created %v", f.specs, f.created)
	}
}

// The reviewed registry is checked before anything builds: this build
// would fail in the builder, with no build credentials, if it started.
func TestDigitalOcean_checksTheRegistryBeforeBuilding(t *testing.T) {
	f, client := newFakeAppPlatform(t)
	f.registries = []string{"other"}
	cfg := apiConfig()
	cfg.BuildTarget = &planning.BuildTarget{Builder: "conn_pool", Registry: "conn_docr", RegistrySettings: map[string]string{"endpoint": "https://registry.digitalocean.com", "repository": "acme"}}
	_, err := doImageExecutor(newDO(client)).Apply(t.Context(), doJob(cfg, ""))
	if err == nil || !strings.Contains(err.Error(), "acme isn't in the account") {
		t.Fatalf("err %v", err)
	}
}

func TestDigitalOcean_refusesADeploymentOfAnotherImage(t *testing.T) {
	f, client := newFakeAppPlatform(t)
	f.registries = []string{"acme"}
	f.alterBuild = func(spec map[string]any) {
		image := spec["services"].([]any)[0].(map[string]any)["image"].(map[string]any)
		image["digest"] = "sha256:" + strings.Repeat("cd", 32)
	}
	_, err := doImageExecutor(newDO(client)).Apply(t.Context(), doJob(doImageConfig(), ""))
	if err == nil || !strings.Contains(err.Error(), "other than the image Loomplane built") {
		t.Fatalf("err %v", err)
	}
}

// A rollback goes back to the recorded deployment and checks it runs the
// release's image, with no commit to compare: nothing was built from one.
func TestDigitalOcean_rollsAnImageBackToItsDeployment(t *testing.T) {
	f, client := newFakeAppPlatform(t)
	f.registries = []string{"acme"}
	d := doImageExecutor(newDO(client))
	job := doJob(doImageConfig(), "")
	first, err := d.Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	// App Platform reports no commit for an image, and the deployment
	// goes live.
	f.tip, f.liveAt = "", "ACTIVE"
	cfg := doImageConfig()
	cfg.Artifact = &planning.ReusedArtifact{Revision: first.Revision, Reference: first.Image, CommitSHA: doApproved}
	job = doJob(cfg, "")
	job.Binding = &planning.Binding{ExternalID: first.ExternalID}
	out, err := d.Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.rollbacks, []string{first.Revision}) || len(f.created) != 1 || out.Revision != "rb" || out.Image != first.Image {
		t.Fatalf("rollbacks %v, created %v, outcome %+v", f.rollbacks, f.created, out)
	}

	// The recorded deployment ran another image: the rollback fails.
	cfg.Artifact.Reference = "registry.digitalocean.com/acme/loomplane/api@sha256:" + strings.Repeat("cd", 32)
	job = doJob(cfg, "")
	job.Binding = &planning.Binding{ExternalID: first.ExternalID}
	if _, err := d.Apply(t.Context(), job); err == nil || !strings.Contains(err.Error(), "other than the image Loomplane built") {
		t.Fatalf("err %v", err)
	}
}

func TestDigitalOcean_refusesAnImageWithoutABuilder(t *testing.T) {
	f, client := newFakeAppPlatform(t)
	f.registries = []string{"acme"}
	if _, err := newDO(client).Apply(t.Context(), doJob(doImageConfig(), "")); err == nil {
		t.Fatal("deployed an image with no builder")
	}
}

func TestDOImageSource(t *testing.T) {
	for _, ref := range []string{
		"registry.digitalocean.com/acme/loomplane/api@" + doImageDigest,
		"registry.digitalocean.com/acme/loomplane/api:4f2a9c",
	} {
		src, registry, err := doImageSource(ref, doImageDigest)
		if err != nil || registry != "acme" || src.Repository != "loomplane/api" || src.Digest != doImageDigest || src.RegistryType != "DOCR" || src.Registry != "" {
			t.Fatalf("%s: %+v %q %v", ref, src, registry, err)
		}
	}
	for _, bad := range []struct{ ref, digest string }{
		{"ghcr.io/acme/api@" + doImageDigest, doImageDigest},
		{"registry.digitalocean.com/acme@" + doImageDigest, doImageDigest},
		{"registry.digitalocean.com/acme/api:latest", ""},
	} {
		if _, _, err := doImageSource(bad.ref, bad.digest); err == nil {
			t.Fatalf("accepted %s", bad.ref)
		}
	}
}
