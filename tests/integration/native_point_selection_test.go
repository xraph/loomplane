//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/internal/security"
)

type selectedNativePointStore interface {
	GetNativePoint(context.Context, string, string, string) (*recovery.NativePoint, error)
}

type nativeSelectionCredentials struct {
	connection *connections.Connection
	credential []byte
}

func (c nativeSelectionCredentials) Credential(context.Context, string, string) (*connections.Connection, []byte, error) {
	return c.connection, c.credential, nil
}

type nativeSelectionProjects struct{ recovery.Projects }

func (nativeSelectionProjects) GetProject(context.Context, string, string) (*projects.Project, error) {
	return &projects.Project{ID: "project", OrgID: "org"}, nil
}

type nativeSelectionAuthorizer struct{}

func (nativeSelectionAuthorizer) Require(context.Context, security.Principal, security.Permission, string) error {
	return nil
}

func TestNativePointSelectionRechecksCloudAfterBindingRemoval(t *testing.T) {
	for _, provider := range []string{"rds", "cloud-sql"} {
		t.Run(provider, func(t *testing.T) {
			f := nativePointHistoryFixture(t)
			created := time.Date(2026, 10, 21, 2, 0, 0, 1, time.UTC)
			changed := false
			reads := 0
			point := recovery.NativePoint{Name: "daily", Kind: "manual", Engine: "postgres", Version: "17.2", CreatedAt: created, Encrypted: true, Status: "available"}
			conn := &connections.Connection{ID: "checked-account", OrgID: "org", Cloud: connections.AWS, Account: awsAccount, Status: connections.StatusReady, Details: map[string]string{"region": "us-east-1"}}
			binding := f.source.Binding
			binding.ID, binding.Name, binding.Provider, binding.ConnectionID = "selected-binding", "selected-database", provider, conn.ID
			backend := &deployments.NativeDatabases{}
			credential := []byte(awsKey(awsKeyID))
			if provider == "rds" {
				point.ProviderID = "arn:aws:rds:us-east-1:" + awsAccount + ":snapshot:daily"
				point.KMSKey = "original-key"
				fake := newFakeAWS(t)
				fake.query["DescribeDBSnapshots"] = func(form url.Values) (int, string) {
					reads++
					if form.Get("DBSnapshotIdentifier") != "daily" || form.Get("DBInstanceIdentifier") != "" || form.Get("DbiResourceId") != "" {
						t.Error("selected native point used a source lookup or inventory scan", form)
					}
					nativeTime := created
					if changed {
						nativeTime = nativeTime.Add(time.Nanosecond)
					}
					return http.StatusOK, fmt.Sprintf(`<DescribeDBSnapshotsResponse><DescribeDBSnapshotsResult><DBSnapshots><DBSnapshot><DBSnapshotIdentifier>daily</DBSnapshotIdentifier><DBSnapshotArn>%s</DBSnapshotArn><DBInstanceIdentifier>database</DBInstanceIdentifier><DbiResourceId>db-original</DbiResourceId><SnapshotType>manual</SnapshotType><SnapshotCreateTime>%s</SnapshotCreateTime><Status>available</Status><Engine>postgres</Engine><EngineVersion>17.2</EngineVersion><Encrypted>true</Encrypted><KmsKeyId>original-key</KmsKeyId></DBSnapshot></DBSnapshots></DescribeDBSnapshotsResult></DescribeDBSnapshotsResponse>`, point.ProviderID, nativeTime.Format(time.RFC3339Nano))
				}
				backend.AWS = aws.New(fake.srv.URL)
			} else {
				conn.Cloud, conn.Account, conn.Details = connections.GCP, "acme-prod", map[string]string{"project_id": "acme-prod"}
				google := newFakeGoogle(t)
				credential = []byte(serviceAccountKey(t, google.srv.URL+"/token"))
				binding.Region, binding.ExternalID = "europe-west1", "acme-prod:europe-west1:database"
				original := &gcp.SQLInstance{Name: "database", Project: "acme-prod", Region: binding.Region, ConnectionName: binding.ExternalID, CreateTime: "2026-10-21T00:00:00Z"}
				var pinErr error
				binding.PhysicalID, pinErr = original.Incarnation("acme-prod", binding.Region, "database")
				if pinErr != nil {
					t.Fatal(pinErr)
				}
				point.ProviderID, point.Kind, point.Version, point.Status = "projects/acme-prod/backups/daily", "FINAL", "17", "SUCCESSFUL"
				google.deploy = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					reads++
					if r.Method != http.MethodGet || r.URL.Path != "/v1/projects/acme-prod/backups/daily" || r.URL.RawQuery != "" {
						t.Errorf("selected backup used source lookup or mutation: %s %s", r.Method, r.URL)
					}
					nativeTime := created
					if changed {
						nativeTime = nativeTime.Add(time.Nanosecond)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"name": point.ProviderID, "kind": "sql#backup", "instance": "database", "type": "FINAL", "state": "SUCCESSFUL", "databaseVersion": "POSTGRES_17", "instanceSettings": original, "backupInterval": map[string]string{"startTime": nativeTime.Format(time.RFC3339Nano), "endTime": "2026-10-21T02:00:01Z"}})
				})
				backend.Google = gcp.New(google.srv.URL)
			}
			profile, err := manageddb.Postgres(provider, "17")
			if err != nil {
				t.Fatal(err)
			}
			binding.Config, err = json.Marshal(planning.ResourceConfig{Kind: "postgres", Version: "17", Provider: provider, Region: binding.Region, Managed: &planning.ManagedDatabaseConfig{Profile: *profile, ConnectionID: conn.ID, AccountIdentity: connections.AccountIdentity(conn.Cloud, conn.Account, conn.Details)}})
			if err != nil {
				t.Fatal(err)
			}
			if err = planning.NewPgStore(f.db).PutBinding(t.Context(), &binding); err != nil {
				t.Fatal(err)
			}
			sources, err := f.store.ListNativeSources(t.Context(), "org", "project", recovery.NativePageInput{})
			if err != nil {
				t.Fatal(err)
			}
			for _, source := range sources.Sources {
				if source.Name == binding.Name {
					f.source = source
				}
			}
			if f.source.Name != binding.Name {
				t.Fatal("reviewed native source was not captured")
			}
			if err = f.refresh(t.Context(), []recovery.NativePoint{point}); err != nil {
				t.Fatal(err)
			}
			history, err := f.store.ReadNativeInventory(t.Context(), "org", f.source.ID, recovery.NativePageInput{})
			if err != nil || len(history.Points) != 1 {
				t.Fatal("missing retained point", err)
			}
			if _, err = f.sql.Exec(t.Context(), `DELETE FROM loomplane_bindings WHERE org_id='org' AND id=$1`, binding.ID); err != nil {
				t.Fatal(err)
			}
			if err = f.refresh(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			backend.Credentials = nativeSelectionCredentials{connection: conn, credential: credential}
			s := &recovery.NativeService{Store: recovery.NewPgStore(f.db), Backend: backend, Authz: nativeSelectionAuthorizer{}, Projects: nativeSelectionProjects{}}
			principal := security.Principal{OrgID: "org", UserID: "operator"}
			selected, err := s.CheckPoint(t.Context(), principal, f.source.ID, history.Points[0].ID)
			if err != nil || selected == nil || selected.ID != history.Points[0].ID || !selected.CreatedAt.Equal(created) || !selected.Present || reads != 1 {
				t.Fatalf("selected source history did not survive removal/reopen: %+v %v, native reads %d", selected, err, reads)
			}
			changed = true
			if _, err = s.CheckPoint(t.Context(), principal, f.source.ID, history.Points[0].ID); !errors.Is(err, recovery.ErrNativeUnavailable) {
				t.Fatal("reused native reference with new nanosecond creation admitted", err)
			}
			if reads != 2 {
				t.Fatal("changed point did not use one exact native read", reads)
			}
			conn.Account = "foreign-account"
			if provider == "cloud-sql" {
				conn.Details["project_id"] = "foreign-project"
			}
			if _, err = s.CheckPoint(t.Context(), principal, f.source.ID, history.Points[0].ID); !errors.Is(err, recovery.ErrNativeUnavailable) || reads != 2 {
				t.Fatal("changed reviewed account reached native reads", err, reads)
			}
			if _, err = s.CheckPoint(t.Context(), security.Principal{OrgID: "foreign"}, f.source.ID, history.Points[0].ID); !errors.Is(err, recovery.ErrNotFound) || reads != 2 {
				t.Fatal("foreign organization reached native reads", err, reads)
			}
			stored, err := f.store.GetNativePoint(t.Context(), "org", f.source.ID, history.Points[0].ID)
			if err != nil || stored.Present || !stored.CreatedAt.Equal(created) {
				t.Fatal("read-only selected check rewrote retained inventory", err)
			}
		})
	}
}

func TestNativePointSelectionReadsExactRowAcrossRestart(t *testing.T) {
	f := nativePointHistoryFixture(t)
	points := []recovery.NativePoint{nativeIdentityPoint(t, "2026-10-22T00:00:00.000000001Z"), nativeIdentityPoint(t, "2026-10-22T00:00:00.000000002Z")}
	if err := f.refresh(t.Context(), points); err != nil {
		t.Fatal(err)
	}
	inventory, err := f.store.ReadNativeInventory(t.Context(), "org", f.source.ID, recovery.NativePageInput{})
	if err != nil || len(inventory.Points) != 2 {
		t.Fatal("missing selected point fixture", err)
	}
	store, ok := any(recovery.NewPgStore(f.db)).(selectedNativePointStore)
	if !ok {
		t.Fatal("retained native points cannot be selected by scoped row identity")
	}
	for _, selected := range inventory.Points {
		got, readErr := store.GetNativePoint(t.Context(), "org", f.source.ID, selected.ID)
		if readErr != nil || got == nil || got.ID != selected.ID || got.SourceID != f.source.ID || !got.CreatedAt.Equal(selected.CreatedAt) || got.ProviderID != selected.ProviderID || !got.Present || got.LastSeenAt.IsZero() {
			t.Fatalf("selected exact creation changed across restart: %+v %v", got, readErr)
		}
	}
	for _, scope := range [][3]string{{"foreign-org", f.source.ID, inventory.Points[0].ID}, {"org", "foreign-source", inventory.Points[0].ID}, {"org", f.source.ID, "missing-point"}} {
		if _, readErr := store.GetNativePoint(t.Context(), scope[0], scope[1], scope[2]); !errors.Is(readErr, recovery.ErrNotFound) {
			t.Fatal("selected point escaped its org/source/row scope", scope, readErr)
		}
	}
	// A past complete observation is not current availability. Selection must
	// retain the row so the recovery service can ask the provider again.
	if err = f.refresh(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetNativePoint(t.Context(), "org", f.source.ID, inventory.Points[0].ID)
	if err != nil || got == nil || got.Present {
		t.Fatal("unobserved history could not be selected for fresh native verification", err)
	}
}
