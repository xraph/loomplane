package cloudinfra

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/pkg/provider"
)

const platformApproved = "0123456789abcdef0123456789abcdef01234567"

// fakeDOApp is one App Platform app, as much of its API as the adapter calls.
type fakeDOApp struct {
	mu       sync.Mutex
	app      map[string]any
	puts     []map[string]any
	canceled []string
	missing  bool
	// updated is the deployment App Platform starts on a spec update, and
	// builtCommit the commit it reports for it.
	updated     string
	builtCommit string
}

func newFakeDOApp(t *testing.T, app map[string]any) (*fakeDOApp, *appPlatformDomains) {
	t.Helper()
	f := &fakeDOApp{app: app, builtCommit: platformApproved}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, newAppPlatformDomains(digitalocean.New(srv.URL))
}

func (f *fakeDOApp) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	if f.missing {
		reply(http.StatusNotFound, map[string]any{"id": "not_found", "message": "not found"})
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v2/apps/app-1":
		reply(http.StatusOK, map[string]any{"app": f.app})
	case r.Method == http.MethodPut && r.URL.Path == "/v2/apps/app-1":
		var body struct {
			Spec map[string]any `json:"spec"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.puts = append(f.puts, body.Spec)
		f.app["spec"] = body.Spec
		if f.updated != "" {
			f.app["in_progress_deployment"] = map[string]any{"id": f.updated, "phase": "BUILDING"}
		}
		reply(http.StatusOK, map[string]any{"app": f.app})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v2/apps/app-1/deployments/"):
		reply(http.StatusOK, map[string]any{"deployment": map[string]any{
			"id": f.updated, "phase": "ACTIVE",
			"services": []map[string]any{{"name": "web", "source_commit_hash": f.builtCommit}},
		}})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/cancel"):
		f.canceled = append(f.canceled, r.URL.Path)
		reply(http.StatusOK, map[string]any{})
	default:
		reply(http.StatusNotFound, map[string]any{"id": "not_found", "message": r.Method + " " + r.URL.Path})
	}
}

func doAppCred() provider.InfrastructureCredential {
	return provider.InfrastructureCredential{Token: "tok"}
}

func doAppBinding() RouteBinding {
	return RouteBinding{Backend: "platform", Provider: "digitalocean", ExternalID: "app-1"}
}

func specDomains(spec map[string]any) []map[string]any {
	list, _ := spec["domains"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, d := range list {
		out = append(out, d.(map[string]any))
	}
	return out
}

func TestAppPlatformRecordsForSubdomainAndApex(t *testing.T) {
	_, a := newFakeDOApp(t, map[string]any{"id": "app-1", "default_ingress": "https://web-abc12.ondigitalocean.app/", "spec": map[string]any{"name": "web"}})
	got, err := a.Records(t.Context(), doAppCred(), doAppBinding(), "app.example.com", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (PlatformRecord{Type: "CNAME", Value: "web-abc12.ondigitalocean.app"}) {
		t.Fatalf("subdomain records = %+v", got)
	}
	got, err = a.Records(t.Context(), doAppCred(), doAppBinding(), "example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, r := range got {
		counts[r.Type]++
	}
	if len(got) != len(digitalocean.AppIngressIPv4)+len(digitalocean.AppIngressIPv6) || counts["A"] != len(digitalocean.AppIngressIPv4) || counts["AAAA"] != len(digitalocean.AppIngressIPv6) {
		t.Fatalf("apex records = %+v", got)
	}
}

func TestAppPlatformRecordsNeedAnAddress(t *testing.T) {
	_, a := newFakeDOApp(t, map[string]any{"id": "app-1", "spec": map[string]any{"name": "web"}})
	_, err := a.Records(t.Context(), doAppCred(), doAppBinding(), "app.example.com", false)
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "App Platform hasn't given this app an address yet. Deploy it, then add the hostname.") {
		t.Fatalf("err = %v", err)
	}
}

func TestAppPlatformAttachAddsAnAliasDomainOnce(t *testing.T) {
	f, a := newFakeDOApp(t, map[string]any{"id": "app-1", "spec": map[string]any{"name": "web"}})
	for range 2 {
		if err := a.Attach(t.Context(), doAppCred(), doAppBinding(), "app.example.com", platformApproved); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.puts) != 1 {
		t.Fatalf("puts = %d, want 1", len(f.puts))
	}
	domains := specDomains(f.puts[0])
	if len(domains) != 1 || domains[0]["domain"] != "app.example.com" || domains[0]["type"] != "ALIAS" {
		t.Fatalf("domains = %+v", domains)
	}
}

func TestAppPlatformAttachWaitsForADeployment(t *testing.T) {
	f, a := newFakeDOApp(t, map[string]any{"id": "app-1", "spec": map[string]any{"name": "web"}, "in_progress_deployment": map[string]any{"id": "dep-0", "phase": "BUILDING"}})
	err := a.Attach(t.Context(), doAppCred(), doAppBinding(), "app.example.com", platformApproved)
	if !errors.Is(err, ErrPending) {
		t.Fatalf("err = %v", err)
	}
	var notice *routeNoticeError
	if !errors.As(err, &notice) || notice.message == "" {
		t.Fatalf("no notice on %v", err)
	}
	if len(f.puts) != 0 || len(f.canceled) != 0 {
		t.Fatalf("puts %d, canceled %d", len(f.puts), len(f.canceled))
	}
}

func TestAppPlatformAttachWaitsForTheDeploymentItStarts(t *testing.T) {
	f, a := newFakeDOApp(t, map[string]any{"id": "app-1", "spec": map[string]any{"name": "web"}})
	f.updated = "dep-1"
	if err := a.Attach(t.Context(), doAppCred(), doAppBinding(), "app.example.com", platformApproved); err != nil {
		t.Fatal(err)
	}
	if len(f.puts) != 1 || len(f.canceled) != 0 {
		t.Fatalf("puts %d, canceled %d", len(f.puts), len(f.canceled))
	}
}

func TestAppPlatformAttachCancelsAnUnreviewedDeployment(t *testing.T) {
	f, a := newFakeDOApp(t, map[string]any{"id": "app-1", "spec": map[string]any{"name": "web"}})
	f.updated, f.builtCommit = "dep-1", "ffffffffffffffffffffffffffffffffffffffff"
	err := a.Attach(t.Context(), doAppCred(), doAppBinding(), "app.example.com", platformApproved)
	want := "App Platform started a deployment of a newer commit than the reviewed one. Deploy the service to finish adding app.example.com."
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v", err)
	}
	if len(f.canceled) != 1 || !strings.Contains(f.canceled[0], "dep-1") {
		t.Fatalf("canceled = %v", f.canceled)
	}
	if len(specDomains(f.app["spec"].(map[string]any))) != 1 {
		t.Fatal("the domain should stay in the spec")
	}
}

func TestAppPlatformStatusMapsPhases(t *testing.T) {
	expires := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	step := func(name, status string, reason any) map[string]any {
		s := map[string]any{"name": name, "status": status}
		if reason != nil {
			s["reason"] = reason
		}
		return s
	}
	domain := func(phase string, extra map[string]any) map[string]any {
		d := map[string]any{"spec": map[string]any{"domain": "app.example.com", "type": "ALIAS"}, "phase": phase}
		for k, v := range extra {
			d[k] = v
		}
		return d
	}
	cases := []struct {
		name       string
		domain     map[string]any
		inSpec     bool
		attached   bool
		state      string
		message    string
		wantExpiry time.Time
	}{
		{name: "active", domain: domain("ACTIVE", map[string]any{"certificate_expires_at": expires.Format(time.RFC3339)}), inSpec: true, attached: true, state: "ready", wantExpiry: expires},
		{name: "active without an expiry", domain: domain("ACTIVE", nil), inSpec: true, attached: true, state: "ready"},
		{name: "pending", domain: domain("PENDING", nil), inSpec: true, attached: true, state: "issuing"},
		{name: "configuring", domain: domain("CONFIGURING", nil), inSpec: true, attached: true, state: "issuing"},
		{
			name: "error with a reason", inSpec: true, attached: true, state: "failed", message: "CNAME points elsewhere",
			domain: domain("ERROR", map[string]any{"progress": map[string]any{"steps": []any{
				step("verify-cname", "SUCCESS", nil),
				step("ensure-certificate", "ERROR", map[string]any{"code": "x", "message": "CNAME points elsewhere"}),
			}}}),
		},
		{
			name: "error without a reason", inSpec: true, attached: true, state: "failed", message: "ensure-certificate",
			domain: domain("ERROR", map[string]any{"progress": map[string]any{"steps": []any{
				step("verify-cname", "SUCCESS", nil),
				step("ensure-certificate", "ERROR", nil),
			}}}),
		},
		{name: "error without progress", domain: domain("ERROR", nil), inSpec: true, attached: true, state: "failed"},
		{name: "not in the spec", inSpec: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := map[string]any{"id": "app-1", "spec": map[string]any{"name": "web"}}
			if tc.inSpec {
				app["spec"] = map[string]any{"name": "web", "domains": []any{map[string]any{"domain": "app.example.com", "type": "ALIAS"}}}
				app["domains"] = []any{tc.domain}
			}
			_, a := newFakeDOApp(t, app)
			got, err := a.Status(t.Context(), doAppCred(), doAppBinding(), "app.example.com")
			if err != nil {
				t.Fatal(err)
			}
			if got.Attached != tc.attached || got.CertState != tc.state || !got.CertExpires.Equal(tc.wantExpiry) {
				t.Fatalf("status = %+v", got)
			}
			if tc.state == "failed" && (got.Message == "" || !strings.Contains(got.Message, tc.message)) {
				t.Fatalf("message = %q, want %q in it", got.Message, tc.message)
			}
		})
	}
}

func TestAppPlatformDetachRemovesOnlyItsHostname(t *testing.T) {
	f, a := newFakeDOApp(t, map[string]any{"id": "app-1", "spec": map[string]any{"name": "web", "domains": []any{
		map[string]any{"domain": "app.example.com", "type": "ALIAS"},
		map[string]any{"domain": "other.example.com", "type": "ALIAS"},
	}}})
	for range 2 {
		if err := a.Detach(t.Context(), doAppCred(), doAppBinding(), "app.example.com", platformApproved); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.puts) != 1 {
		t.Fatalf("puts = %d, want 1", len(f.puts))
	}
	domains := specDomains(f.puts[0])
	if len(domains) != 1 || domains[0]["domain"] != "other.example.com" {
		t.Fatalf("domains = %+v", domains)
	}
}

func TestAppPlatformDetachOfAMissingAppIsDone(t *testing.T) {
	f, a := newFakeDOApp(t, map[string]any{"id": "app-1"})
	f.missing = true
	if err := a.Detach(t.Context(), doAppCred(), doAppBinding(), "app.example.com", platformApproved); err != nil {
		t.Fatal(err)
	}
}
