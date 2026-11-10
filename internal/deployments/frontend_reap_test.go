package deployments

import (
	"context"
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
)

// fakeLedger is the frontend ledger and plan store a FrontendReaper reads
// and settles: absence and cancellation drop the uses they settle.
type fakeLedger struct {
	held      []cloudinfra.HeldFrontend
	bindings  []*planning.Binding
	absences  []cloudinfra.FrontendAbsence
	cancelled []string
	// started are plans whose effects started, which cancelling can't settle.
	started map[string]bool
	records []*ProviderEnvironment
}

func (l *fakeLedger) ProviderEnvironments(context.Context, string, string) ([]*ProviderEnvironment, error) {
	return l.records, nil
}

func (l *fakeLedger) HeldFrontends(_ context.Context, _, _, env string) ([]cloudinfra.HeldFrontend, error) {
	var out []cloudinfra.HeldFrontend
	for _, h := range l.held {
		if env == "" || h.EnvironmentID == env {
			out = append(out, h)
		}
	}
	return out, nil
}

func (l *fakeLedger) ListBindings(_ context.Context, _, env string) ([]*planning.Binding, error) {
	var out []*planning.Binding
	for _, b := range l.bindings {
		if b.EnvironmentID == env {
			out = append(out, b)
		}
	}
	return out, nil
}

func (l *fakeLedger) RecordFrontendAbsence(_ context.Context, e cloudinfra.FrontendAbsence) error {
	l.absences = append(l.absences, e)
	l.held = slices.DeleteFunc(l.held, func(h cloudinfra.HeldFrontend) bool {
		return h.Service == e.Service && h.EnvironmentID == e.EnvironmentID &&
			(h.ExternalID == e.ExternalID || h.ExternalID == "" && h.PlanID == e.PlanID && h.Status == "accepted")
	})
	return nil
}

func (l *fakeLedger) CancelUnstartedFrontends(_ context.Context, _, plan string) error {
	l.cancelled = append(l.cancelled, plan)
	if !l.started[plan] {
		l.held = slices.DeleteFunc(l.held, func(h cloudinfra.HeldFrontend) bool {
			return h.PlanID == plan && h.Status == "pending" && h.ExternalID == ""
		})
	}
	return nil
}

type oneEnvironment struct{}

func (oneEnvironment) ListEnvironments(context.Context, string, string) ([]*projects.Environment, error) {
	return []*projects.Environment{{ID: "env_production", Name: "production"}}, nil
}

type fixedCredentials struct{}

func (fixedCredentials) Credential(context.Context, string, string) (*connections.Connection, []byte, error) {
	return &connections.Connection{ID: "conn"}, []byte("token"), nil
}

func newFrontendReaper(t *testing.T, ledger *fakeLedger) (*FrontendReaper, *fakeVercelAPI) {
	t.Helper()
	api := newFakeVercelAPI()
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	return &FrontendReaper{
		Ledger: ledger, Plans: ledger, Environments: oneEnvironment{}, Records: ledger, Credentials: fixedCredentials{},
		Executors: map[string]Executor{"vercel": &Vercel{Client: vercel.New(server.URL)}},
	}, api
}

func held(service, plan, external, status string) cloudinfra.HeldFrontend {
	return cloudinfra.HeldFrontend{CloudID: "cloud", SlotID: "slot-" + service, EnvironmentID: "env_production", Service: service,
		PlanID: plan, Provider: "vercel", ConnectionID: "conn", AccountID: "acct", ExternalID: external, Status: status}
}

// prepareAndRun prepares a whole project's deletion and runs what it found.
func prepareAndRun(t *testing.T, fr *FrontendReaper) (projects.Prepared, error) {
	t.Helper()
	got, err := fr.PrepareDelete(t.Context(), "org", "proj_abc12345", "", projects.DeleteEnvironmentOptions{})
	if err != nil || got.Run == nil {
		return got, err
	}
	err = got.Run(t.Context())
	return got, err
}

// A failed deploy's recorded project is removed from Vercel and its
// absence recorded, so the deletion can go ahead.
func TestFrontendReaperRemovesARecordedLeak(t *testing.T) {
	ledger := &fakeLedger{held: []cloudinfra.HeldFrontend{held("website", "plan_2", "prj_7", "accepted")}}
	fr, api := newFrontendReaper(t, ledger)
	api.projects["website-abc12345"] = "prj_7"
	got, err := prepareAndRun(t, fr)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Removes) != 1 || !strings.Contains(got.Removes[0], "website in production (prj_7)") {
		t.Fatalf("removes %v", got.Removes)
	}
	if !api.calledExactly("DELETE /v9/projects/prj_7") || len(api.projects) != 0 {
		t.Fatalf("Vercel still has %v; calls %v", api.projects, api.callLog())
	}
	if len(ledger.absences) != 1 || ledger.absences[0].ExternalID != "prj_7" || ledger.absences[0].PlanID != "plan_2" || ledger.absences[0].AccountID != "acct" {
		t.Fatalf("absences %+v", ledger.absences)
	}
}

// An older deploy that never named its project, with a project Vercel
// still has under the name it would have used, refuses the deletion and
// names it, without deleting anything.
func TestFrontendReaperRefusesAnUnrecordedFrontendVercelStillHas(t *testing.T) {
	ledger := &fakeLedger{held: []cloudinfra.HeldFrontend{held("app", "plan_1", "", "accepted")}}
	fr, api := newFrontendReaper(t, ledger)
	api.projects["app-production-abc12345"] = "prj_3"
	got, err := prepareAndRun(t, fr)
	if !errors.Is(err, projects.ErrFrontendsHeld) || !strings.Contains(err.Error(), "app-production-abc12345") {
		t.Fatalf("an unrecorded frontend Vercel has: %v", err)
	}
	if len(got.Removes) != 0 || api.called("DELETE ") || len(ledger.absences) != 0 {
		t.Fatalf("removes %v, calls %v, absences %v", got.Removes, api.callLog(), ledger.absences)
	}
}

// Once Vercel has nothing it could be, the unrecorded frontend's slot is
// released on that evidence.
func TestFrontendReaperReleasesAnUnrecordedFrontendVercelLacks(t *testing.T) {
	ledger := &fakeLedger{held: []cloudinfra.HeldFrontend{held("app", "plan_1", "", "accepted")}}
	fr, _ := newFrontendReaper(t, ledger)
	if _, err := prepareAndRun(t, fr); err != nil {
		t.Fatal(err)
	}
	if len(ledger.absences) != 1 || ledger.absences[0].ExternalID != "app-production-abc12345" || ledger.absences[0].PlanID != "plan_1" {
		t.Fatalf("absences %+v", ledger.absences)
	}
}

// A deploy that never started its effects is cancelled, and one whose
// effects started but named nothing refuses the deletion.
func TestFrontendReaperCancelsWhatNeverStarted(t *testing.T) {
	ledger := &fakeLedger{held: []cloudinfra.HeldFrontend{held("web", "plan_1", "", "pending")}}
	fr, _ := newFrontendReaper(t, ledger)
	got, err := prepareAndRun(t, fr)
	if err != nil || len(got.Removes) != 0 || !slices.Equal(ledger.cancelled, []string{"plan_1"}) {
		t.Fatalf("never started: %v, removes %v, cancelled %v", err, got.Removes, ledger.cancelled)
	}

	ledger = &fakeLedger{held: []cloudinfra.HeldFrontend{held("web", "plan_1", "", "pending")}, started: map[string]bool{"plan_1": true}}
	fr, _ = newFrontendReaper(t, ledger)
	if _, err = prepareAndRun(t, fr); !errors.Is(err, projects.ErrFrontendsHeld) {
		t.Fatalf("started but unnamed: %v", err)
	}
}

// Keeping a frontend while its environment goes would leave it counted
// with nothing to release it, so it's refused before anything runs.
func TestFrontendReaperRefusesKeepingAFrontend(t *testing.T) {
	ledger := &fakeLedger{
		held:     []cloudinfra.HeldFrontend{held("web", "plan_1", "prj_1", "active")},
		bindings: []*planning.Binding{{ID: "bnd_web", EnvironmentID: "env_production", Name: "web", Provider: "vercel", ExternalID: "prj_1"}},
	}
	fr, api := newFrontendReaper(t, ledger)
	_, err := fr.PrepareDelete(t.Context(), "org", "proj_abc12345", "env_production", projects.DeleteEnvironmentOptions{Keep: []string{"bnd_web"}})
	if !errors.Is(err, projects.ErrFrontendsHeld) || len(api.callLog()) != 0 {
		t.Fatalf("keeping a frontend: %v, calls %v", err, api.callLog())
	}
}

// Nothing held is nothing to do.
func TestFrontendReaperWithNothingHeld(t *testing.T) {
	fr, _ := newFrontendReaper(t, &fakeLedger{})
	got, err := fr.PrepareDelete(t.Context(), "org", "proj_abc12345", "", projects.DeleteEnvironmentOptions{})
	if err != nil || got.Run != nil || len(got.Removes) != 0 {
		t.Fatalf("nothing held: %+v %v", got, err)
	}
}

// A fallback record proves the environment's own project is Loomplane's,
// so an older deploy that never named it still has it removed.
func TestFrontendReaperRemovesAFallbackProjectItsRecordProves(t *testing.T) {
	ledger := &fakeLedger{
		held:    []cloudinfra.HeldFrontend{held("app", "plan_1", "", "accepted")},
		records: []*ProviderEnvironment{{OrgID: "org", EnvironmentID: "env_production", Provider: "vercel", ConnectionID: "conn", Scope: "app", Mode: ModeFallback}},
	}
	fr, api := newFrontendReaper(t, ledger)
	api.projects["app-production-abc12345"] = "prj_3"
	got, err := prepareAndRun(t, fr)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Removes) != 1 || !api.calledExactly("DELETE /v9/projects/prj_3") {
		t.Fatalf("removes %v, calls %v", got.Removes, api.callLog())
	}
	if len(ledger.absences) != 1 || ledger.absences[0].ExternalID != "prj_3" {
		t.Fatalf("absences %+v", ledger.absences)
	}
}

// A native deploy that failed before recording its environment claimed the
// shared project first; the environment's role picks its environment there,
// as the deploy did, and that's what goes.
func TestFrontendReaperRemovesANativeEnvironmentItsRecordProves(t *testing.T) {
	ledger := &fakeLedger{
		held:    []cloudinfra.HeldFrontend{held("website", "plan_2", "", "accepted")},
		records: []*ProviderEnvironment{{OrgID: "org", EnvironmentID: "env_production", Provider: "vercel", ConnectionID: "conn", Scope: "website", Mode: ModeNative, ProjectRef: "prj_7"}},
	}
	fr, api := newFrontendReaper(t, ledger)
	store := &roleStore{fakePEStore: newFakePEStore(), kind: "production", name: "production", first: true}
	fr.Executors["vercel"].(*Vercel).Environments = &NativeEnvironments{Store: store, Notify: &countingNotifier{}}
	api.projects["website-abc12345"] = "prj_7"
	got, err := prepareAndRun(t, fr)
	if err != nil {
		t.Fatal(err)
	}
	want := planning.VercelRef("prj_7", "production")
	if len(got.Removes) != 1 || !strings.Contains(got.Removes[0], want) {
		t.Fatalf("removes %v, want %s", got.Removes, want)
	}
	if len(ledger.absences) != 1 || ledger.absences[0].ExternalID != want {
		t.Fatalf("absences %+v", ledger.absences)
	}
	// Nothing else uses the project the deploy claimed, so it goes too.
	if len(api.projects) != 0 {
		t.Fatalf("Vercel still has %v", api.projects)
	}
}

// A frontend still deploying refuses the deletion with nothing touched.
func TestFrontendReaperRefusesWhileDeploying(t *testing.T) {
	h := held("website", "plan_2", "prj_7", "accepted")
	h.Deploying = true
	ledger := &fakeLedger{held: []cloudinfra.HeldFrontend{h}}
	fr, api := newFrontendReaper(t, ledger)
	_, err := fr.PrepareDelete(t.Context(), "org", "proj_abc12345", "", projects.DeleteEnvironmentOptions{})
	if !errors.Is(err, projects.ErrFrontendsHeld) || !strings.Contains(err.Error(), "still deploying") || len(api.callLog()) != 0 {
		t.Fatalf("while deploying: %v, calls %v", err, api.callLog())
	}
}

// A project Vercel has under the shared name that no environment claims
// blocks, while one other environments claim says nothing of this one.
func TestFrontendReaperSharedProjectNeedsAClaim(t *testing.T) {
	ledger := &fakeLedger{held: []cloudinfra.HeldFrontend{held("web", "plan_1", "", "accepted")}}
	fr, api := newFrontendReaper(t, ledger)
	store := &roleStore{fakePEStore: newFakePEStore(), kind: "production", name: "production", first: true}
	fr.Executors["vercel"].(*Vercel).Environments = &NativeEnvironments{Store: store, Notify: &countingNotifier{}}
	api.projects["web-abc12345"] = "prj_9"
	if _, err := prepareAndRun(t, fr); !errors.Is(err, projects.ErrFrontendsHeld) || !strings.Contains(err.Error(), "no environment records using") {
		t.Fatalf("an unclaimed shared project: %v", err)
	}
	store.rows["org|env_staging|vercel|conn|web"] = &ProviderEnvironment{OrgID: "org", EnvironmentID: "env_staging", Provider: "vercel", ConnectionID: "conn", Scope: "web", Mode: ModeNative, ProjectRef: "prj_9"}
	if _, err := prepareAndRun(t, fr); err != nil || len(ledger.absences) != 1 || api.called("DELETE ") {
		t.Fatalf("a shared project another environment claims: %v, absences %v, calls %v", err, ledger.absences, api.callLog())
	}
}
