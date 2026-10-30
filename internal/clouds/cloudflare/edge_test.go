package cloudflare

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

type edgeFixture struct {
	client   *Client
	zone     provider.DNSZone
	cred     provider.InfrastructureCredential
	rules    []cacheRule
	next     int
	purges   []map[string]any
	failRule bool
	ssl      string
	record   infrastructureRecord
	patches  int
}

func newEdgeFixture(t *testing.T) *edgeFixture {
	t.Helper()
	f := &edgeFixture{zone: provider.DNSZone{ID: "zone", Name: "example.com", AccountID: "account"}, cred: provider.InfrastructureCredential{Token: "secret", AccountID: "account"}, ssl: "strict", record: infrastructureRecord{ID: "dns", Name: "api.example.com", Type: "A", Content: "192.0.2.1", TTL: 60, Comment: "loomplane:route:route"}}
	f.rules = []cacheRule{{ID: "foreign", Ref: "foreign", Expression: "true", Action: "set_cache_settings", Parameters: map[string]any{"cache": false}, Enabled: true}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var result any
		switch {
		case strings.HasSuffix(r.URL.Path, "/settings/ssl"):
			result = map[string]any{"value": f.ssl}
		case strings.HasSuffix(r.URL.Path, "/entrypoint"):
			result = cacheRuleset{ID: "set", Rules: f.rules}
		case strings.HasSuffix(r.URL.Path, "/purge_cache"):
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if _, bad := body["purge_everything"]; bad {
				t.Error("zone-wide purge")
			}
			f.purges = append(f.purges, body)
			result = map[string]any{"id": "purge"}
		case strings.HasSuffix(r.URL.Path, "/dns_records/dns"):
			if r.Method == http.MethodPatch {
				f.patches++
				var body infrastructureRecord
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				body.ID = "dns"
				f.record = body
			}
			result = f.record
		case r.Method == http.MethodDelete:
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			if id == "foreign" {
				t.Error("deleted foreign rule")
			}
			f.rules = slices.DeleteFunc(f.rules, func(rule cacheRule) bool { return rule.ID == id })
			result = cacheRuleset{ID: "set", Rules: f.rules}
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/rules"):
			var body struct {
				cacheRule
				Position struct {
					Before string `json:"before"`
				} `json:"position"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if f.failRule && strings.HasSuffix(body.Ref, "_0") {
				f.failRule = false
				w.WriteHeader(503)
				_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1000,"message":"secret"}]}`))
				return
			}
			f.next++
			body.ID = fmt.Sprint(f.next)
			at := len(f.rules)
			for i, rule := range f.rules {
				if rule.ID == body.Position.Before {
					at = i
					break
				}
			}
			f.rules = slices.Insert(f.rules, at, body.cacheRule)
			result = cacheRuleset{ID: "set", Rules: f.rules}
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": result})
	}))
	t.Cleanup(server.Close)
	f.client = New(server.URL, false)
	return f
}

func TestEdgeRulesScopedRetryAndDisable(t *testing.T) {
	f := newEdgeFixture(t)
	p := provider.EdgePolicy{Enabled: true, Rules: []provider.EdgeRule{{PathPrefix: "/assets/", Mode: "public", EdgeTTL: 3600}, {PathPrefix: "/api/public", Mode: "origin"}}}
	f.failRule = true
	err := f.client.ApplyEdge(t.Context(), f.cred, f.zone, "org/route", "api.example.com", p, []string{"/admin"})
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe error: %v", err)
	}
	if !strings.HasSuffix(f.rules[len(f.rules)-1].Ref, "_updating") {
		t.Fatal("partial update must end in a bypass")
	}
	if err = f.client.ApplyEdge(t.Context(), f.cred, f.zone, "org/route", "api.example.com", p, []string{"/admin"}); err != nil {
		t.Fatal(err)
	}
	if len(f.rules) != 5 || f.rules[0].ID != "foreign" {
		t.Fatalf("rules: %+v", f.rules)
	}
	guard := f.rules[len(f.rules)-1]
	if normalizeParams(guard.Parameters) != normalizeParams(edgeBypass()) {
		t.Fatal("safety bypass retained a browser TTL override")
	}
	for _, part := range []string{"authorization", "http.cookie", "GET", "HEAD", "/admin", `http.host eq "api.example.com"`} {
		if !strings.Contains(guard.Expression, part) {
			t.Error("missing guard", part)
		}
	}
	before := f.next
	if err = f.client.ApplyEdge(t.Context(), f.cred, f.zone, "org/route", "api.example.com", p, []string{"/admin"}); err != nil {
		t.Fatal(err)
	}
	if f.next != before {
		t.Fatal("unchanged rules mutated")
	}
	if err = f.client.ApplyEdge(t.Context(), f.cred, f.zone, "org/route", "api.example.com", provider.EdgePolicy{}, nil); err != nil {
		t.Fatal(err)
	}
	if len(f.rules) != 1 || f.rules[0].ID != "foreign" {
		t.Fatal("disable must preserve foreign rules")
	}
}

func TestEdgeDNSReconcilesLostProxyResponse(t *testing.T) {
	f := newEdgeFixture(t)
	prior := provider.DNSRecord{ID: "dns", ZoneID: "zone", Name: "api", Type: "A", Value: "192.0.2.1", TTL: 60, Owner: "loomplane:route:route"}
	desired := prior
	desired.Proxied = true
	desired.TTL = 300
	req := provider.DNSUpsertRequest{Zone: f.zone, Expected: &prior, Record: desired}
	got, err := f.client.UpsertRecord(t.Context(), f.cred, req)
	if err != nil || got != desired || f.record.TTL != 1 {
		t.Fatalf("proxy: %+v %v", got, err)
	}
	got, err = f.client.UpsertRecord(t.Context(), f.cred, req)
	if err != nil || got != desired || f.patches != 1 {
		t.Fatalf("lost reply: %+v %v patches=%d", got, err, f.patches)
	}
	f.record.Content = "192.0.2.99"
	if err = f.client.VerifyEdgeDNS(t.Context(), f.cred, f.zone, desired); err == nil {
		t.Fatal("accepted changed origin")
	}
	f.record.Content = desired.Value
	got, err = f.client.UpsertRecord(t.Context(), f.cred, provider.DNSUpsertRequest{Zone: f.zone, Expected: &desired, Record: prior})
	if err != nil || got != prior || f.record.Proxied {
		t.Fatalf("disable: %+v %v", got, err)
	}
}

func TestEdgeRequiresStrictTLSAndConfinesPurge(t *testing.T) {
	f := newEdgeFixture(t)
	f.ssl = "flexible"
	if err := f.client.ApplyEdge(t.Context(), f.cred, f.zone, "route", "api.example.com", provider.EdgePolicy{Enabled: true}, nil); err == nil {
		t.Fatal("accepted flexible TLS")
	}
	if f.next != 0 {
		t.Fatal("rules changed before TLS check")
	}
	for _, host := range []string{"other.test", "*.example.com"} {
		if err := f.client.PurgeEdge(t.Context(), f.cred, f.zone, host, nil); err == nil {
			t.Error("accepted host", host)
		}
	}
	if err := f.client.PurgeEdge(t.Context(), f.cred, f.zone, "api.example.com", []string{"https://other.example.com/x"}); err == nil {
		t.Fatal("cross-service purge")
	}
	if err := f.client.PurgeEdge(t.Context(), f.cred, f.zone, "api.example.com", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.purges) != 1 || f.purges[0]["hosts"].([]any)[0] != "api.example.com" {
		t.Fatal(f.purges)
	}
}
