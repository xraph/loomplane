package deployments

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/internal/security"
)

func TestNativePointDeletionAdapterUsesOriginalAccountAndRetainsUncertainty(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs}))
	for _, provider := range []string{"rds", "cloud-sql"} {
		for _, scenario := range []string{"complete", "lost-response", "denied", "intent-failed", "foreign-account", "foreign-actor", "automated"} {
			t.Run(provider+"/"+scenario, func(t *testing.T) {
				j := managedDatabaseJob(t, provider)
				j.Binding.OrgID, j.Connection.OrgID = "org", "org"
				j.Binding.ProjectID, j.Binding.EnvironmentID, j.Binding.Subject, j.Binding.Name = "project", "environment", "resource", "database"
				j.Binding.ExternalID, j.Binding.PhysicalID = "database", "db-original"
				created := time.Date(2026, 10, 21, 2, 0, 0, 0, time.UTC)
				source := &gcp.SQLInstance{Name: "database", Project: "project", Region: "us-east-1", ConnectionName: "project:us-east-1:database", CreateTime: "2026-10-21T00:00:00Z"}
				if provider == "cloud-sql" {
					j.Binding.ExternalID = source.ConnectionName
					j.Binding.PhysicalID, err = source.Incarnation("project", "us-east-1", "database")
					if err != nil {
						t.Fatal(err)
					}
				}
				point := recovery.NativePoint{ID: "point", SourceID: "source", ProviderID: "arn:aws:rds:us-east-1:123456789012:snapshot:original", Name: "original", Kind: "manual", Status: "available", Engine: "postgres", Version: "17.2", CreatedAt: created, Encrypted: true, KMSKey: "original-key"}
				if provider == "cloud-sql" {
					point.ProviderID, point.Kind, point.Status, point.Version = "projects/project/backups/original", "FINAL", "SUCCESSFUL", "17"
				}
				if scenario == "automated" {
					point.Kind = "automated"
					if provider == "cloud-sql" {
						point.Kind = "AUTOMATED"
					}
				}
				exists, intent := true, false
				writes, rejected := 0, 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/token" {
						w.Header().Set("Content-Type", "application/json")
						_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "native-delete-test-token", "token_type": "Bearer", "expires_in": 3600})
						return
					}
					if provider == "cloud-sql" {
						if r.URL.Path != "/v1/projects/project/backups/original" {
							t.Error("foreign native backup path", r.URL)
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						if r.Method == http.MethodGet {
							if !exists {
								w.WriteHeader(http.StatusNotFound)
								return
							}
							_ = json.NewEncoder(w).Encode(map[string]any{"name": point.ProviderID, "kind": "sql#backup", "instance": "database", "type": "FINAL", "state": "SUCCESSFUL", "databaseVersion": "POSTGRES_17", "kmsKey": "original-key", "backupInterval": map[string]string{"startTime": created.Format(time.RFC3339Nano), "endTime": created.Add(time.Minute).Format(time.RFC3339Nano)}, "instanceSettings": source})
							return
						}
					} else {
						if parseErr := r.ParseForm(); parseErr != nil {
							t.Error(parseErr)
						}
						w.Header().Set("Content-Type", "text/xml")
						if r.Form.Get("Action") == "DescribeDBSnapshots" {
							if !exists {
								w.WriteHeader(http.StatusNotFound)
								fmt.Fprint(w, `<ErrorResponse><Error><Code>DBSnapshotNotFound</Code></Error></ErrorResponse>`)
								return
							}
							fmt.Fprintf(w, `<DescribeDBSnapshotsResponse><DescribeDBSnapshotsResult><DBSnapshots><DBSnapshot><DBSnapshotIdentifier>original</DBSnapshotIdentifier><DBSnapshotArn>%s</DBSnapshotArn><DBInstanceIdentifier>database</DBInstanceIdentifier><DbiResourceId>db-original</DbiResourceId><SnapshotType>manual</SnapshotType><SnapshotCreateTime>%s</SnapshotCreateTime><Status>available</Status><Engine>postgres</Engine><EngineVersion>17.2</EngineVersion><Encrypted>true</Encrypted><KmsKeyId>original-key</KmsKeyId></DBSnapshot></DBSnapshots></DescribeDBSnapshotsResult></DescribeDBSnapshotsResponse>`, point.ProviderID, created.Format(time.RFC3339Nano))
							return
						}
						if r.Form.Get("Action") != "DeleteDBSnapshot" || r.Form.Get("DBSnapshotIdentifier") != "original" {
							t.Error("foreign native deletion", r.Form)
						}
					}
					writes++
					if !intent {
						t.Error("native write preceded its retained intent")
					}
					if scenario == "denied" {
						w.WriteHeader(http.StatusForbidden)
						if provider == "rds" {
							fmt.Fprint(w, `<ErrorResponse><Error><Code>AccessDenied</Code></Error></ErrorResponse>`)
						}
						return
					}
					exists = false
					if scenario == "lost-response" {
						w.WriteHeader(http.StatusInternalServerError)
						if provider == "rds" {
							fmt.Fprint(w, `<ErrorResponse><Error><Code>InternalFailure</Code></Error></ErrorResponse>`)
						}
						return
					}
					if provider == "rds" {
						fmt.Fprint(w, `<DeleteDBSnapshotResponse><DeleteDBSnapshotResult/></DeleteDBSnapshotResponse>`)
					} else {
						fmt.Fprint(w, `{"name":"delete-original","status":"DONE"}`)
					}
				}))
				defer server.Close()
				credential := []byte(`{"access_key_id":"AKIATESTTESTTESTTEST","secret_access_key":"secret-test-secret-test"}`)
				if provider == "cloud-sql" {
					credential, err = json.Marshal(map[string]string{"type": "service_account", "project_id": "project", "client_email": "native-delete@example.com", "private_key": keyPEM, "token_uri": server.URL + "/token"})
					if err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "foreign-account" {
					if provider == "cloud-sql" {
						j.Connection.Details["project_id"] = "other"
					} else {
						j.Connection.Account = "other"
					}
				}
				backend := &NativeDatabases{AWS: aws.New(server.URL), Google: gcp.New(server.URL), Credentials: &nativeInventoryCredentials{connection: j.Connection, credential: credential}}
				deleter, ok := any(backend).(recovery.NativeDeletionBackend)
				if !ok {
					t.Fatal("native database backend has no point deletion adapter")
				}
				record := recovery.NativeRetirement{ID: "retirement", OrgID: "org", ProjectID: "project", SourceID: "source", PointID: point.ID, CreatedBy: "operator", Source: *j.Binding, Point: point}
				p := security.Principal{OrgID: "org", UserID: "operator"}
				if scenario == "foreign-actor" {
					p.UserID = "other"
				}
				control := recovery.NativeDeletionControl{Check: func(context.Context) error { return nil }, Before: func() error {
					if scenario == "intent-failed" {
						return errors.New("checkpoint unavailable")
					}
					intent = true
					return nil
				}, Rejected: func() error { rejected++; intent = false; return nil }}
				absent, err := deleter.DeleteNativePoint(t.Context(), p, record, control)
				if scenario == "lost-response" {
					if absent || !errors.Is(err, recovery.ErrNativePending) || writes != 1 {
						t.Fatal("lost native response was not retained", absent, err, writes)
					}
					record.Attempted = true
					absent, err = deleter.DeleteNativePoint(t.Context(), p, record, control)
				}
				success := scenario == "complete" || scenario == "lost-response"
				if absent != success || (err == nil) != success {
					t.Fatal("incorrect original native deletion result", absent, err)
				}
				want := 0
				if success || scenario == "denied" {
					want = 1
				}
				if writes != want {
					t.Fatal("native write repeated or missing", writes, want)
				}
				if scenario == "denied" && (rejected != 1 || intent) {
					t.Fatal("known native rejection cannot safely retry", rejected, intent)
				}
			})
		}
	}
}
