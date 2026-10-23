package aws

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type selectedDBSnapshotReader interface {
	ReadDBSnapshot(context.Context, string, DBSnapshot) (*DBSnapshot, error)
}

func TestSelectedDBSnapshotRevalidatesOriginalSourceAndCreation(t *testing.T) {
	for _, scenario := range []string{"original", "reused-source", "changed-point-time", "changed-name", "changed-account", "changed-region", "changed-engine", "changed-version", "changed-kind", "changed-key", "missing-provider-key", "missing-key", "missing-encryption", "changed-encryption", "pending", "missing", "denied"} {
		t.Run(scenario, func(t *testing.T) {
			reads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reads++
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.Form.Get("Action") != "DescribeDBSnapshots" || r.Form.Get("DBSnapshotIdentifier") != "original" || r.Form.Get("DBInstanceIdentifier") != "" {
					t.Errorf("selected read used another source or a write: %v", r.Form)
				}
				name, physical, account, region, kind, created, state, engine, version, encrypted, key := "original", "db-original", "123456789012", "us-east-1", "manual", "2026-10-21T02:00:00Z", "available", "postgres", "17.2", "true", "original-key"
				switch scenario {
				case "reused-source":
					physical = "db-replacement"
				case "changed-point-time":
					created = "2026-10-21T02:00:00.000000001Z"
				case "changed-name":
					name = "replacement"
				case "changed-account":
					account = "999999999999"
				case "changed-region":
					region = "us-west-2"
				case "changed-engine":
					engine = "mysql"
				case "changed-version":
					version = "17.3"
				case "changed-kind":
					kind = "automated"
				case "changed-key":
					key = "replacement-key"
				case "missing-provider-key", "missing-key":
					key = ""
				case "missing-encryption":
					key, encrypted = "", ""
				case "changed-encryption":
					encrypted = "false"
				case "pending":
					state = "creating"
				case "missing":
					w.Header().Set("Content-Type", "text/xml")
					fmt.Fprint(w, `<DescribeDBSnapshotsResponse><DescribeDBSnapshotsResult><DBSnapshots/></DescribeDBSnapshotsResult></DescribeDBSnapshotsResponse>`)
					return
				case "denied":
					w.WriteHeader(http.StatusForbidden)
					fmt.Fprint(w, `<ErrorResponse><Error><Code>AccessDenied</Code></Error></ErrorResponse>`)
					return
				}
				w.Header().Set("Content-Type", "text/xml")
				body := fmt.Sprintf(`<DescribeDBSnapshotsResponse><DescribeDBSnapshotsResult><DBSnapshots><DBSnapshot><DBSnapshotIdentifier>%s</DBSnapshotIdentifier><DBSnapshotArn>arn:aws:rds:%s:%s:snapshot:%s</DBSnapshotArn><DBInstanceIdentifier>database</DBInstanceIdentifier><DbiResourceId>%s</DbiResourceId><SnapshotType>%s</SnapshotType><SnapshotCreateTime>%s</SnapshotCreateTime><Status>%s</Status><Engine>%s</Engine><EngineVersion>%s</EngineVersion><Encrypted>%s</Encrypted><KmsKeyId>%s</KmsKeyId></DBSnapshot></DBSnapshots></DescribeDBSnapshotsResult></DescribeDBSnapshotsResponse>`, name, region, account, name, physical, kind, created, state, engine, version, encrypted, key)
				if scenario == "missing-encryption" {
					body = strings.ReplaceAll(body, "<Encrypted></Encrypted>", "")
				}
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			s := New(server.URL).Session(&Key{AccessKeyID: "AKIATESTTESTTESTTEST", SecretAccessKey: "secret-test-secret-test"}, "us-east-1", "123456789012")
			reader, ok := any(s).(selectedDBSnapshotReader)
			if !ok {
				t.Fatal("selected RDS recovery points cannot be reread by exact reference")
			}
			created, err := time.Parse(time.RFC3339Nano, "2026-10-21T02:00:00Z")
			if err != nil {
				t.Fatal(err)
			}
			selected := DBSnapshot{ID: "original", ARN: "arn:aws:rds:us-east-1:123456789012:snapshot:original", InstanceID: "database", ResourceID: "db-original", Type: "manual", CreatedAt: created, Engine: "postgres", EngineVersion: "17.2", Encrypted: true, KMSKey: "original-key"}
			if scenario == "missing-key" {
				selected.KMSKey = ""
			}
			if scenario == "missing-encryption" {
				selected.Encrypted, selected.KMSKey = false, ""
			}
			point, err := reader.ReadDBSnapshot(t.Context(), "db-original", selected)
			if scenario == "original" {
				if err != nil || point == nil || point.ARN != selected.ARN || !point.CreatedAt.Equal(selected.CreatedAt) || point.Status != "available" {
					t.Fatalf("original point unavailable: %+v %v", point, err)
				}
			} else if err == nil || point != nil {
				t.Fatalf("changed or unavailable point admitted: %+v %v", point, err)
			}
			wantedReads := 1
			if scenario == "missing-key" {
				wantedReads = 0
			}
			if reads != wantedReads {
				t.Fatal("selected read did not use exactly one snapshot request", reads)
			}
		})
	}
}

func TestSelectedDBSnapshotRejectsForeignInputBeforeReads(t *testing.T) {
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reads++ }))
	defer server.Close()
	s := New(server.URL).Session(&Key{AccessKeyID: "AKIATESTTESTTESTTEST", SecretAccessKey: "secret-test-secret-test"}, "us-east-1", "123456789012")
	reader, ok := any(s).(selectedDBSnapshotReader)
	if !ok {
		t.Fatal("selected RDS point validation is missing")
	}
	for _, selected := range []DBSnapshot{{}, {ID: "original", ResourceID: "db-original", ARN: "arn:aws:rds:us-west-2:123456789012:snapshot:original"}, {ID: "original", ResourceID: "db-original", ARN: "arn:aws:rds:us-east-1:999999999999:snapshot:original"}, {ID: "escaped%2fpath", ResourceID: "db-original", ARN: "arn:aws:rds:us-east-1:123456789012:snapshot:escaped%2fpath"}} {
		if _, err := reader.ReadDBSnapshot(t.Context(), "db-original", selected); err == nil {
			t.Fatal("foreign selected point accepted", selected.ARN)
		}
	}
	if reads != 0 {
		t.Fatal("invalid selected point reached AWS", reads)
	}
}
