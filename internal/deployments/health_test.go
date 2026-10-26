package deployments

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/metrics"
	"github.com/xraph/loomplane/internal/clouds/render"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/releasehealth"
)

type healthBindings map[string]*planning.Binding // by org + "/" + id

func (m healthBindings) GetBinding(_ context.Context, orgID, id string) (*planning.Binding, error) {
	if b, ok := m[orgID+"/"+id]; ok {
		return b, nil
	}
	return nil, planning.ErrNotFound
}

type healthCredentials struct{}

func (healthCredentials) Credential(context.Context, string, string) (*connections.Connection, []byte, error) {
	return &connections.Connection{}, []byte("secret"), nil
}

type fakeMeter struct {
	got      metrics.Window
	revision string
	m        *metrics.Measurement
	err      error
}

func (f *fakeMeter) MeasureRevision(_ context.Context, _ *connections.Connection, _ []byte, _ *planning.Binding, revision string, w metrics.Window) (*metrics.Measurement, error) {
	f.got, f.revision = w, revision
	return f.m, f.err
}

var healthStart = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)

func healthWindow() releasehealth.Window {
	return releasehealth.Window{Start: healthStart.Add(30 * time.Second), End: healthStart.Add(10*time.Minute + 10*time.Second)}
}

func healthReader(provider string, meter RevisionMeter) *HealthReader {
	meters := map[string]RevisionMeter{}
	if meter != nil {
		meters[provider] = meter
	}
	return &HealthReader{
		Bindings:    healthBindings{"org-a/b1": {ID: "b1", OrgID: "org-a", Name: "web", Provider: provider, ConnectionID: "c1"}},
		Credentials: healthCredentials{},
		Meters:      meters,
	}
}

func counted() *metrics.Measurement {
	return &metrics.Measurement{
		Requests:      []metrics.Point{{T: healthStart, V: 40}, {T: healthStart.Add(time.Minute), V: 60}},
		Errors:        []metrics.Point{{T: healthStart.Add(time.Minute), V: 2}},
		Latency:       120,
		Statistic:     metrics.P95,
		ErrorsCounted: true,
	}
}

func TestHealthReaderSumsTheWindow(t *testing.T) {
	h, err := healthReader("ecs", &fakeMeter{m: counted()}).Health(context.Background(), "org-a", "b1", "rev-2", healthWindow())
	if err != nil {
		t.Fatal(err)
	}
	want := releasehealth.Health{Requests: 100, Errors: 2, LatencyMS: 120, Statistic: "p95", Available: true}
	if h != want {
		t.Fatalf("got %+v, want %+v", h, want)
	}
}

func TestHealthReaderPassesTheRevisionAndAShortWindow(t *testing.T) {
	meter := &fakeMeter{m: counted()}
	if _, err := healthReader("gcp", meter).Health(context.Background(), "org-a", "b1", "rev-2", healthWindow()); err != nil {
		t.Fatal(err)
	}
	want := metrics.Window{Start: healthStart, End: healthStart.Add(10 * time.Minute), Step: time.Minute}
	if meter.revision != "rev-2" || meter.got != want {
		t.Fatalf("meter saw %q and %+v, want rev-2 and %+v", meter.revision, meter.got, want)
	}
}

func TestHealthReaderUnavailableWhenTheCloudCantSay(t *testing.T) {
	h, err := healthReader("gcp", &fakeMeter{err: metrics.Unavailablef("grant X")}).Health(context.Background(), "org-a", "b1", "", healthWindow())
	if err != nil {
		t.Fatal(err)
	}
	if h.Available || h.Reason != "grant X" {
		t.Fatalf("got %+v", h)
	}
}

func TestHealthReaderReturnsOtherMeterErrors(t *testing.T) {
	boom := errors.New("boom")
	if _, err := healthReader("gcp", &fakeMeter{err: boom}).Health(context.Background(), "org-a", "b1", "", healthWindow()); !errors.Is(err, boom) {
		t.Fatalf("got %v, want boom", err)
	}
}

func TestHealthReaderUnavailableWithoutErrorCounts(t *testing.T) {
	m := counted()
	m.ErrorsCounted, m.Errors = false, nil
	h, err := healthReader("render", &fakeMeter{m: m}).Health(context.Background(), "org-a", "b1", "", healthWindow())
	if err != nil {
		t.Fatal(err)
	}
	if h.Available || !strings.Contains(h.Reason, "5xx") {
		t.Fatalf("got %+v", h)
	}
}

func TestHealthReaderUnavailableWithoutAMeasurement(t *testing.T) {
	h, err := healthReader("gcp", &fakeMeter{}).Health(context.Background(), "org-a", "b1", "", healthWindow())
	if err != nil {
		t.Fatal(err)
	}
	if h.Available || h.Reason == "" {
		t.Fatalf("got %+v", h)
	}
}

func TestHealthReaderNoMeterForProvider(t *testing.T) {
	h, err := healthReader("kubernetes", nil).Health(context.Background(), "org-a", "b1", "", healthWindow())
	if err != nil {
		t.Fatal(err)
	}
	want := "Services on your own clusters report to your own monitoring, which Loomplane doesn't read."
	if h.Available || h.Reason != want {
		t.Fatalf("got %+v", h)
	}
}

func TestHealthReaderStaysInTheOrganization(t *testing.T) {
	_, err := healthReader("ecs", &fakeMeter{m: counted()}).Health(context.Background(), "org-b", "b1", "", healthWindow())
	if !errors.Is(err, planning.ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestHealthReaderRenderHasNoRevisions(t *testing.T) {
	// The real Render meter refuses a revision before it touches the client.
	r := healthReader("render", &Render{})
	h, err := r.Health(context.Background(), "org-a", "b1", "x", healthWindow())
	if err != nil {
		t.Fatal(err)
	}
	if h.Available || !strings.Contains(h.Reason, "one release at a time") {
		t.Fatalf("got %+v", h)
	}
	// An empty revision reaches the service read (here a datastore, which
	// reports nothing), so it is measured rather than refused for being a revision.
	r.Bindings = healthBindings{"org-a/b1": {ID: "b1", OrgID: "org-a", Name: "db", Provider: "render", ConnectionID: "c1", ExternalID: render.Postgres + "/dpg-abc"}}
	h, err = r.Health(context.Background(), "org-a", "b1", "", healthWindow())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.Reason, "one release at a time") {
		t.Fatalf("an empty revision was refused as one: %+v", h)
	}
}
