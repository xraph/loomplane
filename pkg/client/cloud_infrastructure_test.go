package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestInfrastructureClientContracts(t *testing.T) {
	tests := []struct {
		name, method, path, body, response string
		call                               func(*Client) error
	}{
		{"servers", "GET", "/api/v1/clouds/c/servers", "", `{"servers":[{"id":"s","ownership":"adopted","state_revision":4}]}`, func(c *Client) error {
			v, e := c.CloudServers(t.Context(), "c")
			if e == nil && (len(v) != 1 || v[0].StateRevision != 4) {
				t.Fatalf("servers %+v", v)
			}
			return e
		}},
		{"options", "GET", "/api/v1/clouds/c/server-options/conn", "", `{}`, func(c *Client) error { _, e := c.CloudServerOptions(t.Context(), "c", "conn"); return e }},
		{"preview", "POST", "/api/v1/clouds/c/servers/preview", `{"kind":"adopt","cloud_revision":3,"confirm_adoption":true}`, `{"id":"op","review":"digest","status":"preview"}`, func(c *Client) error {
			_, e := c.PreviewCloudServer(t.Context(), "c", ServerInput{Kind: "adopt", CloudRevision: 3, ConfirmAdoption: true})
			return e
		}},
		{"apply", "POST", "/api/v1/clouds/c/infrastructure-operations/op/apply", `{"review":"digest"}`, `{}`, func(c *Client) error {
			_, e := c.ApplyInfrastructureOperation(t.Context(), "c", "op", InfrastructureApplyInput{Review: "digest"})
			return e
		}},
		{"retry", "POST", "/api/v1/clouds/c/infrastructure-operations/op/retry", "", `{}`, func(c *Client) error { _, e := c.RetryInfrastructureOperation(t.Context(), "c", "op"); return e }},
		{"cancel", "POST", "/api/v1/clouds/c/infrastructure-operations/op/cancel", "", `{}`, func(c *Client) error { _, e := c.CancelInfrastructureOperation(t.Context(), "c", "op"); return e }},
		{"usage", "GET", "/api/v1/clouds/c/servers/s/usage", "", `{"workloads":[{"phase":"pending_recovery","execution_revision":7}]}`, func(c *Client) error {
			v, e := c.CloudServerUsage(t.Context(), "c", "s")
			if e == nil && (len(v.Workloads) != 1 || v.Workloads[0].ExecutionRevision != 7) {
				t.Fatalf("usage %+v", v)
			}
			return e
		}},
		{"drain preview", "POST", "/api/v1/clouds/c/servers/s/drain/preview", "", `{}`, func(c *Client) error { _, e := c.PreviewCloudServerDrain(t.Context(), "c", "s"); return e }},
		{"drain apply", "POST", "/api/v1/clouds/c/servers/s/drain/apply", `{"review":"digest"}`, `{}`, func(c *Client) error {
			_, e := c.ApplyCloudServerDrain(t.Context(), "c", "s", InfrastructureApplyInput{Review: "digest"})
			return e
		}},
		{"pools", "GET", "/api/v1/clouds/c/pools", "", `{"pools":[{"id":"pool","members":[{"server_id":"s","cpu_millis":500,"memory_bytes":1024}]}]}`, func(c *Client) error {
			v, e := c.CloudPools(t.Context(), "c")
			if e == nil && (len(v) != 1 || v[0].Members[0].CPUMillis != 500) {
				t.Fatalf("pools %+v", v)
			}
			return e
		}},
		{"pool save", "POST", "/api/v1/clouds/c/pools/pool", `{"name":"shared","revision":2,"members":[]}`, `{}`, func(c *Client) error {
			_, e := c.UpdateCloudPool(t.Context(), "c", "pool", PoolInput{Name: "shared", Revision: 2, Members: []PoolMember{}})
			return e
		}},
		{"move", "POST", "/api/v1/clouds/c/placements/accept-move", `{"project_id":"p","environment_id":"env","service":"web","pool_id":"pool","destination_server_id":"s","review":"digest"}`, `{}`, func(c *Client) error {
			_, e := c.AcceptCloudMove(t.Context(), "c", MoveInput{ProjectID: "p", EnvironmentID: "env", Service: "web", PoolID: "pool", DestinationServerID: "s", Review: "digest"})
			return e
		}},
		{"zones", "GET", "/api/v1/dns-zones", "", `{"zones":[{"id":"z","mode":"external"}]}`, func(c *Client) error {
			v, e := c.DNSZones(t.Context())
			if e == nil && (len(v) != 1 || v[0].Mode != "external") {
				t.Fatalf("zones %+v", v)
			}
			return e
		}},
		{"zone create", "POST", "/api/v1/dns-zones", `{"name":"example.com","mode":"external","project_ids":["p"]}`, `{}`, func(c *Client) error {
			_, e := c.CreateDNSZone(t.Context(), ZoneInput{Name: "example.com", Mode: "external", ProjectIDs: []string{"p"}})
			return e
		}},
		{"zone update", "POST", "/api/v1/dns-zones/z/update", `{"revision":3,"project_ids":["p"],"renew_proof":true}`, `{}`, func(c *Client) error {
			_, e := c.UpdateDNSZone(t.Context(), "z", ZoneUpdate{Revision: 3, ProjectIDs: []string{"p"}, RenewProof: true})
			return e
		}},
		{"zone remove", "POST", "/api/v1/dns-zones/z/remove", `{"revision":3}`, ``, func(c *Client) error { return c.RemoveDNSZone(t.Context(), "z", ResourceRevisionInput{Revision: 3}) }},
		{"routes", "GET", "/api/v1/projects/p/domain-routes", "", `{"routes":[{"id":"r","verification":{"dns":true,"tls":false,"backend":true},"revision":9}]}`, func(c *Client) error {
			v, e := c.DomainRoutes(t.Context(), "p")
			if e == nil && (len(v) != 1 || !v[0].Verification.DNS || v[0].Verification.TLS || v[0].Revision != 9) {
				t.Fatalf("routes %+v", v)
			}
			return e
		}},
		{"route apply", "POST", "/api/v1/projects/p/route-operations/op/apply", `{"review":"digest"}`, `{}`, func(c *Client) error {
			_, e := c.ApplyDomainRoute(t.Context(), "p", "op", InfrastructureApplyInput{Review: "digest"})
			return e
		}},
		{"route retry", "POST", "/api/v1/projects/p/route-operations/op/retry", "", `{}`, func(c *Client) error { _, e := c.RetryDomainRoute(t.Context(), "p", "op"); return e }},
		{"route verify", "POST", "/api/v1/projects/p/domain-routes/r/verify", "", `{}`, func(c *Client) error { _, e := c.VerifyDomainRoute(t.Context(), "p", "r"); return e }},
		{"route remove", "POST", "/api/v1/projects/p/domain-routes/r/remove", `{"revision":9}`, `{}`, func(c *Client) error {
			_, e := c.RemoveDomainRoute(t.Context(), "p", "r", ResourceRevisionInput{Revision: 9})
			return e
		}},
		{"ttl", "POST", "/api/v1/projects/p/domain-routes/r/ttl", `{"revision":9,"external_ttl_seconds":7200}`, `{}`, func(c *Client) error {
			_, e := c.UpdateDomainRouteTTL(t.Context(), "p", "r", RouteTTLInput{Revision: 9, ExternalTTLSeconds: 7200})
			return e
		}},
		{"history", "GET", "/api/v1/projects/p/domain-routes/r/operations", "", `{"operations":[{"id":"op","not_before":"2026-10-23T12:00:00Z","desired_record":{"value":"192.0.2.1","ttl":3600},"message":"set DNS"}]}`, func(c *Client) error {
			v, e := c.DomainRouteHistory(t.Context(), "p", "r")
			if e == nil && (len(v) != 1 || v[0].DesiredRecord.TTL != 3600 || v[0].NotBefore.IsZero()) {
				t.Fatalf("history %+v", v)
			}
			return e
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tt.method || r.URL.Path != tt.path || r.Header.Get("Authorization") != "Bearer scoped-key" {
					t.Errorf("request %s %s auth=%s", r.Method, r.URL.Path, r.Header.Get("Authorization"))
				}
				if tt.body != "" {
					var got, want map[string]any
					if e := json.NewDecoder(r.Body).Decode(&got); e != nil {
						t.Error(e)
					}
					if e := json.Unmarshal([]byte(tt.body), &want); e != nil {
						t.Fatal(e)
					}
					for k, v := range want {
						a, _ := json.Marshal(v)
						b, _ := json.Marshal(got[k])
						if string(a) != string(b) {
							t.Errorf("%s=%s want %s", k, b, a)
						}
					}
				}
				if tt.response == "" {
					w.WriteHeader(http.StatusNoContent)
				} else {
					_, _ = w.Write([]byte(tt.response))
				}
			}))
			defer srv.Close()
			if e := tt.call(New(srv.URL, "scoped-key")); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestInfrastructureClientPreservesAPIErrorsAndLatestProgress(t *testing.T) {
	for _, status := range []int{403, 409, 422, 501} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"message":"review or capability rejected"}`))
			}))
			defer srv.Close()
			_, err := New(srv.URL, "key").PreviewCloudServer(t.Context(), "c", ServerInput{})
			var api *Error
			if !errors.As(err, &api) || api.Status != status {
				t.Fatalf("%v", err)
			}
		})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"op","status":"running","attempt":4,"actions":[{"id":"action","status":"in-progress"}]}`))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	v, err := New(srv.URL, "key").WaitInfrastructureOperation(ctx, "c", "op")
	if !errors.Is(err, context.DeadlineExceeded) || v == nil || v.Attempt != 4 || len(v.Actions) != 1 {
		t.Fatalf("%+v %v", v, err)
	}
}
func TestEnvironmentsReturnsDecodedScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/projects/p/environments" {
			t.Error(r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"environments":[{"id":"env","name":"staging"}]}`))
	}))
	defer srv.Close()
	v, err := New(srv.URL, "key").Environments(t.Context(), "p")
	if err != nil || len(v) != 1 || v[0].Name != "staging" {
		t.Fatalf("%+v %v", v, err)
	}
}
func TestCloudPlacementContractsKeepRuntimePoolCompensationAndRoutes(t *testing.T) {
	var v CloudPreview
	err := json.Unmarshal([]byte(`{"input":{"runtime":"server","pool_id":"pool"},"after":{"runtime":"server","pool_id":"pool"},"after_default":{"runtime":"server","pool_id":"pool"},"environments":[{"compensation_mode":"restore_retained","compensation_message":"exact retained runtime","routing_status":"draining","routes":[{"route_id":"r","not_before":"2026-10-23T12:00:00Z"}]}]}`), &v)
	if err != nil || v.Input.PoolID != "pool" || v.After.Runtime != "server" || v.AfterDefault.PoolID != "pool" || v.Environments[0].CompensationMode != "restore_retained" || v.Environments[0].Routes[0].NotBefore.IsZero() {
		t.Fatalf("%+v %v", v, err)
	}
}

func TestRouteWaitPreservesManualDNSAndDrainCheckpoint(t *testing.T) {
	for _, status := range []string{"waiting", "running"} {
		t.Run(status, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"id":"op","status":"` + status + `","not_before":"2026-10-23T12:00:00Z","message":"Update external DNS","desired_record":{"type":"A","value":"192.0.2.1","ttl":7200},"route":{"verification":{"dns":false,"tls":true,"backend":true}}}`))
			}))
			defer srv.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			v, err := New(srv.URL, "key").WaitDomainRouteOperation(ctx, "p", "op")
			if status == "waiting" && err != nil {
				t.Fatal(err)
			}
			if status == "running" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			if v == nil || v.DesiredRecord.Value != "192.0.2.1" || v.DesiredRecord.TTL != 7200 || v.NotBefore.IsZero() || v.Route.Verification.DNS || !v.Route.Verification.TLS {
				t.Fatalf("lost route progress %+v", v)
			}
		})
	}
}

func TestReservationRecoveryClientUsesAcceptedAuthority(t *testing.T) {
	for _, action := range []string{"preview-recovery", "recover"} {
		t.Run(action, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/api/v1/clouds/c/placements/"+action {
					t.Errorf("%s %s", r.Method, r.URL.Path)
				}
				var in RecoveryInput
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
					t.Error(err)
				}
				if in.PlanID != "accepted" || in.Service != "web" || in.Review != "digest" {
					t.Error(in)
				}
				_, _ = w.Write([]byte(`{"plan_id":"accepted","service":"web","phase":"released","revision":8,"storage_retained":true}`))
			}))
			defer srv.Close()
			c := New(srv.URL, "key")
			in := RecoveryInput{PlanID: "accepted", Service: "web", Review: "digest"}
			var v *ReservationRecovery
			var err error
			if action == "recover" {
				v, err = c.RecoverCloudReservation(t.Context(), "c", in)
			} else {
				v, err = c.PreviewCloudReservationRecovery(t.Context(), "c", in)
			}
			if err != nil || v.PlanID != "accepted" || v.Revision != 8 || !v.StorageRetained {
				t.Fatalf("%+v %v", v, err)
			}
		})
	}
}
