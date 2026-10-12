package planning

import (
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/discovery"
)

func TestLockset(t *testing.T) {
	sources := []*discovery.Source{
		{ID: "src_api", Alias: "api", Repository: "acme/api"},
		{ID: "src_web", Alias: "web", Repository: "acme/web", Root: "apps/web"},
	}
	latest := map[string]*discovery.Analysis{
		"src_api": {ID: "anl_api2", CommitSHA: "api2"},
		"src_web": {ID: "anl_web2", CommitSHA: "web2"},
	}
	deployed := map[string]*DeployedSource{"src_api": {SourceID: "src_api", AnalysisID: "anl_api1", CommitSHA: "api1"}}

	all, err := lockset(sources, latest, deployed, nil, "production")
	if err != nil || len(all) != 2 || !all[0].InScope || all[0].CommitSHA != "api2" || all[1].Root != "apps/web" {
		t.Fatalf("everything: %v, %+v", err, all)
	}
	web, err := lockset(sources, latest, deployed, []string{"web"}, "production")
	if err != nil || web[0].InScope || web[0].CommitSHA != "api1" || web[0].AnalysisID != "anl_api1" || !web[1].InScope {
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
