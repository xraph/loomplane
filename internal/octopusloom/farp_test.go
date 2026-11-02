package octopusloom

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/planning"
)

const billingRegisterBody = `{"instance":{"id":"lp-svc-billing","service_name":"billing","address":"lp-inst-01h455vb4pex5vsknk084sn02q","port":8080,"metadata":{"farp.openapi.path":"/openapi.json"}},"manifest":null}`

var (
	t0 = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	t1 = t0.Add(30 * time.Second)
)

// farpService is a service binding that feeds the fixture's gateway "edge".
func farpService(t *testing.T, name string, port int) *planning.Binding {
	t.Helper()
	return serviceBinding(t, name, "conn_a", planning.ServiceConfig{Port: port, Farp: &planning.FarpConfig{
		Gateway: "edge", Port: port, Metadata: map[string]string{"farp.openapi.path": "/openapi.json"},
	}})
}

// farpFixture is the sync fixture with billing (8080) feeding FARP, Octopus
// already holding the gateway's FARP hostnames, and the clock at t0.
func farpFixture(t *testing.T, services ...*planning.Binding) *syncFixture {
	t.Helper()
	f := newSyncFixture(t)
	f.bindings = append(f.bindings[:1:1], services...)
	f.octo.binding = []string{"api.acme.dev"}
	f.at(t0)
	return f
}

func (f *syncFixture) at(now time.Time) { f.syncer.Now = func() time.Time { return now } }

// farpCalls are the /_farp/ requests since the last reset, as "METHOD /path".
func (f *fakeOctopus) farpCalls() []string {
	var out []string
	for _, r := range f.log() {
		if strings.HasPrefix(r.Path, "/_farp/") {
			out = append(out, r.Method+" "+r.Path)
		}
	}
	return out
}

func (f *syncFixture) mustSync() SyncResult {
	f.t.Helper()
	res, err := f.sync()
	if err != nil {
		f.t.Fatalf("sync: %v (%+v)", err, res)
	}
	if res.State != SyncApplied {
		f.t.Fatalf("result = %+v", res)
	}
	return res
}

func (f *syncFixture) registered(service string) Registered {
	f.t.Helper()
	r, ok := f.store.saved().State.Registrations[service]
	if !ok {
		f.t.Fatalf("no registration for %s in %+v", service, f.store.saved().State.Registrations)
	}
	return r
}

func TestRegistrationsRegisterThenHeartbeat(t *testing.T) {
	f := farpFixture(t, farpService(t, "billing", 8080))

	f.mustSync()
	if got, want := f.octo.farpCalls(), []string{"POST /_farp/v1/register"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first sync calls = %v, want %v", got, want)
	}
	if got := strings.TrimSpace(f.octo.body("POST /_farp/v1/register")); got != billingRegisterBody {
		t.Fatalf("register body =\n%s\nwant\n%s", got, billingRegisterBody)
	}
	want := Registered{InstanceID: "lp-svc-billing", Revision: "rev-billing", RegisteredAt: t0, HeartbeatAt: t0}
	if got := f.registered("billing"); got != want {
		t.Fatalf("registered = %+v, want %+v", got, want)
	}

	f.octo.reset()
	f.at(t1)
	f.mustSync()
	if got, want := f.octo.farpCalls(), []string{"PUT /_farp/v1/heartbeat/lp-svc-billing"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second sync calls = %v, want %v", got, want)
	}
	if got := strings.TrimSpace(f.octo.body("PUT /_farp/v1/heartbeat/lp-svc-billing")); got != `{"status":"healthy"}` {
		t.Fatalf("heartbeat body = %s", got)
	}
	want = Registered{InstanceID: "lp-svc-billing", Revision: "rev-billing", RegisteredAt: t0, HeartbeatAt: t1}
	if got := f.registered("billing"); got != want {
		t.Fatalf("registered = %+v, want %+v", got, want)
	}
}

func TestHeartbeat404ReRegisters(t *testing.T) {
	f := farpFixture(t, farpService(t, "billing", 8080))
	f.mustSync()

	// Octopus no longer knows the instance, as after losing its memory.
	f.octo.mu.Lock()
	delete(f.octo.instances, "lp-svc-billing")
	f.octo.mu.Unlock()
	f.octo.reset()
	f.at(t1)

	f.mustSync()
	want := []string{"PUT /_farp/v1/heartbeat/lp-svc-billing", "POST /_farp/v1/register"}
	if got := f.octo.farpCalls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if got := f.registered("billing"); got.RegisteredAt != t1 || got.HeartbeatAt != t1 || got.Error != "" {
		t.Fatalf("registered = %+v", got)
	}
}

func TestNewReleaseReRegisters(t *testing.T) {
	f := farpFixture(t, farpService(t, "billing", 8080))
	f.mustSync()

	f.bindings[1].Revision = "rev-billing-2"
	f.octo.reset()
	f.at(t1)
	f.mustSync()

	// The heartbeat would pass, but a new release replaces the routes.
	if got, want := f.octo.farpCalls(), []string{"POST /_farp/v1/register"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	want := Registered{InstanceID: "lp-svc-billing", Revision: "rev-billing-2", RegisteredAt: t1, HeartbeatAt: t1}
	if got := f.registered("billing"); got != want {
		t.Fatalf("registered = %+v, want %+v", got, want)
	}
}

func TestOctopusRestartReRegistersEveryService(t *testing.T) {
	f := farpFixture(t, farpService(t, "billing", 8080), farpService(t, "api", 3000))
	f.mustSync()

	f.octo.startTime = "2026-10-21 11:00:00"
	f.octo.reset()
	f.at(t1)
	res := f.mustSync()

	if !res.Restarted {
		t.Fatalf("result = %+v", res)
	}
	want := []string{"POST /_farp/v1/register", "POST /_farp/v1/register"}
	if got := f.octo.farpCalls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if got := f.registered("api"); got.RegisteredAt != t1 {
		t.Fatalf("api registered = %+v", got)
	}
}

func TestOptedOutServiceIsDeregistered(t *testing.T) {
	f := farpFixture(t, farpService(t, "billing", 8080))
	f.mustSync()

	f.bindings[1] = serviceBinding(t, "billing", "conn_a", planning.ServiceConfig{Port: 8080})
	f.octo.reset()
	f.mustSync()

	if got, want := f.octo.farpCalls(), []string{"DELETE /_farp/v1/deregister/lp-svc-billing"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if regs := f.store.saved().State.Registrations; len(regs) != 0 {
		t.Fatalf("registrations = %+v, want none", regs)
	}
}

func TestRemovedServiceIsDeregistered(t *testing.T) {
	f := farpFixture(t, farpService(t, "billing", 8080))
	f.mustSync()

	f.bindings = f.bindings[:1]
	f.octo.reset()
	f.mustSync()

	if got, want := f.octo.farpCalls(), []string{"DELETE /_farp/v1/deregister/lp-svc-billing"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if regs := f.store.saved().State.Registrations; len(regs) != 0 {
		t.Fatalf("registrations = %+v, want none", regs)
	}
}

func TestDeregisterOfAnUnknownInstanceIsDone(t *testing.T) {
	f := farpFixture(t, farpService(t, "billing", 8080))
	f.mustSync()

	f.octo.mu.Lock()
	delete(f.octo.instances, "lp-svc-billing")
	f.octo.mu.Unlock()
	f.bindings = f.bindings[:1]
	f.octo.reset()
	f.mustSync()

	if regs := f.store.saved().State.Registrations; len(regs) != 0 {
		t.Fatalf("registrations = %+v, want none", regs)
	}
}

func TestFailedDeregisterIsKeptAndRetried(t *testing.T) {
	f := farpFixture(t, farpService(t, "billing", 8080))
	f.mustSync()

	f.bindings = f.bindings[:1]
	f.octo.farpStatus["DELETE lp-svc-billing"] = http.StatusInternalServerError
	f.mustSync()
	got := f.registered("billing")
	if want := "Octopus refused to deregister billing: 500"; got.Error != want {
		t.Fatalf("Error = %q, want %q", got.Error, want)
	}

	delete(f.octo.farpStatus, "DELETE lp-svc-billing")
	f.octo.reset()
	f.mustSync()
	if got, want := f.octo.farpCalls(), []string{"DELETE /_farp/v1/deregister/lp-svc-billing"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if regs := f.store.saved().State.Registrations; len(regs) != 0 {
		t.Fatalf("registrations = %+v, want none", regs)
	}
}

func TestKubernetesGatewaysNeverPushRegister(t *testing.T) {
	f := farpFixture(t, farpService(t, "billing", 8080))
	f.store.gateway.Provider = ProviderKubernetes
	f.store.gateway.Namespace = "shop"

	f.mustSync()
	f.at(t1)
	f.mustSync()

	if calls := f.octo.farpCalls(); len(calls) != 0 {
		t.Fatalf("a kubernetes gateway made FARP calls: %v", calls)
	}
	if regs := f.store.saved().State.Registrations; len(regs) != 0 {
		t.Fatalf("registrations = %+v", regs)
	}
}

func TestRegistrationErrorIsPerService(t *testing.T) {
	f := farpFixture(t, farpService(t, "billing", 8080), farpService(t, "api", 3000))
	f.octo.farpStatus["POST lp-svc-billing"] = http.StatusInternalServerError

	// A refused registration is visible on the service. It doesn't make the
	// sync fail: the gateway's routes and upstreams are what the result is.
	res := f.mustSync()
	if res.Message != "" {
		t.Fatalf("result = %+v", res)
	}
	if got := f.registered("api"); got.Error != "" || got.RegisteredAt != t0 || got.InstanceID != "lp-svc-api" {
		t.Fatalf("api registered = %+v", got)
	}
	if _, ok := f.octo.instances["lp-svc-api"]; !ok {
		t.Fatal("api isn't registered in Octopus")
	}
	billing := f.registered("billing")
	if want := "Octopus refused billing's FARP registration: 500"; billing.Error != want {
		t.Fatalf("billing Error = %q, want %q", billing.Error, want)
	}
	if f.store.saved().State.Error != "" {
		t.Fatalf("State.Error = %q", f.store.saved().State.Error)
	}

	// Octopus takes it on the next sync, which clears the error.
	delete(f.octo.farpStatus, "POST lp-svc-billing")
	f.octo.reset()
	f.at(t1)
	f.mustSync()
	want := []string{"POST /_farp/v1/register", "PUT /_farp/v1/heartbeat/lp-svc-api"}
	if got := f.octo.farpCalls(); !reflect.DeepEqual(got, want) && !reflect.DeepEqual(got, []string{want[1], want[0]}) {
		t.Fatalf("calls = %v", got)
	}
	if got := f.registered("billing"); got.Error != "" || got.RegisteredAt != t1 {
		t.Fatalf("billing registered = %+v", got)
	}
}

func TestFailedHeartbeatIsRecordedAndRetried(t *testing.T) {
	f := farpFixture(t, farpService(t, "billing", 8080))
	f.mustSync()

	f.octo.farpStatus["PUT lp-svc-billing"] = http.StatusInternalServerError
	f.at(t1)
	f.mustSync()
	got := f.registered("billing")
	if want := "Octopus refused billing's FARP heartbeat: 500"; got.Error != want || got.RegisteredAt != t0 || got.HeartbeatAt != t0 {
		t.Fatalf("registered = %+v", got)
	}

	delete(f.octo.farpStatus, "PUT lp-svc-billing")
	f.octo.reset()
	f.at(t1.Add(30 * time.Second))
	f.mustSync()
	if got, want := f.octo.farpCalls(), []string{"PUT /_farp/v1/heartbeat/lp-svc-billing"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if got := f.registered("billing"); got.Error != "" || got.HeartbeatAt != t1.Add(30*time.Second) {
		t.Fatalf("registered = %+v", got)
	}
}

func TestRegistrationWhenOctopusStopsAnsweringIsPending(t *testing.T) {
	f := farpFixture(t, farpService(t, "billing", 8080), farpService(t, "api", 3000))
	f.octo.farpStatus["POST lp-svc-billing"] = http.StatusServiceUnavailable

	res, err := f.sync()
	if !errors.Is(err, ErrUnavailable) || res.State != SyncPending {
		t.Fatalf("result = %+v, err = %v", res, err)
	}
	if f.store.saved().AppliedGeneration != 2 {
		t.Fatal("generation recorded as applied")
	}
}

func TestFarpCallsSendTheFarpToken(t *testing.T) {
	f := farpFixture(t, farpService(t, "billing", 8080), farpService(t, "api", 3000))
	f.octo.failBody = `{"error":"` + fakeFarpToken + `"}`
	f.mustSync() // registers both
	f.at(t1)
	f.mustSync() // heartbeats both
	f.bindings = f.bindings[:1]
	f.mustSync() // deregisters both

	farpRequests := 0
	for _, r := range f.octo.log() {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("%s %s sent Authorization", r.Method, r.Path)
		}
		if !strings.HasPrefix(r.Path, "/_farp/") {
			if r.Header.Get("X-Octopus-Farp-Token") != "" {
				t.Errorf("%s %s sent the FARP token", r.Method, r.Path)
			}
			continue
		}
		farpRequests++
		if got := r.Header.Get("X-Octopus-Farp-Token"); got != fakeFarpToken {
			t.Errorf("%s %s sent FARP token %q", r.Method, r.Path, got)
		}
		if r.Header.Get("X-Octopus-Admin-Token") != "" {
			t.Errorf("%s %s sent the admin token", r.Method, r.Path)
		}
	}
	if farpRequests != 6 {
		t.Fatalf("saw %d FARP requests, want 6: %v", farpRequests, f.octo.farpCalls())
	}
}

func TestFarpErrorsNeverCarryTheToken(t *testing.T) {
	f := farpFixture(t, farpService(t, "billing", 8080))
	f.octo.farpStatus["POST lp-svc-billing"] = http.StatusInternalServerError
	f.octo.failBody = fakeFarpToken
	f.mustSync()

	g := f.store.saved()
	if raw := g.State.Registrations["billing"].Error + g.State.Error; strings.Contains(raw, fakeFarpToken) {
		t.Fatalf("a registration error carries the FARP token: %q", raw)
	}
}

func TestAdminFarpCalls(t *testing.T) {
	f := newFakeOctopus(t)
	a := NewAdmin(f.channel(), []byte(fakeAdminToken), []byte(fakeFarpToken))
	ctx := t.Context()

	if err := a.Heartbeat(ctx, "lp-svc-billing"); !errors.Is(err, ErrUnknownInstance) {
		t.Fatalf("Heartbeat of an unknown instance = %v, want ErrUnknownInstance", err)
	}
	if err := a.Deregister(ctx, "lp-svc-billing"); err != nil {
		t.Fatalf("Deregister of an unknown instance = %v", err)
	}
	reg := Registration{InstanceID: "lp-svc-billing", Service: "billing", Address: testContainer, Port: 8080}
	if err := a.Register(ctx, reg); err != nil {
		t.Fatal(err)
	}
	// No metadata is sent as an empty object, not null.
	if got := f.body("POST /_farp/v1/register"); !strings.Contains(got, `"metadata":{}`) {
		t.Fatalf("register body = %s", got)
	}
	if err := a.Heartbeat(ctx, "lp-svc-billing"); err != nil {
		t.Fatal(err)
	}

	f.farpStatus["POST lp-svc-billing"] = http.StatusUnauthorized
	var status *StatusError
	if err := a.Register(ctx, reg); !errors.As(err, &status) || status.Status != http.StatusUnauthorized {
		t.Fatalf("Register = %v, want a 401 StatusError", err)
	}
	f.farpStatus["PUT lp-svc-billing"] = http.StatusBadGateway
	if err := a.Heartbeat(ctx, "lp-svc-billing"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Heartbeat answered 502 = %v, want ErrUnavailable", err)
	}
}
