package fleets

import (
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/fleet"
)

func renderFixture(t *testing.T) (fleet.TemplateRelease, fleet.Instance, fleet.CreateInstanceInput) {
	t.Helper()
	raw := `schema_version: 1
manifest:
  version: 1
  services:
    api:
      kind: http-service
      build: {image: registry.example.test/api@sha256:` + strings.Repeat("a", 64) + `}
profiles:
  small:
    isolation: namespace
    services:
      api: {cpu_millis: 500, memory_mib: 256, min_replicas: 1, max_replicas: 3}
parameters:
  label: {kind: string, required: true, max_length: 30}
secrets:
  token: {required: true, services: [api]}
config:
  api:
    DISPLAY_LABEL: {parameter: label}
    API_TOKEN: {secret: token}
`
	tmpl, err := fleet.ParseTemplate([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := fleet.CanonicalDigest(tmpl)
	if err != nil {
		t.Fatal(err)
	}
	label := "example"
	return fleet.TemplateRelease{ID: "release", FleetID: "fleet", Digest: digest, Template: *tmpl}, fleet.Instance{ID: "instance", FleetID: "fleet", StageID: "stage", EnvironmentID: "environment", ProfileID: "small", DesiredReleaseID: "release", DesiredGeneration: 1, OwnershipEpoch: 1}, fleet.CreateInstanceInput{StageID: "stage", ReleaseID: "release", ProfileID: "small", Parameters: map[string]fleet.ParameterValue{"label": {Kind: "string", StringValue: &label}}, SecretRefs: map[string]fleet.SecretRef{"token": {ID: "var_example", Version: 2}}}
}
func TestInstanceRenderDeterministic(t *testing.T) {
	release, instance, input := renderFixture(t)
	a, err := RenderInstance(release, instance, input)
	if err != nil {
		t.Fatal(err)
	}
	b, err := RenderInstance(release, instance, input)
	if err != nil || a.Digest != b.Digest {
		t.Fatalf("unstable rendering: %+v %v", b, err)
	}
	if a.Values["api"]["DISPLAY_LABEL"] != "example" || a.Values["api"]["LOOMPLANE_INSTANCE_ID"] != instance.ID || a.Secrets["api"]["API_TOKEN"].Version != 2 {
		t.Fatalf("rendered config: %+v", a)
	}
	if a.Manifest.Services["api"].Size.CPU != 0.5 || a.Manifest.Services["api"].Scale.Max != 1 {
		t.Fatal("profile sizing or automatic scaling default changed")
	}
	instance.ID = "second"
	instance.EnvironmentID = "second-env"
	c, err := RenderInstance(release, instance, input)
	if err != nil || a.Digest == c.Digest || a.EnvironmentID == c.EnvironmentID {
		t.Fatalf("instance identity collision: %+v %v", c, err)
	}
}
func TestInstanceReservedVariableRejected(t *testing.T) {
	release, instance, input := renderFixture(t)
	label := "override"
	input.Parameters["namespace"] = fleet.ParameterValue{Kind: "string", StringValue: &label}
	if _, err := RenderInstance(release, instance, input); err == nil {
		t.Fatal("unknown identity parameter accepted")
	}
	delete(input.Parameters, "namespace")
	release.Template.Config["api"]["LOOMPLANE_INSTANCE_ID"] = fleet.ConfigValue{Parameter: "label"}
	if _, err := fleet.CanonicalDigest(&release.Template); err == nil {
		t.Fatal("reserved platform variable can be bound")
	}
}
func TestInstanceTemplatePinned(t *testing.T) {
	release, instance, input := renderFixture(t)
	first, err := RenderInstance(release, instance, input)
	if err != nil {
		t.Fatal(err)
	}
	service := release.Template.Manifest.Services["api"]
	service.Build.Image = "changed"
	if first.Manifest.Services["api"].Build.Image == "changed" {
		t.Fatal("snapshot aliases template")
	}
	if _, err = RenderInstance(release, instance, input); err == nil {
		t.Fatal("changed published digest accepted")
	}
}
func TestInstanceInputsBounded(t *testing.T) {
	for _, change := range []func(*fleet.CreateInstanceInput){func(i *fleet.CreateInstanceInput) { i.ProfileID = "other" }, func(i *fleet.CreateInstanceInput) { i.StageID = "other" }, func(i *fleet.CreateInstanceInput) {
		v := strings.Repeat("x", 31)
		i.Parameters["label"] = fleet.ParameterValue{Kind: "string", StringValue: &v}
	}, func(i *fleet.CreateInstanceInput) { delete(i.SecretRefs, "token") }, func(i *fleet.CreateInstanceInput) {
		i.SecretRefs["token"] = fleet.SecretRef{ID: "../other", Version: 2}
	}} {
		release, instance, input := renderFixture(t)
		change(&input)
		if _, err := RenderInstance(release, instance, input); err == nil {
			t.Fatal("invalid snapshot input accepted")
		}
	}
}

func TestInstanceEmptyInputsCanonical(t *testing.T) {
	release, instance, input := renderFixture(t)
	release.Template.Parameters = nil
	release.Template.Secrets = nil
	release.Template.Config = nil
	var err error
	release.Digest, err = fleet.CanonicalDigest(&release.Template)
	if err != nil {
		t.Fatal(err)
	}
	input.Parameters = nil
	input.SecretRefs = nil
	a, err := RenderInstance(release, instance, input)
	if err != nil {
		t.Fatal(err)
	}
	input.Parameters = map[string]fleet.ParameterValue{}
	input.SecretRefs = map[string]fleet.SecretRef{}
	b, err := RenderInstance(release, instance, input)
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest != b.Digest {
		t.Fatal("empty and omitted input maps have different snapshot identities")
	}
}
