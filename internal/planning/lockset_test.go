package planning

import (
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/discovery"
)

func TestLockset(t *testing.T) {
	// Out of order, so the lockset's sort by alias shows.
	sources := []*discovery.Source{
		{ID: "src_web", Alias: "web", Repository: "acme/web", Root: "apps/web"},
		{ID: "src_api", Alias: "api", Repository: "acme/api"},
	}
	latest := map[string]*discovery.Analysis{
		"src_api": {ID: "anl_api2", CommitSHA: "api2"},
		"src_web": {ID: "anl_web2", CommitSHA: "web2"},
	}
	deployed := map[string]*DeployedSource{"src_api": {SourceID: "src_api", AnalysisID: "anl_api1", CommitSHA: "api1"}}

	all, err := lockset(sources, latest, deployed, nil, "production")
	if err != nil || len(all) != 2 || all[0].Alias != "api" || !all[0].InScope || all[0].CommitSHA != "api2" ||
		all[1].Alias != "web" || all[1].Root != "apps/web" {
		t.Fatalf("everything: %v, %+v", err, all)
	}
	// A scope alias is trimmed, as a hand-written request may pad it.
	web, err := lockset(sources, latest, deployed, []string{" web "}, "production")
	if err != nil || web[0].Alias != "api" || web[0].InScope || web[0].CommitSHA != "api1" || web[0].AnalysisID != "anl_api1" ||
		web[1].Alias != "web" || !web[1].InScope || web[1].CommitSHA != "web2" {
		t.Fatalf("web only: %v, %+v", err, web)
	}

	tests := []struct {
		name     string
		sources  []*discovery.Source
		latest   map[string]*discovery.Analysis
		deployed map[string]*DeployedSource
		scope    []string
		want     error
	}{
		{"no sources", nil, latest, deployed, nil, ErrNoSources},
		{"a source never analyzed", sources, map[string]*discovery.Analysis{"src_api": latest["src_api"]}, deployed, nil, ErrNoAnalysis},
		{"holding one never deployed", sources, latest, deployed, []string{"api"}, ErrNeverDeployed},
		{"an unknown or removed alias", sources, latest, deployed, []string{"gone"}, ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := lockset(tt.sources, tt.latest, tt.deployed, tt.scope, "production"); !errors.Is(err, tt.want) {
				t.Fatalf("lockset: %v, want %v", err, tt.want)
			}
		})
	}
}

func TestLockset_branchComesFromTheAnalysis(t *testing.T) {
	sources := []*discovery.Source{{ID: "src_api", Alias: "api", Repository: "acme/api", Ref: "main"}}
	deployed := map[string]*DeployedSource{"src_api": {SourceID: "src_api", AnalysisID: "anl_api1", CommitSHA: "api1"}}
	for _, tt := range []struct {
		name, analysisRef, want string
	}{
		// "Another branch" analyses do-test; its commit is on do-test.
		{"a branch analysed on purpose", "do-test", "do-test"},
		{"the source's own branch", "", "main"},
		// A commit or tag isn't a branch a builder can follow.
		{"a commit analysed on purpose", "0123456789abcdef0123456789abcdef01234567", "main"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			latest := map[string]*discovery.Analysis{"src_api": {ID: "anl_api2", CommitSHA: "api2", Ref: tt.analysisRef}}
			got, err := lockset(sources, latest, deployed, nil, "production")
			if err != nil || got[0].Ref != tt.want {
				t.Fatalf("ref %q, %v; want %q", got[0].Ref, err, tt.want)
			}
		})
	}
	// A held repository stays on its own branch.
	sources = []*discovery.Source{sources[0], {ID: "src_web", Alias: "web", Repository: "acme/web"}}
	latest := map[string]*discovery.Analysis{"src_api": {ID: "anl_api2", CommitSHA: "api2", Ref: "do-test"}, "src_web": {ID: "anl_web2", CommitSHA: "web2"}}
	got, err := lockset(sources, latest, deployed, []string{"web"}, "production")
	if err != nil || got[0].Alias != "api" || got[0].InScope || got[0].Ref != "main" {
		t.Fatalf("held: %v, %+v", err, got)
	}
}
