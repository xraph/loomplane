package octopusloom

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/planning"
)

// reconcileFixture is the hook fixture with three gateways in env_x, edge,
// inner and side, each with its own Octopus and binding.
func reconcileFixture(t *testing.T) *hookFixture {
	t.Helper()
	f := newHookFixture(t)
	other := hookRef("edge")
	other.EnvironmentID = "env_other"
	delete(f.store.gateways, other)
	f.store.gateways[hookRef("side")] = &Gateway{GatewayRef: hookRef("side"), Provider: ProviderServer, Generation: 4, AppliedGeneration: 3}
	f.octo["side"] = newFakeOctopus(t)
	f.bindings = append(f.bindings, &planning.Binding{Subject: planning.ResourceSubject, Name: "side--gateway", ConnectionID: "conn_a", ExternalID: testInstance})
	return f
}

func TestReconcileJobSyncsEveryGatewayAndSurvivesOneFailure(t *testing.T) {
	f := reconcileFixture(t)
	f.openErr["inner"] = errors.New("connection refused")
	def := ReconcileJob(f.syncer)
	if def.Name != JobOctopusReconcile || JobOctopusReconcile != "loomplane.octopus.reconcile" || CronOctopusReconcile != "loomplane.octopus" {
		t.Fatalf("job %q, cron %q", def.Name, CronOctopusReconcile)
	}
	if err := def.Handler(context.Background(), ReconcilePayload{}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	for _, name := range []string{"edge", "side"} {
		g := f.gateway(name)
		if g.AppliedGeneration != g.Generation || g.State.Error != "" {
			t.Errorf("%s = applied %d of %d, error %q", name, g.AppliedGeneration, g.Generation, g.State.Error)
		}
		if len(f.octo[name].log()) == 0 {
			t.Errorf("%s's Octopus wasn't called", name)
		}
	}
	if g := f.gateway("inner"); g.State.Error != msgUnavailable {
		t.Fatalf("inner's sync error = %q", g.State.Error)
	}
	if len(f.warns) != 1 || !strings.Contains(f.warns[0], "inner") {
		t.Fatalf("warnings %q, want one for inner", f.warns)
	}
	for _, w := range f.warns {
		if strings.Contains(w, fakeAdminToken) || strings.Contains(w, fakeFarpToken) {
			t.Fatalf("a warning holds a token: %q", w)
		}
	}
}

func TestReconcileJobDoesntWarnAboutAGatewayThatNeverDeployed(t *testing.T) {
	f := reconcileFixture(t)
	// side's first deploy never wrote its binding.
	f.bindings = slices.DeleteFunc(f.bindings, func(b *planning.Binding) bool { return b.Name == "side--gateway" })
	f.openErr["inner"] = errors.New("connection refused")
	if err := ReconcileJob(f.syncer).Handler(context.Background(), ReconcilePayload{}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if g := f.gateway("side"); g.State.Error != msgNotRunning {
		t.Fatalf("side's sync error = %q", g.State.Error)
	}
	// A real failure still warns.
	if len(f.warns) != 1 || !strings.Contains(f.warns[0], "inner") {
		t.Fatalf("warnings %q, want one for inner", f.warns)
	}
}

func TestReconcileJobWarnsWhenAGatewayThatNeverDeployedCantBeRecorded(t *testing.T) {
	f := reconcileFixture(t)
	f.bindings = slices.DeleteFunc(f.bindings, func(b *planning.Binding) bool { return b.Name == "side--gateway" })
	// Every save fails, so each gateway warns; side's is the one that
	// mustn't be taken for the quiet wait.
	f.store.saveErr = errors.New("db down")
	if err := ReconcileJob(f.syncer).Handler(context.Background(), ReconcilePayload{}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	warned := false
	for _, w := range f.warns {
		if strings.Contains(w, "side") && strings.Contains(w, "db down") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("warnings %q, want one for side's failed save", f.warns)
	}
}

func TestPendingNotDeployedWithAFailedSaveIsLoomplaneSide(t *testing.T) {
	f := newHookFixture(t)
	f.bindings = f.bindings[2:]
	f.store.saveErr = errors.New("db down")
	_, err := f.syncer.Sync(context.Background(), hookRef("edge"))
	if err == nil || !errors.Is(err, errLoomplaneSide) || !errors.Is(err, ErrUnavailable) || errors.Is(err, errNotDeployed) {
		t.Fatalf("Sync = %v, want Loomplane-side and unavailable, not not-deployed", err)
	}
}

func TestReconcileJobFailsOnlyWhenItCantListGateways(t *testing.T) {
	s := &Syncer{Store: failingGateways{newHookStore(new([]string))}}
	if err := ReconcileJob(s).Handler(context.Background(), ReconcilePayload{}); err == nil {
		t.Fatal("reconcile succeeded without its gateways")
	}
}

type failingGateways struct{ *hookStore }

func (failingGateways) Gateways(context.Context) ([]*Gateway, error) {
	return nil, errors.New("db down")
}

func TestReconcileJobSyncsAtMostEightAtOnce(t *testing.T) {
	f := reconcileFixture(t)
	for i := range 20 {
		name := fmt.Sprintf("gw%02d", i)
		f.store.gateways[hookRef(name)] = &Gateway{GatewayRef: hookRef(name), Provider: ProviderServer, Generation: 1}
		f.openErr[name] = errors.New("down")
		f.bindings = append(f.bindings, &planning.Binding{Subject: planning.ResourceSubject, Name: name + "--gateway", ConnectionID: "conn_a", ExternalID: testInstance})
	}
	f.delay = 20 * time.Millisecond
	if err := ReconcileJob(f.syncer).Handler(context.Background(), ReconcilePayload{}); err != nil {
		t.Fatal(err)
	}
	if len(f.opens) != 23 {
		t.Fatalf("opened %d gateways, want 23", len(f.opens))
	}
	if f.maxInFlight > 8 || f.maxInFlight < 2 {
		t.Fatalf("%d syncs ran at once, want between 2 and 8", f.maxInFlight)
	}
}

func TestReconcileJobBoundsEachSync(t *testing.T) {
	if reconcileSyncTimeout != 10*time.Second {
		t.Fatalf("per-gateway bound = %s, want 10s", reconcileSyncTimeout)
	}
	old := reconcileSyncTimeout
	reconcileSyncTimeout = 50 * time.Millisecond
	t.Cleanup(func() { reconcileSyncTimeout = old })
	f := reconcileFixture(t)
	f.block = true
	start := time.Now()
	if err := ReconcileJob(f.syncer).Handler(context.Background(), ReconcilePayload{}); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("reconcile took %s", took)
	}
	if len(f.warns) != 3 {
		t.Fatalf("warnings %q, want one per gateway", f.warns)
	}
}
