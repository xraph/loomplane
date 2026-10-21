package aws

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRDSInventoryFollowsOnlyItsPhysicalSource(t *testing.T) {
	for _, scenario := range []string{"pages", "foreign-source", "foreign-account", "foreign-region", "missing-time", "shared", "repeated-marker", "duplicate", "read-denied", "bounded"} {
		t.Run(scenario, func(t *testing.T) {
			reads := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.Form.Get("Action") != "DescribeDBSnapshots" || r.Form.Get("DbiResourceId") != "db-original" || r.Form.Get("DBInstanceIdentifier") != "" || r.Form.Get("IncludePublic") != "false" || r.Form.Get("IncludeShared") != "false" || r.Form.Get("MaxRecords") != "100" {
					t.Errorf("unsafe inventory query: %v", r.Form)
				}
				reads++
				w.Header().Set("Content-Type", "text/xml")
				if scenario == "read-denied" {
					w.WriteHeader(403)
					fmt.Fprint(w, `<ErrorResponse><Error><Code>AccessDenied</Code></Error></ErrorResponse>`)
					return
				}
				id, physical, account, region, kind, created := fmt.Sprintf("snapshot-%d", reads), "db-original", "123456789012", "us-east-1", "manual", "2026-10-20T00:00:00Z"
				switch scenario {
				case "foreign-source":
					physical = "db-foreign"
				case "foreign-account":
					account = "999999999999"
				case "foreign-region":
					region = "us-west-2"
				case "missing-time":
					created = ""
				case "shared":
					kind = "shared"
				case "duplicate":
					id = "snapshot"
				}
				marker := ""
				if reads == 1 || scenario == "repeated-marker" || scenario == "bounded" {
					marker = fmt.Sprintf("page-%d", reads)
				}
				if scenario == "repeated-marker" {
					marker = "page"
				}
				fmt.Fprintf(w, `<DescribeDBSnapshotsResponse><DescribeDBSnapshotsResult><DBSnapshots><DBSnapshot><DBSnapshotIdentifier>%s</DBSnapshotIdentifier><DBSnapshotArn>arn:aws:rds:%s:%s:snapshot:%s</DBSnapshotArn><DBInstanceIdentifier>database</DBInstanceIdentifier><DbiResourceId>%s</DbiResourceId><SnapshotType>%s</SnapshotType><SnapshotCreateTime>%s</SnapshotCreateTime><Status>available</Status><Engine>postgres</Engine><EngineVersion>17.2</EngineVersion><Encrypted>true</Encrypted></DBSnapshot></DBSnapshots><Marker>%s</Marker></DescribeDBSnapshotsResult></DescribeDBSnapshotsResponse>`, id, region, account, id, physical, kind, created, marker)
			}))
			defer srv.Close()
			session := New(srv.URL).Session(&Key{AccessKeyID: "AKIATESTTESTTESTTEST", SecretAccessKey: "secret-test-secret-test"}, "us-east-1", "123456789012")
			points, complete, err := session.ListDBSnapshots(t.Context(), "db-original")
			if scenario == "pages" {
				if err != nil || !complete || len(points) != 2 || reads != 2 {
					t.Fatalf("incomplete inventory: %d %v %v", len(points), complete, err)
				}
				return
			}
			if scenario == "bounded" {
				if err != nil || complete || len(points) != 10 || reads != 10 {
					t.Fatalf("pagination was not bounded: %d %v %v", len(points), complete, err)
				}
				return
			}
			if err == nil || complete || len(points) != 0 {
				t.Fatalf("untrusted inventory published: %d %v %v", len(points), complete, err)
			}
			if strings.Contains(scenario, "foreign") && reads != 1 {
				t.Fatal("followed pagination after identity failure")
			}
		})
	}
}
