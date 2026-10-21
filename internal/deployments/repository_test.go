package deployments

import (
	"testing"

	"github.com/xraph/loomplane/internal/planning"
)

func TestRepositoryForUsesExactAppliedServiceSource(t *testing.T) {
	for _, tc := range []struct {
		name, alias, commit, repository string
		wantError                       bool
	}{
		{name: "applied repository", alias: "app", commit: "applied", repository: "owner/applied"},
		{name: "wrong alias", alias: "other", commit: "applied", repository: "owner/applied", wantError: true},
		{name: "wrong commit", alias: "app", commit: "newer", repository: "owner/applied", wantError: true},
		{name: "missing repository", alias: "app", commit: "applied", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := &Job{CommitSHA: "applied", Deployment: &Deployment{Sources: []SourceArtifact{{Alias: "app", Repository: "owner/newer", CommitSHA: "newer"}}}, Plan: &planning.Plan{CloudSyncID: "sync", Sources: []planning.PlanSource{{Alias: "app", Repository: "owner/newer", CommitSHA: "newer"}}}, Action: planning.Action{Service: &planning.ServiceConfig{Repo: "app", CommitSHA: "applied", AppliedInputs: &planning.AppliedInputs{Source: &planning.PlanSource{Alias: tc.alias, CommitSHA: tc.commit, Repository: tc.repository}, ArtifactKey: "applied-artifact"}}}}
			got, err := repositoryFor(job)
			if tc.wantError {
				if err == nil {
					t.Fatalf("accepted invalid applied source as %q", got)
				}
				return
			}
			if err != nil || got != "owner/applied" {
				t.Fatalf("repository %q, error %v", got, err)
			}
		})
	}
}
