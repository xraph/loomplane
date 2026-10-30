//go:build integration

package deployments

import (
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/cloudinfra"
	fc "github.com/xraph/loomplane/internal/firecracker"
)

func TestFirecrackerAuthorityFencesReplayTakeoverAndCancellation(t *testing.T) {
	dsn, template := admissionTemplate(t)
	fixture := admissionFixture(t, dsn, template)
	pg := fixture.db
	attempt, err := cloudinfra.NewPgStore(pg).BeginWorkloadAttempt(t.Context(), cloudinfra.WorkloadAttempt{Org: "org", Project: "proj", Environment: "env", Service: "api", Plan: "plan", Deployment: "deployment"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := attempt.Context(t.Context())
	store := NewPgStore(pg)
	scope := fc.Scope{Org: "org", Connection: "host", Project: "proj", Environment: "env", Resource: "api"}
	digest := strings.Repeat("a", 64)
	first, err := store.IssueFirecracker(ctx, scope, "apply", digest)
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.IssueFirecracker(ctx, scope, "apply", digest)
	if err != nil || again.Generation != first.Generation || again.Operation != first.Operation {
		t.Fatal("replay changed authority", again, err)
	}
	if _, err = store.IssueFirecracker(ctx, scope, "apply", strings.Repeat("b", 64)); !errors.Is(err, fc.ErrConflict) {
		t.Fatal("changed replay accepted", err)
	}
	next, err := store.IssueFirecracker(ctx, scope, "stop", digest)
	if err != nil || next.Generation <= first.Generation {
		t.Fatal("resource generation failed", next, err)
	}
	if _, err = store.IssueFirecracker(ctx, scope, "apply", digest); !errors.Is(err, fc.ErrConflict) {
		t.Fatal("superseded operation revived", err)
	}
	takeover, err := cloudinfra.NewPgStore(pg).BeginWorkloadAttempt(t.Context(), *attempt, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.IssueFirecracker(ctx, scope, "other", digest); err == nil {
		t.Fatal("stale worker renewed authority")
	}
	if _, err = store.IssueFirecracker(takeover.Context(t.Context()), scope, "apply", digest); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.Exec(t.Context(), `UPDATE loomplane_deployments SET status='cancelled',finished_at=now() WHERE id='deployment'`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.IssueFirecracker(takeover.Context(t.Context()), scope, "after-cancel", digest); err == nil {
		t.Fatal("cancelled worker issued authority")
	}
}
