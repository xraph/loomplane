package planning

import (
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/providers"
)

// workerID is the graph ID of examples/polyglot's worker, which nothing in
// acme needs, so excluding it leaves the rest of the plan alone.
func workerID(t *testing.T) string {
	t.Helper()
	for _, s := range polyglot(t).Services {
		if s.Entrypoint == "services/worker/cmd/worker" || s.Path == "services/worker/cmd/worker" {
			return s.ID
		}
	}
	t.Fatal("polyglot has no worker")
	return ""
}

func computeExcluding(t *testing.T, excluded map[string]bool, bindings ...*Binding) *Result {
	t.Helper()
	return Compute(Input{
		Environment: "production", Graph: polyglot(t), Manifest: parse(t, acme),
		Providers: providers.Registry(), Bindings: bindings, Connections: connected,
		Excluded: excluded,
	})
}

func TestCompute_excludedServices(t *testing.T) {
	id := workerID(t)
	live := deployed(t, compute(t, acme, "production"))

	t.Run("not deployed is never created", func(t *testing.T) {
		r := computeExcluding(t, map[string]bool{id: false})
		if len(r.Problems) > 0 {
			t.Fatalf("problems: %+v", r.Problems)
		}
		for _, a := range r.Actions {
			if a.ID == "service:worker" {
				t.Fatalf("worker planned though excluded: %+v", a)
			}
		}
		if r.Summary != (Summary{Create: 5}) {
			t.Errorf("summary = %+v, want the other five created", r.Summary)
		}
		for _, w := range r.Warnings {
			if w.Name == "worker" {
				t.Errorf("warning about the excluded worker: %+v", w)
			}
		}
	})

	t.Run("deployed is deleted, saying why", func(t *testing.T) {
		r := computeExcluding(t, map[string]bool{id: false}, live...)
		w := action(t, r, "service:worker")
		if w.Op != Delete || !strings.Contains(w.Reason, "excluded from this project") {
			t.Fatalf("worker = %+v, want a delete because it's excluded", w)
		}
		if r.Summary != (Summary{Keep: 5, Delete: 1}) {
			t.Errorf("summary = %+v", r.Summary)
		}
	})

	t.Run("kept running stays as deployed", func(t *testing.T) {
		r := computeExcluding(t, map[string]bool{id: true}, live...)
		w := action(t, r, "service:worker")
		if w.Op != Keep || !strings.Contains(w.Reason, "left running") {
			t.Fatalf("worker = %+v, want kept as it runs", w)
		}
		if r.Summary != (Summary{Keep: 6}) {
			t.Errorf("summary = %+v", r.Summary)
		}
	})

	t.Run("kept running but not deployed creates nothing", func(t *testing.T) {
		r := computeExcluding(t, map[string]bool{id: true})
		for _, a := range r.Actions {
			if a.ID == "service:worker" {
				t.Fatalf("worker planned though excluded: %+v", a)
			}
		}
	})
}

// An excluded service deploys nothing, so a canary release and a
// promotion's artifact reuse both pass it over, whether it's deleted or
// left running.
func TestCompute_excludedServicesInAReleaseAndAPromotion(t *testing.T) {
	id := workerID(t)
	live := deployed(t, compute(t, acme, "production"))
	// The worker changes, so a plan that doesn't exclude it updates it.
	changed := strings.Replace(acme, "    provider: ecs-fargate\n", "    provider: ecs-fargate\n    scale: {min: 1, max: 2}\n", 1) +
		"environments:\n  production:\n    release:\n      strategy: canary\n"
	plan := func(excluded map[string]bool) *Result {
		t.Helper()
		r := Compute(Input{
			Environment: "production", Graph: polyglot(t), Manifest: parse(t, changed),
			Providers: providers.Registry(), Bindings: live, Connections: connected,
			Excluded: excluded,
		})
		artifactService().applyArtifacts(promotionPlan(), r, map[string]ReleasedImage{}, "staging")
		return r
	}
	mentions := func(r *Result) []string {
		var got []string
		for _, w := range r.Warnings {
			if w.Name == "worker" && w.Code == codeCanaryUnavailable {
				got = append(got, "canary-unavailable")
			}
		}
		if r.Release != nil && r.Release.Ramps("service:worker") {
			got = append(got, "ramps")
		}
		for _, c := range r.Artifacts {
			if c.Service == "worker" {
				got = append(got, "artifact choice")
			}
		}
		return got
	}

	base := plan(nil)
	if w := action(t, base, "service:worker"); w.Op != Update {
		t.Fatalf("worker = %+v, want an update when it isn't excluded", w)
	}
	if got := mentions(base); len(got) != 2 {
		t.Fatalf("not excluded, worker shows up as %v, want a canary-unavailable warning and an artifact choice", got)
	}
	for _, keep := range []bool{false, true} {
		r := plan(map[string]bool{id: keep})
		if len(r.Problems) > 0 {
			t.Fatalf("keep %v: problems %+v", keep, r.Problems)
		}
		if r.Release == nil || r.Release.Strategy != "canary" {
			t.Fatalf("keep %v: release %+v, want the canary release pinned", keep, r.Release)
		}
		want := map[bool]Op{false: Delete, true: Keep}[keep]
		if w := action(t, r, "service:worker"); w.Op != want || w.Service != nil && w.Service.Artifact != nil {
			t.Fatalf("keep %v: worker = %+v, want %s with no artifact", keep, w, want)
		}
		if got := mentions(r); len(got) > 0 {
			t.Errorf("keep %v: the excluded worker shows up as %v", keep, got)
		}
	}
}
