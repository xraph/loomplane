package usage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/metrics"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

var (
	viewer = security.Principal{OrgID: "org", UserID: "viewer"}
	now    = time.Date(2026, 10, 24, 14, 10, 0, 0, time.UTC)
)

type fakeConnections struct {
	mu      sync.Mutex
	list    []*connections.Connection
	reveals int
}

func (f *fakeConnections) All(_ context.Context, org string) ([]*connections.Connection, error) {
	if org != "org" {
		return nil, nil
	}
	return f.list, nil
}

func (f *fakeConnections) Credential(_ context.Context, org, id string) (*connections.Connection, []byte, error) {
	f.mu.Lock()
	f.reveals++
	f.mu.Unlock()
	for _, c := range f.list {
		if c.ID == id && c.OrgID == org {
			if c.Status != connections.StatusReady {
				return nil, nil, connections.ErrNotReady
			}
			return c, []byte("secret-" + id), nil
		}
	}
	return nil, nil, connections.ErrNotFound
}

type fakeBindings []*planning.Binding

func (f fakeBindings) OrgServiceBindings(_ context.Context, org string) ([]*planning.Binding, error) {
	out := []*planning.Binding{}
	for _, b := range f {
		if b.OrgID == org {
			out = append(out, b)
		}
	}
	return out, nil
}

type fakeAuthz struct {
	denied      security.Permission
	hidden      string // a project the principal can't read
	unavailable bool
}

func (a fakeAuthz) Require(_ context.Context, _ security.Principal, perm security.Permission, resource string) error {
	if a.unavailable && perm == security.ProjectRead {
		return errors.New("authorization unavailable")
	}
	if perm == a.denied || perm == security.ProjectRead && resource == a.hidden {
		return security.ErrForbidden
	}
	return nil
}

type spendFunc func(*connections.Connection, []byte) (*metrics.Spend, error)

type fakeSpender struct {
	mu    sync.Mutex
	calls int
	fn    spendFunc
}

func (f *fakeSpender) Spend(_ context.Context, c *connections.Connection, cred []byte, _ time.Time) (*metrics.Spend, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.fn(c, cred)
}

type measureFunc func(*planning.Binding, metrics.Window) (*metrics.Measurement, error)

type fakeMeter struct {
	mu    sync.Mutex
	calls int
	fn    measureFunc
}

func (f *fakeMeter) Measure(_ context.Context, _ *connections.Connection, _ []byte, b *planning.Binding, w metrics.Window) (*metrics.Measurement, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.fn(b, w)
}

func conn(id, cloud, status string) *connections.Connection {
	return &connections.Connection{ID: id, OrgID: "org", Cloud: cloud, Name: id + " account", Status: status, Generation: 1}
}

func service(d Deps) *Service {
	s := NewService(d)
	s.now = func() time.Time { return now }
	return s
}

func day(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }

func TestSpendAddsUpEachCurrencyAndSaysWhyAnAccountIsMissing(t *testing.T) {
	cons := &fakeConnections{list: []*connections.Connection{
		conn("aws", connections.AWS, connections.StatusReady),
		conn("az", connections.Azure, connections.StatusReady),
		conn("eu", connections.GCP, connections.StatusReady),
		conn("gcp", connections.GCP, connections.StatusReady),
		conn("broken", connections.AWS, connections.StatusReady),
		conn("stale", connections.AWS, connections.StatusIncomplete),
		conn("site", connections.Vercel, connections.StatusReady),
		conn("images", connections.Registry, connections.StatusReady),
	}}
	aws := &fakeSpender{fn: func(c *connections.Connection, cred []byte) (*metrics.Spend, error) {
		if string(cred) != "secret-"+c.ID {
			t.Errorf("%s read with %q", c.ID, cred)
		}
		if c.ID == "broken" {
			return nil, errors.New("connection reset")
		}
		return &metrics.Spend{Currency: "USD", Total: 30, Days: []metrics.Point{{T: day(1), V: 10}, {T: day(2), V: 20}}}, nil
	}}
	azure := &fakeSpender{fn: func(*connections.Connection, []byte) (*metrics.Spend, error) {
		return &metrics.Spend{Currency: "USD", Total: 5, Days: []metrics.Point{{T: day(2), V: 5}}}, nil
	}}
	gcp := &fakeSpender{fn: func(c *connections.Connection, _ []byte) (*metrics.Spend, error) {
		if c.ID == "eu" {
			return &metrics.Spend{Currency: "EUR", Total: 7}, nil
		}
		return nil, metrics.Unavailablef("add the billing export")
	}}
	s := service(Deps{Connections: cons, Authz: fakeAuthz{}, Spenders: map[string]SpendReader{
		connections.AWS: aws, connections.Azure: azure, connections.GCP: gcp,
	}})
	rep, err := s.Spend(t.Context(), viewer)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Month.Equal(day(1)) {
		t.Errorf("month %v", rep.Month)
	}
	if len(rep.Totals) != 2 || rep.Totals[0].Currency != "EUR" || rep.Totals[0].Total != 7 ||
		rep.Totals[1].Currency != "USD" || rep.Totals[1].Total != 35 {
		t.Fatalf("totals %+v", rep.Totals)
	}
	usd := rep.Totals[1].Days
	if len(usd) != 31 || usd[0].V != 10 || usd[1].V != 25 || usd[2].V != 0 || rep.Totals[1].Undated != 0 {
		t.Fatalf("USD days %+v, %v undated", usd[:3], rep.Totals[1].Undated)
	}
	// Europe's bill has no daily breakdown: it counts in the total, and
	// isn't drawn on any one day.
	if eur := rep.Totals[0]; eur.Undated != 7 || metrics.Total(eur.Days) != 0 || len(eur.Days) != 31 {
		t.Fatalf("EUR %+v", eur)
	}
	if len(rep.Accounts) != 3 || rep.Accounts[0].ConnectionID != "aws" || rep.Accounts[0].Total != 30 {
		t.Fatalf("accounts %+v", rep.Accounts)
	}
	status := map[string]Source{}
	for _, src := range rep.Sources {
		status[src.ConnectionID] = src
	}
	if _, ok := status["images"]; ok {
		t.Error("an image registry has no bill, so it isn't a source")
	}
	for id, want := range map[string]string{
		"aws": Reporting, "az": Reporting, "eu": Reporting,
		"gcp": Unavailable, "stale": Unavailable, "site": Unavailable, "broken": Failed,
	} {
		if status[id].Status != want {
			t.Errorf("%s is %q, want %q (%s)", id, status[id].Status, want, status[id].Reason)
		}
	}
	if status["gcp"].Reason != "add the billing export" || status["site"].Reason == "" || status["stale"].Reason == "" || status["broken"].Reason == "" {
		t.Errorf("reasons: %+v", status)
	}
}

func TestSpendNeedsCloudAccountReadBeforeAnyCredentialIsRead(t *testing.T) {
	cons := &fakeConnections{list: []*connections.Connection{conn("aws", connections.AWS, connections.StatusReady)}}
	reader := &fakeSpender{fn: func(*connections.Connection, []byte) (*metrics.Spend, error) { return &metrics.Spend{}, nil }}
	s := service(Deps{Connections: cons, Authz: fakeAuthz{denied: security.CloudAccountRead}, Spenders: map[string]SpendReader{connections.AWS: reader}})
	if _, err := s.Spend(t.Context(), viewer); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("err %v", err)
	}
	if cons.reveals != 0 || reader.calls != 0 {
		t.Fatalf("read %d credentials and %d bills without permission", cons.reveals, reader.calls)
	}
}

func TestSpendIsCachedForAnHourAndNotPastACredentialChange(t *testing.T) {
	c := conn("aws", connections.AWS, connections.StatusReady)
	cons := &fakeConnections{list: []*connections.Connection{c}}
	reader := &fakeSpender{fn: func(*connections.Connection, []byte) (*metrics.Spend, error) {
		return &metrics.Spend{Currency: "USD", Total: 1}, nil
	}}
	s := service(Deps{Connections: cons, Authz: fakeAuthz{}, Spenders: map[string]SpendReader{connections.AWS: reader}})
	read := func() {
		t.Helper()
		if _, err := s.Spend(t.Context(), viewer); err != nil {
			t.Fatal(err)
		}
	}
	read()
	read()
	if reader.calls != 1 {
		t.Fatalf("a second read within the hour asked the cloud again (%d calls)", reader.calls)
	}
	c.Generation = 2
	read()
	if reader.calls != 2 {
		t.Fatalf("a replaced credential read the old bill (%d calls)", reader.calls)
	}
	s.now = func() time.Time { return now.Add(61 * time.Minute) }
	read()
	if reader.calls != 3 {
		t.Fatalf("an hour-old bill was served (%d calls)", reader.calls)
	}
}

func TestSpendRetriesAFailureSoonerThanASuccess(t *testing.T) {
	cons := &fakeConnections{list: []*connections.Connection{conn("aws", connections.AWS, connections.StatusReady)}}
	reader := &fakeSpender{fn: func(*connections.Connection, []byte) (*metrics.Spend, error) { return nil, errors.New("throttled") }}
	s := service(Deps{Connections: cons, Authz: fakeAuthz{}, Spenders: map[string]SpendReader{connections.AWS: reader}})
	_, _ = s.Spend(t.Context(), viewer)
	_, _ = s.Spend(t.Context(), viewer)
	if reader.calls != 1 {
		t.Fatalf("a failure was retried at once (%d calls)", reader.calls)
	}
	s.now = func() time.Time { return now.Add(2 * time.Minute) }
	_, _ = s.Spend(t.Context(), viewer)
	if reader.calls != 2 {
		t.Fatalf("a failure was remembered for too long (%d calls)", reader.calls)
	}
}

func binding(id, project, provider, connection string) *planning.Binding {
	return &planning.Binding{ID: id, OrgID: "org", ProjectID: project, EnvironmentID: "env_" + project, Name: id,
		Provider: provider, ConnectionID: connection, Subject: planning.ServiceSubject}
}

func TestTrafficSumsRequestsAndRanksServicesByLatency(t *testing.T) {
	cons := &fakeConnections{list: []*connections.Connection{
		conn("gcp", connections.GCP, connections.StatusReady),
		conn("az", connections.Azure, connections.StatusReady),
	}}
	w := metrics.Week.Window(now)
	run := &fakeMeter{fn: func(b *planning.Binding, win metrics.Window) (*metrics.Measurement, error) {
		if win.Step != w.Step {
			t.Errorf("measured on a %v step", win.Step)
		}
		if win.Start.Equal(w.Start) {
			lat := map[string]float64{"web": 80, "api": 240}[b.ID]
			return &metrics.Measurement{Requests: []metrics.Point{{T: w.Start, V: 10}, {T: w.Start.Add(w.Step), V: 5}}, Latency: lat, Statistic: metrics.P95}, nil
		}
		return &metrics.Measurement{Requests: []metrics.Point{{T: win.Start, V: 3}}, Latency: 100, Statistic: metrics.P95}, nil
	}}
	apps := &fakeMeter{fn: func(_ *planning.Binding, win metrics.Window) (*metrics.Measurement, error) {
		return &metrics.Measurement{Requests: []metrics.Point{{T: win.Start, V: 1}}, Latency: 120, Statistic: metrics.Average}, nil
	}}
	s := service(Deps{Connections: cons, Authz: fakeAuthz{}, Meters: map[string]Meter{"cloud-run": run, "azure-container-apps": apps},
		Bindings: fakeBindings{
			binding("web", "p1", "cloud-run", "gcp"), binding("api", "p2", "cloud-run", "gcp"),
			binding("worker", "p1", "azure-container-apps", "az"),
		}})
	rep, err := s.Traffic(t.Context(), viewer, "7d")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Range != metrics.Week || !rep.Start.Equal(w.Start) || !rep.End.Equal(w.End) || rep.Step != int(w.Step.Seconds()) {
		t.Errorf("window %+v", rep)
	}
	if len(rep.Requests) != w.Buckets() || rep.Requests[0].V != 21 || rep.Requests[1].V != 10 {
		t.Fatalf("requests %+v", rep.Requests[:2])
	}
	if rep.Total != 31 || rep.PreviousTotal != 7 {
		t.Errorf("totals %v, previously %v", rep.Total, rep.PreviousTotal)
	}
	if len(rep.Services) != 3 || rep.Services[0].BindingID != "api" || rep.Services[1].BindingID != "worker" || rep.Services[2].BindingID != "web" {
		t.Fatalf("services by latency %+v", rep.Services)
	}
	if api := rep.Services[0]; api.Latency != 240 || api.PreviousLatency != 100 || api.Requests != 15 || api.PreviousRequests != 3 || api.Statistic != metrics.P95 {
		t.Errorf("api %+v", api)
	}
	if rep.Services[1].Statistic != metrics.Average {
		t.Error("an average was passed off as a p95")
	}
}

func TestTrafficLeavesOutProjectsTheViewerCantRead(t *testing.T) {
	cons := &fakeConnections{list: []*connections.Connection{conn("gcp", connections.GCP, connections.StatusReady)}}
	meter := &fakeMeter{fn: func(*planning.Binding, metrics.Window) (*metrics.Measurement, error) {
		return &metrics.Measurement{}, nil
	}}
	s := service(Deps{Connections: cons, Authz: fakeAuthz{hidden: "secret"}, Meters: map[string]Meter{"cloud-run": meter},
		Bindings: fakeBindings{binding("web", "p1", "cloud-run", "gcp"), binding("vault", "secret", "cloud-run", "gcp")}})
	rep, err := s.Traffic(t.Context(), viewer, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Services) != 1 || rep.Services[0].BindingID != "web" {
		t.Fatalf("services %+v", rep.Services)
	}
	if meter.calls != 2 {
		t.Fatalf("measured a hidden service: %d calls", meter.calls)
	}
}

func TestTrafficRefusesWithoutInfrastructureReadAndDoesntHideAnOutage(t *testing.T) {
	cons := &fakeConnections{list: []*connections.Connection{conn("gcp", connections.GCP, connections.StatusReady)}}
	meter := &fakeMeter{fn: func(*planning.Binding, metrics.Window) (*metrics.Measurement, error) {
		return &metrics.Measurement{}, nil
	}}
	bindings := fakeBindings{binding("web", "p1", "cloud-run", "gcp")}
	s := service(Deps{Connections: cons, Authz: fakeAuthz{denied: security.InfrastructureRead}, Meters: map[string]Meter{"cloud-run": meter}, Bindings: bindings})
	if _, err := s.Traffic(t.Context(), viewer, "7d"); !errors.Is(err, security.ErrForbidden) || meter.calls != 0 || cons.reveals != 0 {
		t.Fatalf("err %v, %d measurements, %d credentials", err, meter.calls, cons.reveals)
	}
	s = service(Deps{Connections: cons, Authz: fakeAuthz{unavailable: true}, Meters: map[string]Meter{"cloud-run": meter}, Bindings: bindings})
	if _, err := s.Traffic(t.Context(), viewer, "7d"); err == nil {
		t.Fatal("an authorization outage read as no services")
	}
}

func TestTrafficSaysWhichServicesCantReportAndWhy(t *testing.T) {
	cons := &fakeConnections{list: []*connections.Connection{
		conn("gcp", connections.GCP, connections.StatusReady),
		conn("site", connections.Vercel, connections.StatusReady),
		conn("stale", connections.AWS, connections.StatusIncomplete),
	}}
	meter := &fakeMeter{fn: func(b *planning.Binding, _ metrics.Window) (*metrics.Measurement, error) {
		switch b.ID {
		case "a", "b":
			return nil, metrics.Unavailablef("grant Monitoring Viewer")
		case "c":
			return nil, errors.New("deadline exceeded")
		}
		return &metrics.Measurement{}, nil
	}}
	s := service(Deps{Connections: cons, Authz: fakeAuthz{}, Meters: map[string]Meter{"cloud-run": meter, "ecs-fargate": meter},
		Title: func(name string) string { return map[string]string{"vercel": "Vercel"}[name] },
		Bindings: fakeBindings{
			binding("a", "p", "cloud-run", "gcp"), binding("b", "p", "cloud-run", "gcp"), binding("c", "p", "cloud-run", "gcp"),
			binding("d", "p", "cloud-run", "gcp"), binding("e", "p", "vercel", "site"), binding("f", "p", "ecs-fargate", "stale"),
		}})
	rep, err := s.Traffic(t.Context(), viewer, "24h")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Services) != 1 || rep.Services[0].BindingID != "d" {
		t.Fatalf("services %+v", rep.Services)
	}
	type key struct{ connection, status, reason string }
	got := map[key][]string{}
	for _, src := range rep.Sources {
		got[key{src.ConnectionID, src.Status, src.Reason}] = src.Services
	}
	if names := got[key{"gcp", Unavailable, "grant Monitoring Viewer"}]; len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Errorf("one reason, said once for both services: %+v", rep.Sources)
	}
	if names := got[key{"gcp", Reporting, ""}]; len(names) != 1 || names[0] != "d" {
		t.Errorf("reporting %+v", rep.Sources)
	}
	var failed, vercel, stale bool
	for _, src := range rep.Sources {
		failed = failed || src.Status == Failed && src.ConnectionID == "gcp" && len(src.Services) == 1 && src.Services[0] == "c"
		vercel = vercel || src.Status == Unavailable && src.ConnectionID == "site" && src.Reason != "" && src.Cloud == connections.Vercel
		stale = stale || src.Status == Unavailable && src.ConnectionID == "stale" && src.Reason != ""
	}
	if !failed || !vercel || !stale {
		t.Errorf("failed %v, vercel %v, stale %v: %+v", failed, vercel, stale, rep.Sources)
	}
}

func TestTrafficRefusesAnUnknownRange(t *testing.T) {
	s := service(Deps{Connections: &fakeConnections{}, Authz: fakeAuthz{}, Bindings: fakeBindings{}})
	if _, err := s.Traffic(t.Context(), viewer, "1y"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err %v", err)
	}
}

func TestTrafficMeasuresEachWindowOnceWhileItsCached(t *testing.T) {
	cons := &fakeConnections{list: []*connections.Connection{conn("gcp", connections.GCP, connections.StatusReady)}}
	meter := &fakeMeter{fn: func(*planning.Binding, metrics.Window) (*metrics.Measurement, error) {
		return &metrics.Measurement{}, nil
	}}
	s := service(Deps{Connections: cons, Authz: fakeAuthz{}, Meters: map[string]Meter{"cloud-run": meter},
		Bindings: fakeBindings{binding("web", "p", "cloud-run", "gcp")}})
	for range 3 {
		if _, err := s.Traffic(t.Context(), viewer, "7d"); err != nil {
			t.Fatal(err)
		}
	}
	if meter.calls != 2 {
		t.Fatalf("%d measurements for one window and the one before, want 2", meter.calls)
	}
	s.now = func() time.Time { return now.Add(6 * time.Minute) }
	if _, err := s.Traffic(t.Context(), viewer, "7d"); err != nil {
		t.Fatal(err)
	}
	if meter.calls != 4 {
		t.Fatalf("%d measurements after five minutes, want 4", meter.calls)
	}
}
