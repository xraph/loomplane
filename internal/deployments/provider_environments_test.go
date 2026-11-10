package deployments

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
)

type fakePEStore struct {
	ProviderEnvironmentStore
	rows map[string]*ProviderEnvironment
}

func newFakePEStore() *fakePEStore {
	return &fakePEStore{rows: map[string]*ProviderEnvironment{}}
}

func peKey(pe *ProviderEnvironment) string {
	return strings.Join([]string{pe.OrgID, pe.EnvironmentID, pe.Provider, pe.ConnectionID, pe.Scope}, "|")
}

func (f *fakePEStore) SaveProviderEnvironment(_ context.Context, pe *ProviderEnvironment) error {
	cp := *pe
	if old, ok := f.rows[peKey(pe)]; ok {
		cp.CreatedAt, cp.NotifiedAt = old.CreatedAt, old.NotifiedAt
	}
	f.rows[peKey(pe)] = &cp
	return nil
}

func (f *fakePEStore) DeleteProviderEnvironment(_ context.Context, pe *ProviderEnvironment) error {
	delete(f.rows, peKey(pe))
	return nil
}

func (f *fakePEStore) ProviderEnvironment(_ context.Context, org, env, provider, conn, scope string) (*ProviderEnvironment, error) {
	return f.rows[strings.Join([]string{org, env, provider, conn, scope}, "|")], nil
}

func (f *fakePEStore) ClaimFallbackNotice(_ context.Context, pe *ProviderEnvironment, at time.Time) (bool, error) {
	row := f.rows[peKey(pe)]
	if row == nil || row.NotifiedAt != nil {
		return false, nil
	}
	row.NotifiedAt = &at
	return true, nil
}

type countingNotifier struct{ calls int }

func (c *countingNotifier) NotifyFallback(context.Context, *ProviderEnvironment, string) error {
	c.calls++
	return nil
}

func peJob(logs *[]string) *Job {
	return &Job{
		Deployment: &Deployment{OrgID: "org1", ProjectID: "prj1", EnvironmentID: "env1", Environment: "staging"},
		Connection: &connections.Connection{ID: "conn1"},
		Progress:   func(_, m string) { *logs = append(*logs, m) },
	}
}

func TestTargetFor(t *testing.T) {
	cases := []struct {
		kind, name string
		first      bool
		want       EnvTarget
	}{
		{"production", "prod", true, EnvTarget{Builtin: "production"}},
		{"production", "prod2", false, EnvTarget{Name: "prod2"}},
		{"preview", "preview", true, EnvTarget{Builtin: "preview"}},
		{"staging", "staging", true, EnvTarget{Name: "staging"}},
		{"development", "development", true, EnvTarget{Name: "development"}},
	}
	for _, c := range cases {
		if got := targetFor(c.kind, c.name, c.first); got != c.want {
			t.Errorf("targetFor(%q,%q,%v) = %+v, want %+v", c.kind, c.name, c.first, got, c.want)
		}
	}
}

func TestFallBackLogsAndNotifiesOnce(t *testing.T) {
	var logs []string
	notify := &countingNotifier{}
	n := &NativeEnvironments{Store: newFakePEStore(), Notify: notify}
	job := peJob(&logs)
	for i := 0; i < 2; i++ {
		if err := n.FallBack(context.Background(), job, "vercel", "web", "plan has no custom environments", false); err != nil {
			t.Fatal(err)
		}
	}
	if notify.calls != 1 {
		t.Fatalf("notify calls = %d, want 1", notify.calls)
	}
	if len(logs) != 2 {
		t.Fatalf("log lines = %d, want 2", len(logs))
	}
	want := "Vercel couldn't use a native staging environment (plan has no custom environments), so it deploys to a separate project"
	for _, l := range logs {
		if l != want {
			t.Errorf("log = %q, want %q", l, want)
		}
	}
}

func TestPermanentFallbackDoesNotNotify(t *testing.T) {
	var logs []string
	notify := &countingNotifier{}
	store := newFakePEStore()
	n := &NativeEnvironments{Store: store, Notify: notify}
	if err := n.FallBack(context.Background(), peJob(&logs), "render", "", "free workspace", true); err != nil {
		t.Fatal(err)
	}
	if notify.calls != 0 {
		t.Fatalf("notify calls = %d, want 0", notify.calls)
	}
	pe, _ := n.Record(context.Background(), peJob(&logs), "render", "")
	if pe == nil || !pe.Permanent || pe.Mode != ModeFallback {
		t.Fatalf("record = %+v", pe)
	}
}

func TestFallBackWithoutProgressOrNotifier(t *testing.T) {
	job := peJob(new([]string))
	job.Progress = nil
	n := &NativeEnvironments{Store: newFakePEStore()}
	if err := n.FallBack(context.Background(), job, "vercel", "web", "x", false); err != nil {
		t.Fatal(err)
	}
}

func TestNativeRecordsRefs(t *testing.T) {
	var logs []string
	n := &NativeEnvironments{Store: newFakePEStore()}
	job := peJob(&logs)
	if err := n.Native(context.Background(), job, "vercel", "web", "prj_1", "env_9"); err != nil {
		t.Fatal(err)
	}
	pe, err := n.Record(context.Background(), job, "vercel", "web")
	if err != nil || pe == nil {
		t.Fatalf("record = %+v, %v", pe, err)
	}
	if pe.Mode != ModeNative || pe.ProjectRef != "prj_1" || pe.EnvironmentRef != "env_9" {
		t.Fatalf("record = %+v", pe)
	}
}
