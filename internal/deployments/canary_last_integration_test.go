//go:build integration

package deployments

import (
	"fmt"
	"testing"
	"time"
)

// The newest canary that ramped a service is found however many newer
// canaries of other services ran since, and only in its own environment
// and organization.
func TestPgLastCanaryIsThatServicesNewestRun(t *testing.T) {
	ctx, h, pg := historyDB(t)
	store := h.Store
	create := func(id, env string, started time.Time, status string, actions ...string) {
		t.Helper()
		historyRelease{plan: "plan_" + id, deployment: id, env: env, canary: status, status: StatusSucceeded, finished: started.Add(5 * time.Minute)}.record(ctx, t, h, pg)
		c := &Canary{Status: status, Steps: []int{100}, Weight: 100}
		for _, a := range actions {
			c.Services = append(c.Services, CanaryService{ActionID: a, Name: a})
		}
		if err := store.SetCanary(ctx, "org", id, c); err != nil {
			t.Fatal(err)
		}
	}
	create("dep_api_ok", "env_b", at(8, 0), CanaryPromoted, "service:api")
	create("dep_api_bad", "env_b", at(9, 0), CanaryRolledBack, "service:api", "service:web")
	for i := range 12 {
		create(fmt.Sprintf("dep_web_%02d", i), "env_b", at(10, i), CanaryPromoted, "service:web")
	}
	create("dep_api_staging", "env_a", at(23, 0), CanaryPromoted, "service:api")

	got, err := store.LastCanary(ctx, "org", "proj", "env_b", "service:api")
	if err != nil || got == nil || got.ID != "dep_api_bad" || got.Canary.Status != CanaryRolledBack {
		t.Fatalf("api: %+v %v", got, err)
	}
	if got, err = store.LastCanary(ctx, "org", "proj", "env_b", "service:web"); err != nil || got == nil || got.ID != "dep_web_11" {
		t.Fatalf("web: %+v %v", got, err)
	}
	if got, err = store.LastCanary(ctx, "org", "proj", "env_b", "service:jobs"); err != nil || got != nil {
		t.Fatalf("jobs: %+v %v", got, err)
	}
	if got, err = store.LastCanary(ctx, "org_other", "proj", "env_b", "service:api"); err != nil || got != nil {
		t.Fatalf("another org: %+v %v", got, err)
	}
}
