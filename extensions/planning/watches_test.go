package planning

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/releasewatch"
	"github.com/xraph/loomplane/internal/security"
)

// fakeWatches records what the watch routes ask, and answers with err when
// it's set.
type fakeWatches struct {
	err   error
	asked []string
}

func (f *fakeWatches) ForDeployment(_ context.Context, _ security.Principal, deploymentID string) (*releasewatch.Watch, error) {
	f.asked = append(f.asked, "watch "+deploymentID)
	if f.err != nil {
		return nil, f.err
	}
	return &releasewatch.Watch{ID: "watch_1", DeploymentID: deploymentID, Status: releasewatch.StatusWatching}, nil
}

func (f *fakeWatches) Armable(_ context.Context, _ security.Principal, projectID, environment string) (*releasewatch.Coverage, error) {
	f.asked = append(f.asked, "coverage "+projectID+" "+environment)
	if f.err != nil {
		return nil, f.err
	}
	return &releasewatch.Coverage{Environment: environment, Duration: 15 * time.Minute, Canary: []string{"web"}}, nil
}

func (f *fakeWatches) Evidence(_ context.Context, _ security.Principal, projectID, environment string) (*releasewatch.ReleaseEvidence, error) {
	f.asked = append(f.asked, "evidence "+projectID+" "+environment)
	if f.err != nil {
		return nil, f.err
	}
	return &releasewatch.ReleaseEvidence{Environment: environment, DeploymentID: "dep_1", Watched: true, Settled: true}, nil
}

func TestWatchRoutes(t *testing.T) {
	svc := &fakeWatches{}
	r := forge.NewRouter()
	if err := watchRoutes(r, nil, svc); err != nil {
		t.Fatal(err)
	}
	out := send(t, r, http.MethodGet, "/api/v1/deployments/dep_1/watch", "", true)
	var w releasewatch.Watch
	if out.Code != http.StatusOK || json.Unmarshal(out.Body.Bytes(), &w) != nil || w.ID != "watch_1" || w.DeploymentID != "dep_1" {
		t.Fatalf("watch: %d %s", out.Code, out.Body.String())
	}
	out = send(t, r, http.MethodGet, "/api/v1/projects/p/environments/production/watch-coverage", "", true)
	var cov releasewatch.Coverage
	if out.Code != http.StatusOK || json.Unmarshal(out.Body.Bytes(), &cov) != nil || cov.Duration != 15*time.Minute || cov.Canary[0] != "web" {
		t.Fatalf("coverage: %d %s", out.Code, out.Body.String())
	}
	out = send(t, r, http.MethodGet, "/api/v1/projects/p/environments/production/release-evidence", "", true)
	var ev releasewatch.ReleaseEvidence
	if out.Code != http.StatusOK || json.Unmarshal(out.Body.Bytes(), &ev) != nil || ev.DeploymentID != "dep_1" || !ev.Watched {
		t.Fatalf("evidence: %d %s", out.Code, out.Body.String())
	}
	if want := []string{"watch dep_1", "coverage p production", "evidence p production"}; !reflect.DeepEqual(svc.asked, want) {
		t.Fatalf("asked %v", svc.asked)
	}

	for _, tc := range []struct {
		err  error
		code int
	}{
		{releasewatch.ErrNotFound, http.StatusNotFound},
		{fmt.Errorf("%w: this project has no environment called qa", projects.ErrEnvironmentNotFound), http.StatusNotFound},
		{fmt.Errorf("%w (deployment:read)", security.ErrForbidden), http.StatusForbidden},
	} {
		svc.err = tc.err
		for _, path := range []string{
			"/api/v1/deployments/dep_1/watch",
			"/api/v1/projects/p/environments/production/watch-coverage",
			"/api/v1/projects/p/environments/production/release-evidence",
		} {
			if out = send(t, r, http.MethodGet, path, "", true); out.Code != tc.code || !strings.Contains(out.Body.String(), tc.err.Error()) {
				t.Fatalf("%s with %v: %d %s", path, tc.err, out.Code, out.Body.String())
			}
			if out = send(t, r, http.MethodGet, path, "", false); out.Code != http.StatusUnauthorized {
				t.Fatalf("%s signed out: %d", path, out.Code)
			}
		}
	}
}

// The real service sits behind the watch routes.
var _ watchService = (*releasewatch.Service)(nil)
