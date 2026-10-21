package aws

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRDSRemovalWaitsForTheOriginalFinalSnapshot(t *testing.T) {
	for _, scenario := range []string{"complete", "resume", "before-fails", "accepted-fails", "foreign-instance", "foreign-snapshot", "missing-snapshot", "snapshot-failed", "read-denied", "preexisting-snapshot", "delete-refused", "lost-response"} {
		t.Run(scenario, func(t *testing.T) {
			deletes, before, accepted := 0, 0, 0
			attempted := scenario == "resume"
			gone := attempted
			snapshotID := FinalDBSnapshotID("db-original")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "text/xml")
				switch r.Form.Get("Action") {
				case "DescribeDBInstances":
					if gone {
						w.WriteHeader(404)
						fmt.Fprint(w, `<ErrorResponse><Error><Code>DBInstanceNotFound</Code></Error></ErrorResponse>`)
						return
					}
					physical := "db-original"
					if scenario == "foreign-instance" {
						physical = "db-recreated"
					}
					fmt.Fprintf(w, `<DescribeDBInstancesResponse><DescribeDBInstancesResult><DBInstances><DBInstance><DBInstanceIdentifier>database</DBInstanceIdentifier><DbiResourceId>%s</DbiResourceId><DBInstanceStatus>available</DBInstanceStatus></DBInstance></DBInstances></DescribeDBInstancesResult></DescribeDBInstancesResponse>`, physical)
				case "DescribeDBSnapshots":
					if scenario == "read-denied" {
						w.WriteHeader(403)
						fmt.Fprint(w, `<ErrorResponse><Error><Code>AccessDenied</Code></Error></ErrorResponse>`)
						return
					}
					if (!gone && scenario != "preexisting-snapshot") || scenario == "missing-snapshot" {
						w.WriteHeader(404)
						fmt.Fprint(w, `<ErrorResponse><Error><Code>DBSnapshotNotFound</Code></Error></ErrorResponse>`)
						return
					}
					physical := "db-original"
					if scenario == "foreign-snapshot" {
						physical = "db-other"
					}
					status := "available"
					if scenario == "snapshot-failed" {
						status = "failed"
					}
					fmt.Fprintf(w, `<DescribeDBSnapshotsResponse><DescribeDBSnapshotsResult><DBSnapshots><DBSnapshot><DBSnapshotIdentifier>%s</DBSnapshotIdentifier><DBSnapshotArn>arn:aws:rds:us-east-1:123456789012:snapshot:%s</DBSnapshotArn><DBInstanceIdentifier>database</DBInstanceIdentifier><DbiResourceId>%s</DbiResourceId><Status>%s</Status><SnapshotType>manual</SnapshotType></DBSnapshot></DBSnapshots></DescribeDBSnapshotsResult></DescribeDBSnapshotsResponse>`, snapshotID, snapshotID, physical, status)
				case "DeleteDBInstance":
					deletes++
					if scenario == "delete-refused" {
						w.WriteHeader(400)
						fmt.Fprint(w, `<ErrorResponse><Error><Code>InvalidDBInstanceState</Code><Message>cannot snapshot</Message></Error></ErrorResponse>`)
						return
					}
					gone = true
					if r.Form.Get("SkipFinalSnapshot") != "false" || r.Form.Get("DeleteAutomatedBackups") != "false" || r.Form.Get("FinalDBSnapshotIdentifier") != snapshotID {
						t.Error("removal weakened recovery protection")
					}
					if scenario == "lost-response" {
						w.WriteHeader(500)
						fmt.Fprint(w, `<ErrorResponse><Error><Code>InternalFailure</Code></Error></ErrorResponse>`)
						return
					}
					fmt.Fprint(w, `<DeleteDBInstanceResponse><DeleteDBInstanceResult><DBInstance><DBInstanceIdentifier>database</DBInstanceIdentifier><DbiResourceId>db-original</DbiResourceId><DBInstanceStatus>deleting</DBInstanceStatus></DBInstance></DeleteDBInstanceResult></DeleteDBInstanceResponse>`)
				default:
					t.Errorf("unexpected native action: %s", r.Form.Get("Action"))
					w.WriteHeader(400)
				}
			}))
			defer srv.Close()
			session := New(srv.URL).Session(&Key{AccessKeyID: "AKIATESTTESTTESTTEST", SecretAccessKey: "secret-test-secret-test"}, "us-east-1", "123456789012")
			sentinel := errors.New("checkpoint unavailable")
			track := DBRemoval{Attempted: attempted, Before: func() error {
				before++
				if scenario == "before-fails" {
					return sentinel
				}
				return nil
			}, Accepted: func() error {
				accepted++
				if scenario == "accepted-fails" {
					return sentinel
				}
				return nil
			}}
			ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
			defer cancel()
			spec := DBRemovalSpec{ID: "database", ResourceID: "db-original", SnapshotID: snapshotID}
			point, err := session.DeleteDBInstance(ctx, spec, track)
			switch scenario {
			case "complete", "resume":
				if err != nil || point.ResourceID != "db-original" || point.Status != "available" {
					t.Fatalf("unverified final snapshot: %+v %v", point, err)
				}
				want := 1
				if attempted {
					want = 0
				}
				if deletes != want {
					t.Fatalf("replayed deletion: %d", deletes)
				}
			case "before-fails":
				if !errors.Is(err, sentinel) || deletes != 0 {
					t.Fatalf("mutation after checkpoint failure: %d %v", deletes, err)
				}
			case "accepted-fails":
				if !errors.Is(err, sentinel) || deletes != 1 {
					t.Fatalf("continued past uncaptured request: %d %v", deletes, err)
				}
			case "foreign-instance", "read-denied", "preexisting-snapshot":
				if err == nil || deletes != 0 {
					t.Fatalf("unsafe source/snapshot caused deletion: %d %v", deletes, err)
				}
			case "foreign-snapshot", "missing-snapshot", "snapshot-failed":
				if err == nil || point != nil || deletes != 1 {
					t.Fatalf("missing recovery proof reported completion: %+v %d %v", point, deletes, err)
				}
			case "delete-refused":
				if err == nil || deletes != 1 || accepted != 0 {
					t.Fatalf("snapshot refusal was ignored: %d %v", deletes, err)
				}
			case "lost-response":
				if err == nil || deletes != 1 || accepted != 0 {
					t.Fatalf("SDK replayed or accepted uncertain deletion: %d %v", deletes, err)
				}
				point, err = session.DeleteDBInstance(t.Context(), spec, DBRemoval{Attempted: true})
				if err != nil || point == nil || deletes != 1 {
					t.Fatalf("original final snapshot did not reconcile: %+v %d %v", point, deletes, err)
				}
			}
		})
	}
}

func TestFinalRDSSnapshotNamesFollowPhysicalIncarnations(t *testing.T) {
	one := FinalDBSnapshotID("db-original")
	if one != FinalDBSnapshotID("db-original") || one == FinalDBSnapshotID("db-recreated") || !strings.HasPrefix(one, "lp-final-") {
		t.Fatal("snapshot identity is reusable or unstable")
	}
}
