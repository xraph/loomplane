package projects

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/security"
)

func TestService_excludeAndInclude(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)
	viewer := f.principal(t, "ausr_viewer", security.RoleViewer)
	p, err := f.svc.Create(ctx, dev, CreateProjectInput{Name: "Loomplane"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := f.svc.Exclude(ctx, dev, p.ID, ExcludeInput{Exclusions: []ExclusionInput{
		{Kind: ExclusionService, Match: "svc:repository:docker:deployments/miget/build", Reason: ReasonTooling, Name: "build"},
		{Kind: ExclusionPath, Match: "/tests/acceptance/**/", Reason: ReasonFixture, Name: "ignored", KeepRunning: true},
	}})
	if err != nil {
		t.Fatalf("Exclude: %v", err)
	}
	if len(got.Exclusions) != 2 {
		t.Fatalf("exclusions = %+v, want 2", got.Exclusions)
	}
	path := got.Exclusions[1]
	if path.Match != "tests/acceptance/**" || path.Name != "" || path.KeepRunning || path.CreatedBy != "ausr_dev" {
		t.Fatalf("path exclusion = %+v; want a cleaned match, no name, not kept running, by ausr_dev", path)
	}

	// Excluding again updates in place and keeps who first excluded it.
	other := f.principal(t, "ausr_other", security.RoleDeveloper)
	got, err = f.svc.Exclude(ctx, other, p.ID, ExcludeInput{Exclusions: []ExclusionInput{
		{Kind: ExclusionService, Match: "svc:repository:docker:deployments/miget/build", Reason: ReasonElsewhere, KeepRunning: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Exclusions) != 2 || got.Exclusions[0].Reason != ReasonElsewhere || !got.Exclusions[0].KeepRunning || got.Exclusions[0].CreatedBy != "ausr_dev" {
		t.Fatalf("after re-excluding: %+v", got.Exclusions)
	}

	if _, err = f.svc.Exclude(ctx, viewer, p.ID, ExcludeInput{Exclusions: []ExclusionInput{{Kind: ExclusionPath, Match: "docs"}}}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("viewer Exclude: %v, want forbidden", err)
	}

	got, err = f.svc.Include(ctx, dev, p.ID, IncludeInput{Exclusions: []ExclusionRef{{Kind: ExclusionPath, Match: "tests/acceptance/**"}, {Kind: ExclusionPath, Match: "not/there"}}})
	if err != nil {
		t.Fatalf("Include: %v", err)
	}
	if len(got.Exclusions) != 1 || got.Exclusions[0].Kind != ExclusionService {
		t.Fatalf("after Include: %+v", got.Exclusions)
	}
	if !strings.Contains(strings.Join(f.audit.actions, ","), "project.exclude,project.exclude,project.include") {
		t.Fatalf("audit %v", f.audit.actions)
	}
}

func TestService_excludeValidation(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)
	p, err := f.svc.Create(ctx, dev, CreateProjectInput{Name: "Loomplane"})
	if err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string]ExclusionInput{
		"no kind":       {Match: "x"},
		"empty service": {Kind: ExclusionService},
		"root path":     {Kind: ExclusionPath, Match: "/"},
		"escaping path": {Kind: ExclusionPath, Match: "../other"},
		"bad pattern":   {Kind: ExclusionPath, Match: "cmd/[x"},
		"bad reason":    {Kind: ExclusionPath, Match: "docs", Reason: "bored"},
	} {
		if _, err := f.svc.Exclude(ctx, dev, p.ID, ExcludeInput{Exclusions: []ExclusionInput{in}}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	if _, err := f.svc.Exclude(ctx, dev, p.ID, ExcludeInput{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty: %v, want ErrInvalid", err)
	}
}
