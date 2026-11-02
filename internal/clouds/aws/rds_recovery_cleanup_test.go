package aws

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type rdsRecoveryRemover interface {
	DeleteDBRecovery(context.Context, DBRecoverySpec, DBRecoveryControl) error
}

func TestRDSRecoveryCleanupNeverDeletesSourceOrReplacement(t *testing.T) {
	for _, scenario := range []string{"delete", "resume", "absent-without-intent", "replacement", "source", "foreign-intent", "before-fails", "lost-response", "revoked", "replaced-during-poll", "capture-restore", "capture-fails", "capture-foreign"} {
		t.Run(scenario, func(t *testing.T) {
			intent := strings.Repeat("a", 64)
			name := "lp-recovery-" + intent[:32]
			spec := DBRecoverySpec{ID: name, ResourceID: "db-copy", Intent: intent, SourcePhysicalID: "db-source", Snapshot: DBSnapshot{InstanceID: "source"}}
			if scenario == "source" {
				spec.ResourceID = "db-source"
			}
			writes, reads, before := 0, 0, 0
			captures := 0
			missing := scenario == "resume" || scenario == "absent-without-intent"
			failure := errors.New("cleanup lease lost")
			revoked := scenario == "revoked"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "text/xml")
				switch r.Form.Get("Action") {
				case "DescribeDBInstances":
					reads++
					if missing {
						w.WriteHeader(404)
						fmt.Fprint(w, `<ErrorResponse><Error><Code>DBInstanceNotFound</Code></Error></ErrorResponse>`)
						return
					}
					physical, tag := "db-copy", intent
					if scenario == "replacement" || scenario == "replaced-during-poll" && writes > 0 {
						physical = "db-replacement"
					}
					if scenario == "foreign-intent" || scenario == "capture-foreign" {
						tag = "other"
					}
					fmt.Fprintf(w, `<DescribeDBInstancesResponse><DescribeDBInstancesResult><DBInstances><DBInstance><DBInstanceIdentifier>%s</DBInstanceIdentifier><DbiResourceId>%s</DbiResourceId><DBInstanceArn>arn:aws:rds:us-east-1:123456789012:db:%s</DBInstanceArn><Engine>postgres</Engine><EngineVersion>17.2</EngineVersion><DBInstanceStatus>available</DBInstanceStatus><TagList><Tag><Key>loomplane-native-recovery</Key><Value>%s</Value></Tag></TagList></DBInstance></DBInstances></DescribeDBInstancesResult></DescribeDBInstancesResponse>`, name, physical, name, tag)
				case "DeleteDBInstance":
					writes++
					if r.Form.Get("DBInstanceIdentifier") != name || r.Form.Get("SkipFinalSnapshot") != "true" || r.Form.Get("DeleteAutomatedBackups") != "true" || r.Form.Get("FinalDBSnapshotIdentifier") != "" {
						t.Error("temporary cleanup retained snapshots or removed another database", r.Form)
					}
					missing = scenario != "replaced-during-poll"
					if scenario == "lost-response" {
						w.WriteHeader(500)
						fmt.Fprint(w, `<ErrorResponse><Error><Code>InternalFailure</Code></Error></ErrorResponse>`)
						return
					}
					fmt.Fprintf(w, `<DeleteDBInstanceResponse><DeleteDBInstanceResult><DBInstance><DBInstanceIdentifier>%s</DBInstanceIdentifier><DbiResourceId>db-copy</DbiResourceId></DBInstance></DeleteDBInstanceResult></DeleteDBInstanceResponse>`, name)
				default:
					t.Error("unexpected cleanup call", r.Form.Get("Action"))
					w.WriteHeader(400)
				}
			}))
			defer server.Close()
			client := New(server.URL).Session(&Key{AccessKeyID: "AKIATESTTESTTESTTEST", SecretAccessKey: "test-test-test-test-secret"}, "us-east-1", "123456789012")
			remover, ok := any(client).(rdsRecoveryRemover)
			if !ok {
				t.Fatal("RDS recovery has no exact temporary-copy cleanup")
			}
			control := DBRecoveryControl{Mutation: DBMutation{Attempted: scenario == "resume", Before: func() error {
				before++
				if scenario == "before-fails" {
					return failure
				}
				return nil
			}}, Check: func(ctx context.Context) error {
				if revoked {
					return failure
				}
				return ctx.Err()
			}}
			if strings.HasPrefix(scenario, "capture-") {
				spec.ResourceID = ""
				control.Mutation.Attempted = true
				control.Mutation.Captured = func(id string) error {
					captures++
					if id != "db-copy" {
						t.Error("wrong restore pin", id)
					}
					if scenario == "capture-fails" {
						return failure
					}
					return nil
				}
				inspector, ok := any(client).(interface {
					InspectDBRecoveryForCleanup(context.Context, DBRecoverySpec, DBRecoveryControl) (*DBInstance, error)
				})
				if !ok {
					t.Fatal("RDS lost restore response cannot capture its original cleanup identity")
				}
				observed, observeErr := inspector.InspectDBRecoveryForCleanup(t.Context(), spec, control)
				if scenario == "capture-restore" {
					if observeErr != nil || observed == nil || captures != 1 || writes != 0 {
						t.Fatal("original restore not captured for cleanup", captures, writes, observeErr)
					}
				} else if observeErr == nil || writes != 0 {
					t.Fatal("unverified cleanup capture accepted", observeErr)
				}
				return
			}

			err := remover.DeleteDBRecovery(t.Context(), spec, control)
			switch scenario {
			case "delete":
				if err != nil || writes != 1 || before != 1 || reads < 2 {
					t.Fatal("copy cleanup did not confirm absence", writes, before, reads, err)
				}
			case "resume":
				if err != nil || writes != 0 || before != 0 {
					t.Fatal("confirmed cleanup was reposted", writes, before, err)
				}
			case "lost-response":
				if err == nil || writes != 1 {
					t.Fatal("uncertain cleanup retried", writes, err)
				}
				control.Mutation.Attempted = true
				if err = remover.DeleteDBRecovery(t.Context(), spec, control); err != nil || writes != 1 {
					t.Fatal("cleanup did not reconcile absence", writes, err)
				}
			case "replaced-during-poll":
				if err == nil || writes != 1 {
					t.Fatal("replacement was treated as confirmed absent", writes, err)
				}
			default:
				if err == nil || writes != 0 {
					t.Fatal("unowned cleanup wrote to provider", writes, err)
				}
			}
		})
	}
}
