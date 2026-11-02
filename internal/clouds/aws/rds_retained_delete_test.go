package aws

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type retainedSnapshotDeleter interface {
	DeleteRetainedDBSnapshot(context.Context, string, DBSnapshot, DBRecoveryControl) error
}

func TestRetainedRDSSnapshotDeletionPinsOriginalAndNeverReposts(t *testing.T) {
	for _, scenario := range []string{"complete", "lost-response", "missing-before-intent", "automated", "replacement", "changed-after-intent", "intent-failed", "guard-revoked", "denied"} {
		t.Run(scenario, func(t *testing.T) {
			created := time.Date(2026, 10, 21, 2, 0, 0, 0, time.UTC)
			writes, checks := 0, 0
			absent, intent := scenario == "missing-before-intent", false
			observedTime := created
			if scenario == "replacement" {
				observedTime = created.Add(time.Second)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "text/xml")
				switch r.Form.Get("Action") {
				case "DescribeDBSnapshots":
					if absent {
						w.WriteHeader(http.StatusNotFound)
						fmt.Fprint(w, `<ErrorResponse><Error><Code>DBSnapshotNotFound</Code></Error></ErrorResponse>`)
						return
					}
					fmt.Fprintf(w, `<DescribeDBSnapshotsResponse><DescribeDBSnapshotsResult><DBSnapshots><DBSnapshot><DBSnapshotIdentifier>original</DBSnapshotIdentifier><DBSnapshotArn>arn:aws:rds:us-east-1:123456789012:snapshot:original</DBSnapshotArn><DBInstanceIdentifier>database</DBInstanceIdentifier><DbiResourceId>db-original</DbiResourceId><SnapshotType>manual</SnapshotType><SnapshotCreateTime>%s</SnapshotCreateTime><Status>available</Status><Engine>postgres</Engine><EngineVersion>17.2</EngineVersion><Encrypted>true</Encrypted><KmsKeyId>original-key</KmsKeyId></DBSnapshot></DBSnapshots></DescribeDBSnapshotsResult></DescribeDBSnapshotsResponse>`, observedTime.Format(time.RFC3339Nano))
				case "DeleteDBSnapshot":
					writes++
					if !intent || r.Form.Get("DBSnapshotIdentifier") != "original" {
						t.Error("native deletion lost durable original intent")
					}
					if scenario == "denied" {
						w.WriteHeader(http.StatusForbidden)
						fmt.Fprint(w, `<ErrorResponse><Error><Code>AccessDenied</Code></Error></ErrorResponse>`)
						return
					}
					absent = true
					if scenario == "lost-response" {
						w.WriteHeader(http.StatusInternalServerError)
						fmt.Fprint(w, `<ErrorResponse><Error><Code>InternalFailure</Code></Error></ErrorResponse>`)
						return
					}
					fmt.Fprint(w, `<DeleteDBSnapshotResponse><DeleteDBSnapshotResult/></DeleteDBSnapshotResponse>`)
				default:
					t.Error("unexpected native call", r.Form)
				}
			}))
			defer server.Close()
			s := New(server.URL).Session(&Key{AccessKeyID: "AKIATESTTESTTESTTEST", SecretAccessKey: "secret-test-secret-test"}, "us-east-1", "123456789012")
			deleter, ok := any(s).(retainedSnapshotDeleter)
			if !ok {
				t.Fatal("native retained snapshot deletion is missing")
			}
			selected := DBSnapshot{ID: "original", ARN: "arn:aws:rds:us-east-1:123456789012:snapshot:original", InstanceID: "database", ResourceID: "db-original", Type: "manual", CreatedAt: created, Engine: "postgres", EngineVersion: "17.2", Encrypted: true, KMSKey: "original-key"}
			if scenario == "automated" {
				selected.Type = "automated"
			}
			control := DBRecoveryControl{Check: func(context.Context) error {
				checks++
				if scenario == "guard-revoked" && checks > 1 {
					return errors.New("revoked")
				}
				return nil
			}, Mutation: DBMutation{Before: func() error {
				if scenario == "intent-failed" {
					return errors.New("checkpoint unavailable")
				}
				intent = true
				if scenario == "changed-after-intent" {
					observedTime = created.Add(time.Second)
				}
				return nil
			}}}
			err := deleter.DeleteRetainedDBSnapshot(t.Context(), "db-original", selected, control)
			if scenario == "lost-response" {
				if err == nil || writes != 1 {
					t.Fatal("lost response was retried or hidden", writes, err)
				}
				control.Mutation.Attempted = true
				err = deleter.DeleteRetainedDBSnapshot(t.Context(), "db-original", selected, control)
			}
			success := scenario == "complete" || scenario == "lost-response" || scenario == "missing-before-intent"
			if (err == nil) != success {
				t.Fatal("incorrect native deletion result", err)
			}
			if success && !intent {
				t.Fatal("absence was accepted without durable intent")
			}
			want := 0
			if scenario == "complete" || scenario == "lost-response" || scenario == "denied" {
				want = 1
			}
			if writes != want {
				t.Fatal("wrong number of native deletions", writes, want)
			}
		})
	}
}
