package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

type nativeOutputTestStore struct {
	values       snapshotSecrets
	puts         []string
	failPut      string
	failAfterPut bool
	failDelete   string
}

func (s *nativeOutputTestStore) Put(ctx context.Context, key string, value []byte) error {
	s.puts = append(s.puts, key)
	if key == s.failPut {
		s.failPut = ""
		if s.failAfterPut {
			if err := s.values.Put(ctx, key, value); err != nil {
				return err
			}
		}
		return errors.New("injected resource store failure")
	}
	return s.values.Put(ctx, key, value)
}
func (s *nativeOutputTestStore) Get(ctx context.Context, key string) ([]byte, error) {
	return s.values.Get(ctx, key)
}
func (s *nativeOutputTestStore) Delete(ctx context.Context, key string) error {
	if key == s.failDelete {
		s.failDelete = ""
		return errors.New("injected resource deletion failure")
	}
	return s.values.Delete(ctx, key)
}

func nativeOutputJob(t *testing.T, s ResourceSecrets, provider string) (*Runner, *Job) {
	t.Helper()
	r := NewRunner(RunnerDeps{Resources: s})
	d := &Deployment{ID: "deployment", OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production"}
	a := planning.Action{ID: "resource:cache", Name: "cache", Provider: provider, Subject: planning.ResourceSubject, Resource: &planning.ResourceConfig{Provider: provider, Kind: "vercel-integration"}}
	job, err := r.newJob(t.Context(), d, &planning.Plan{ID: "plan"}, a, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r, job
}

func TestNativeOutputKeepPersistsNamesBeforeValuesAndForgetClearsThem(t *testing.T) {
	s := &nativeOutputTestStore{values: snapshotSecrets{resourceKey("org", "env", "other", "output:API_KEY"): []byte("other-resource")}}
	_, job := nativeOutputJob(t, s, "vercel")
	for _, field := range []string{"output:API_KEY", "output:BLOB_READ_WRITE_TOKEN", "output:GLOBAL_CONFIG", ResourceURL} {
		if err := job.Keep(t.Context(), field, []byte("application-secret")); err != nil {
			t.Fatal(err)
		}
	}
	metadata := resourceKey("org", "env", "cache", apiOutputFields)
	if len(s.puts) < 2 || s.puts[0] != metadata || s.puts[1] != resourceKey("org", "env", "cache", "output:API_KEY") {
		t.Fatal("application value was written before its cleanup inventory")
	}
	var fields []string
	if err := json.Unmarshal(s.values[metadata], &fields); err != nil || !slices.Contains(fields, "output:API_KEY") || strings.Contains(string(s.values[metadata]), "application-secret") {
		t.Fatal("output inventory lost a cleanup name or included a credential")
	}
	if err := job.Forget(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(s.values) != 1 || string(s.values[resourceKey("org", "env", "other", "output:API_KEY")]) != "other-resource" {
		t.Fatal("forget left dynamic credentials or crossed resource scope")
	}
}

func TestNativeOutputUncertainFirstWriteRemainsDiscoverableOnRetry(t *testing.T) {
	key := resourceKey("org", "env", "cache", "output:API_KEY")
	metadata := resourceKey("org", "env", "cache", apiOutputFields)
	s := &nativeOutputTestStore{values: snapshotSecrets{}, failPut: key, failAfterPut: true}
	_, job := nativeOutputJob(t, s, "vercel")
	if err := job.Keep(t.Context(), "output:API_KEY", []byte("uncertain-secret")); err == nil {
		t.Fatal("write failure was hidden")
	}
	if !strings.Contains(string(s.values[metadata]), "output:API_KEY") {
		t.Fatal("uncertain credential write has no durable cleanup inventory")
	}
	_, retry := nativeOutputJob(t, s, "vercel")
	if err := retry.Forget(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(s.values) != 0 {
		t.Fatal("retry could not remove the uncertain credential")
	}
}

func TestNativeOutputInventoryFailurePreventsCredentialWrite(t *testing.T) {
	metadata := resourceKey("org", "env", "cache", apiOutputFields)
	s := &nativeOutputTestStore{values: snapshotSecrets{}, failPut: metadata}
	_, job := nativeOutputJob(t, s, "vercel")
	if err := job.Keep(t.Context(), "output:API_KEY", []byte("private")); err == nil {
		t.Fatal("untracked credential write succeeded")
	}
	if _, present := s.values[resourceKey("org", "env", "cache", "output:API_KEY")]; present {
		t.Fatal("credential was stored without its cleanup proof")
	}
}

func TestNativeOutputInvalidInventoryCannotDeleteOtherFields(t *testing.T) {
	for _, provider := range []string{"vercel", "cloudflare-workers"} {
		t.Run(provider, func(t *testing.T) {
			s := &nativeOutputTestStore{values: snapshotSecrets{resourceKey("org", "env", "cache", apiOutputFields): []byte(`["output:API_KEY","../other/url"]`), resourceKey("org", "env", "cache", "output:API_KEY"): []byte("private")}}
			_, job := nativeOutputJob(t, s, provider)
			if err := job.Forget(t.Context()); err == nil {
				t.Fatal("invalid inventory was accepted")
			}
			if string(s.values[resourceKey("org", "env", "cache", "output:API_KEY")]) != "private" {
				t.Fatal("cleanup began before validating every recorded field")
			}
		})
	}
}

func TestNativeOutputTrackingLeavesServiceAndOtherProviderJobsUnchanged(t *testing.T) {
	for _, scenario := range []string{"service", "other-provider"} {
		t.Run(scenario, func(t *testing.T) {
			s := &nativeOutputTestStore{values: snapshotSecrets{}}
			r, job := nativeOutputJob(t, s, "cloud-run")
			if scenario == "service" {
				a := job.Action
				a.Provider = "vercel"
				a.Subject = planning.ServiceSubject
				a.Resource = nil
				a.Service = &planning.ServiceConfig{}
				var err error
				job, err = r.newJob(t.Context(), job.Deployment, job.Plan, a, nil, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := job.Keep(t.Context(), "output:LEGACY", []byte("value")); err != nil {
				t.Fatal(err)
			}
			if _, present := s.values[resourceKey("org", "env", "cache", apiOutputFields)]; present {
				t.Fatal("unrelated job gained native output bookkeeping")
			}
		})
	}
}

func TestNativeOutputSuccessfulCloudflareUpdatePrunesStaleAccessBeforeBinding(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint("failed_apply_", fail), func(t *testing.T) {
			plan := &planning.Plan{ID: "plan", OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production", Connections: map[string]string{connections.Cloudflare: "connection"}}
			cfg := &planning.ResourceConfig{Provider: "cloudflare-workers", Kind: "cloudflare-kv", Settings: map[string]string{}}
			a := planning.Action{ID: "resource:cache", Name: "cache", Subject: planning.ResourceSubject, Provider: "cloudflare-workers", Op: planning.Update, Resource: cfg}
			plan.Actions = []planning.Action{a}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintf(w, `{"success":true,"result":{"id":%q,"title":"cache"}}`, cfResourceID)
			}))
			defer server.Close()
			executor := &Cloudflare{Client: cloudflare.New(server.URL, false)}
			r, _, plans := newRunner(t, plan, executor, nil)
			r.executors["cloudflare-workers"] = executor
			r.credentials = &nativeInventoryCredentials{connection: &connections.Connection{ID: "connection", Cloud: connections.Cloudflare, OrgID: "org", Status: connections.StatusReady, Account: cfAccount, Details: map[string]string{"account_id": cfAccount}}, credential: []byte("admin-token")}
			oldcfg := *cfg
			oldcfg.Settings = map[string]string{"application_endpoint": "old-application-access"}
			raw, _ := json.Marshal(&oldcfg)
			plans.bindings[a.ID] = &planning.Binding{ID: "old-binding", OrgID: "org", ProjectID: "project", EnvironmentID: "env", Subject: planning.ResourceSubject, Name: "cache", Provider: a.Provider, ConnectionID: "connection", ExternalID: cfResourceID, PhysicalID: cfResourceID, Config: raw}
			metadata := resourceKey("org", "env", "cache", apiOutputFields)
			oldkey := resourceKey("org", "env", "cache", "output:CLOUDFLARE_API_TOKEN")
			s := &nativeOutputTestStore{values: snapshotSecrets{metadata: []byte(`["output:CLOUDFLARE_API_TOKEN"]`), oldkey: []byte("old-application-token")}}
			if fail {
				s.failPut = resourceKey("org", "env", "cache", CloudflareBindingOutput)
			}
			r.resources = s
			d := &Deployment{ID: "deployment", OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production"}
			err := r.run(t.Context(), d, plan, a, &Step{}, nil, func([]byte) error { return nil }, func(string, string) {}, nil)
			if fail {
				if err == nil || string(s.values[oldkey]) != "old-application-token" {
					t.Fatal("failed native apply pruned the previous application credential")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, present := s.values[oldkey]; present {
				t.Fatal("native-only update retained its old application token")
			}
			var fields []string
			if err = json.Unmarshal(s.values[metadata], &fields); err != nil || len(fields) != 1 || fields[0] != CloudflareBindingOutput {
				t.Fatalf("native output inventory did not shrink: %v %v", fields, err)
			}
		})
	}
}

func TestNativeOutputReplacementPreservesDestinationAndClearsNativeHistory(t *testing.T) {
	for _, retained := range []bool{false, true} {
		t.Run(fmt.Sprint("retained_", retained), func(t *testing.T) {
			metadata := resourceKey("org", "env", "cache", apiOutputFields)
			oldkey := resourceKey("org", "env", "cache", "output:AWS_SECRET_ACCESS_KEY")
			s := &nativeOutputTestStore{values: snapshotSecrets{metadata: []byte(`["output:AWS_SECRET_ACCESS_KEY","output:CLOUDFLARE_BINDING"]`), oldkey: []byte("old-r2-secret")}}
			r, source := nativeOutputJob(t, s, "s3")
			old := &planning.Binding{Subject: planning.ResourceSubject, Provider: "cloudflare-workers"}
			if retained {
				old = &planning.Binding{Subject: planning.ResourceSubject, Provider: "s3", Replaced: []planning.Binding{*old}}
			}
			job, err := r.newJob(t.Context(), source.Deployment, source.Plan, source.Action, nil, nil, old)
			if err != nil {
				t.Fatal(err)
			}
			if err = job.Keep(t.Context(), ResourceURL, []byte("s3://destination")); err != nil {
				t.Fatal(err)
			}
			if err = job.finishNativeOutputs(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, present := s.values[oldkey]; present {
				t.Fatal("provider replacement retained old native credentials")
			}
			if _, present := s.values[metadata]; present {
				t.Fatal("URL-only replacement retained stale native output bookkeeping")
			}
			if string(s.values[resourceKey("org", "env", "cache", ResourceURL)]) != "s3://destination" {
				t.Fatal("native cleanup removed the replacement address")
			}
		})
	}
}
