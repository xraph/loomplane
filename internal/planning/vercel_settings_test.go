package planning

import (
	"encoding/json"
	"errors"
	"testing"

	domain "github.com/xraph/loomplane/pkg/federation"
	"github.com/xraph/loomplane/pkg/manifest"
)

func TestPlacementGroupsRejectVercelBuildSettings(t *testing.T) {
	for _, settings := range []manifest.Settings{{Commands: &manifest.BuildCommands{}}, {Vercel: &manifest.VercelSettings{Services: true}}} {
		in := groupProfileFixture(t)
		svc := in.Manifest.Services["api"]
		svc.Commands, svc.Vercel = settings.Commands, settings.Vercel
		in.Manifest.Services["api"] = svc
		if _, err := ValidatePlacementGroupProfile(in); err == nil {
			t.Fatal("accepted Vercel settings on a Kubernetes placement group")
		}
		store := &PgStore{}
		if err := store.SavePlacementGroupConfiguration(t.Context(), domain.QualifiedGroupPlan{}, GroupExecutionConfiguration{Settings: settings}); !errors.Is(err, ErrConflict) {
			t.Fatalf("retained config: %v", err)
		}
	}
}

func TestVercelServicesIsOneReviewedDeployment(t *testing.T) {
	doc := "version: 1\nservices:\n  web:\n    source: apps/web\n    provider: vercel\n    vercel:\n      services: true\n      root: .\n      service_commands:\n        api: {install: '', build: go build ./...}\n"
	r := compute(t, doc, "production")
	if len(r.Problems) != 0 {
		t.Fatalf("problems: %+v", r.Problems)
	}
	a := action(t, r, "service:web")
	if len(r.Actions) != 1 || a.Service.Source != "." || a.Service.Framework != "" || a.Service.Vercel == nil || !a.Service.Vercel.Services {
		t.Fatalf("action: %+v", a)
	}
	raw, err := json.Marshal(a.Service)
	if err != nil {
		t.Fatal(err)
	}
	var restored ServiceConfig
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Vercel.ServiceCommands["api"].Install == nil || *restored.Vercel.ServiceCommands["api"].Install != "" {
		t.Fatal("lost explicit skip in persisted plan")
	}
}

func TestVercelCommandsRejectOtherProviders(t *testing.T) {
	r := compute(t, "version: 1\nservices:\n  web: {source: apps/web, provider: render, commands: {build: test}}\n", "production")
	for _, problem := range r.Problems {
		if problem.Code == "vercel-settings-provider" {
			return
		}
	}
	t.Fatalf("missing provider rejection: %+v", r.Problems)
}
