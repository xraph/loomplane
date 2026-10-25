//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/planning"
)

type sourceFixtureDNS struct{}

func (sourceFixtureDNS) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("10.20.0.8")}, nil
}

func TestCloudSQLPSCSourceRequiresSQLBeforePublishing(t *testing.T) {
	for _, scenario := range []string{"verified", "endpoint-missing", "transport-retry", "bad-password", "changed-endpoint"} {
		t.Run(scenario, func(t *testing.T) {
			dsn := freshDatabase(t)
			admin, err := pgx.Connect(t.Context(), dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = admin.Exec(context.Background(), "DROP ROLE IF EXISTS app")
				_ = admin.Close(context.Background())
			})
			pgcfg := admin.Config()
			google := newFakeGoogle(t)
			job := checkpointSQLJob(t, google)
			job.Action.Name = strings.ReplaceAll(pgcfg.Database, "_", "-")
			job.Action.ID = "resource:" + job.Action.Name
			instance := deployments.SQLInstanceName(job) + "-europe-west1"
			profile, err := manageddb.CloudSQLPSCPostgres("17")
			if err != nil {
				t.Fatal(err)
			}
			endpoint := manageddb.CloudSQLSourceEndpoint{ConsumerProject: "app-project", Network: "projects/app-project/global/networks/apps", ForwardingRule: "projects/app-project/regions/europe-west1/forwardingRules/database"}
			job.Action.Resource.Managed = &planning.ManagedDatabaseConfig{Profile: *profile, ConnectionID: job.Connection.ID, AccountIdentity: connections.AccountIdentity(job.Connection.Cloud, job.Connection.Account, job.Connection.Details), CloudSQLPSC: endpoint}
			const attachment = "https://www.googleapis.com/compute/v1/projects/sql-tenant/regions/europe-west1/serviceAttachments/source"
			created, endpointReady, transportReady := false, scenario != "endpoint-missing", scenario != "transport-retry"
			endpointID := "123456789"
			counts := map[string]int{}
			var settings map[string]any
			var password string
			google.deploy = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/compute/v1/"+endpoint.ForwardingRule {
					if !endpointReady {
						w.WriteHeader(404)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"kind": "compute#forwardingRule", "id": endpointID, "name": "database", "selfLink": "https://www.googleapis.com/compute/v1/" + endpoint.ForwardingRule, "region": "https://www.googleapis.com/compute/v1/projects/app-project/regions/europe-west1", "network": "https://www.googleapis.com/compute/v1/" + endpoint.Network, "target": attachment, "IPAddress": "10.20.0.8", "pscConnectionStatus": "ACCEPTED"})
					return
				}
				if r.Method == http.MethodGet {
					if !created {
						w.WriteHeader(404)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"kind": "sql#instance", "settings": settings, "project": "acme-prod", "region": "europe-west1", "createTime": "2026-10-21T00:00:00Z", "name": instance, "connectionName": "acme-prod:europe-west1:" + instance, "state": "RUNNABLE", "databaseVersion": "POSTGRES_17", "pscServiceAttachmentLink": attachment, "dnsName": instance + ".sql.goog."})
					return
				}
				stage, operation := "instance", "CREATE"
				switch {
				case strings.HasSuffix(r.URL.Path, "/databases"):
					stage, operation = "database", "CREATE_DATABASE"
				case strings.HasSuffix(r.URL.Path, "/users"):
					stage, operation = "user", "CREATE_USER"
					var body map[string]string
					if decodeErr := json.NewDecoder(r.Body).Decode(&body); decodeErr != nil {
						t.Error(decodeErr)
					}
					password = body["password"]
					actual := password
					if scenario == "bad-password" {
						actual = "different-password"
					}
					if _, sqlErr := admin.Exec(t.Context(), "SELECT set_config('loomplane.test_password',$1,false)", actual); sqlErr != nil {
						t.Error(sqlErr)
					}
					if _, sqlErr := admin.Exec(t.Context(), `DO $test$ BEGIN CREATE ROLE app LOGIN; EXECUTE format('ALTER ROLE app PASSWORD %L',current_setting('loomplane.test_password')); END $test$`); sqlErr != nil {
						t.Error(sqlErr)
					}
				default:
					var body struct {
						Settings map[string]any `json:"settings"`
					}
					if decodeErr := json.NewDecoder(r.Body).Decode(&body); decodeErr != nil {
						t.Error(decodeErr)
					}
					settings = body.Settings
					settings["settingsVersion"] = "1"
					created = true
				}
				counts[stage]++
				fmt.Fprintf(w, `{"kind":"sql#operation","name":%q,"status":"DONE","targetProject":"acme-prod","targetId":%q,"operationType":%q}`, stage, instance, operation)
			})
			var saved []byte
			job.Checkpoint = func(raw []byte) error { saved = append([]byte(nil), raw...); return nil }
			factoryCalls := 0
			executor := &deployments.CloudSQL{Google: gcp.New(google.srv.URL).WithDNSResolver(sourceFixtureDNS{}), PSCConnector: func(context.Context, *gcp.Session, string, string) (gcp.SQLConnector, error) {
				factoryCalls++
				if scenario == "changed-endpoint" {
					endpointID = "987654321"
				}
				return nativeFixtureConnector{dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
					if !transportReady {
						return nil, errors.New("private route unavailable")
					}
					return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(pgcfg.Host, fmt.Sprint(pgcfg.Port)))
				}}, nil
			}}
			outcome, err := executor.Apply(t.Context(), job)
			if scenario == "endpoint-missing" || scenario == "transport-retry" {
				if err == nil {
					t.Fatal("unverified source succeeded")
				}
				if _, keptErr := job.Recall(t.Context(), deployments.ResourceURL); !errors.Is(keptErr, deployments.ErrNoCredential) {
					t.Fatal("unverified URL published", keptErr)
				}
				endpointReady, transportReady = true, true
				job.Resume = saved
				outcome, err = executor.Apply(t.Context(), job)
			}
			if scenario == "bad-password" || scenario == "changed-endpoint" {
				if err == nil {
					t.Fatal("unverified SQL identity or changed endpoint succeeded")
				}
				if strings.Contains(err.Error(), password) {
					t.Fatal("probe error leaked password")
				}
				if _, keptErr := job.Recall(t.Context(), deployments.ResourceURL); !errors.Is(keptErr, deployments.ErrNoCredential) {
					t.Fatal("failed proof published URL", keptErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			kept, err := job.Recall(t.Context(), deployments.ResourceURL)
			if err != nil || len(kept) == 0 || factoryCalls == 0 || counts["instance"] != 1 || counts["database"] != 1 || counts["user"] != 1 || strings.Contains(string(saved), password) {
				t.Fatal("source replay or credential publication failed", err, counts, factoryCalls)
			}
			var readers int
			if err = admin.QueryRow(t.Context(), `SELECT count(*) FROM pg_roles WHERE rolname LIKE 'lp_recovery_%'`).Scan(&readers); err != nil || readers != 0 {
				t.Fatal("source probe created recovery roles", readers, err)
			}
			raw, err := json.Marshal(job.Action.Resource)
			if err != nil {
				t.Fatal(err)
			}
			binding := &planning.Binding{Provider: "cloud-sql", Region: "europe-west1", ConnectionID: job.Connection.ID, Config: raw, ExternalID: outcome.ExternalID, PhysicalID: outcome.PhysicalID}
			if observed, observeErr := executor.Observe(t.Context(), job.Connection, job.Credential, binding); observeErr != nil || !observed.Exists {
				t.Fatal("verified source observation failed", observeErr)
			}
			endpointReady = false
			if _, observeErr := executor.Observe(t.Context(), job.Connection, job.Credential, binding); observeErr == nil {
				t.Fatal("missing PSC endpoint was reported healthy")
			}
		})
	}
}
