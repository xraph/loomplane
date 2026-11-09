//go:build integration

package releaseprep

import (
	"time"

	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/internal/releasewatch"
)

// fullEvidence sets every evidence field, so a round trip proves the
// stored JSON keeps all of it.
func fullEvidence() *ReadinessEvidence {
	finished := t0.Add(-time.Hour)
	return &ReadinessEvidence{
		CollectedAt: t0,
		PlanStatus:  planning.StatusReady,
		Sources: []SourceDiff{{
			SourceID: "src_1", Alias: "api", Repository: "acme/api", ProductionCommit: "a", StagingCommit: "b",
			Direction: DirectionAhead, AheadBy: 1, TotalCommits: 1, FilesChanged: 2, Additions: 3, Deletions: 1,
			Commits:    []DiffCommit{{SHA: "b", Title: "Add users", Author: "ada", At: t0}},
			Migrations: []MigrationFile{{Path: "db/migrate/1_users.rb", Status: "added", Additions: 3, Patch: "+create_table"}},
		}},
		Problems:        []planning.Finding{{Code: "p", Message: "problem"}},
		Warnings:        []planning.Finding{{Code: WarningProductionMovesBack, Name: "api", Message: "api: staging runs b which is behind production's a"}},
		Destroy:         []string{"service:web"},
		DataUpdates:     []string{"resource:db"},
		BackupSchedule:  true,
		ChangedServices: []ChangedService{{ActionID: "service:api", Name: "api", Provider: "cloud-run", Op: "update", Canary: true}},
		Artifacts:       []planning.ArtifactChoice{{Service: "api", Reused: true, Reference: "acme/api@sha256:1", From: "staging"}},
		PinsMatch:       true,
		PinDifferences:  []string{"api: staging runs c, the plan pins b"},
		Soak: &releasewatch.Soak{
			Environment: "staging", Running: true, Since: t0.Add(-2 * time.Hour), Duration: 2 * time.Hour, Healthy: true,
			Reasons: []string{"r"}, Services: []releasewatch.SoakService{{Name: "api", Provider: "cloud-run", Verdict: releasehealth.Pass, Reason: "ok"}},
		},
		StagingRelease:    &releasewatch.ReleaseEvidence{Environment: "staging", DeploymentID: "dep_s", Watched: true, WatchID: "w_s", Status: "passed", Verdict: "pass", Settled: true},
		ProductionRelease: &releasewatch.ReleaseEvidence{Environment: "production", DeploymentID: "dep_p", Unverified: true, Reason: "production has no release yet"},
		Coverage: &releasewatch.Coverage{
			Environment: "production", Duration: 15 * time.Minute, Canary: []string{"api"},
			Services: []releasewatch.ServiceCoverage{{Name: "web", Provider: "render", Watchable: true, Reason: "r"}}, Unverified: true,
		},
		Canaries: []CanarySummary{{
			DeploymentID: "dep_p", PlanID: "plan_0", Status: "promoted", Reason: "fine", Services: []string{"api"}, ActionIDs: []string{"service:api"}, FinishedAt: &finished,
		}},
		OpenReports: []OpenReport{{ID: "arep_1", Title: "Errors up", Severity: "high", Path: "/agent/reports/arep_1", OpenedAt: t0}},
		Budget:      &agentsettings.BudgetState{LimitUSD: "25.00", SpentUSD: "5.00", Exhausted: false},
		Unavailable: map[string]string{KeyCanaries: "no canary has run"},
	}
}
