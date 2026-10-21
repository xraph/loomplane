package aws

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRDSCreateCapturesAndReconcilesPhysicalIdentity(t *testing.T) {
	for _, scenario := range []string{"new", "before-fails", "capture-fails", "tag-reconcile", "foreign", "vanished", "recreated", "lost-response", "missing-id"} {
		t.Run(scenario, func(t *testing.T) {
			writes, reads, before, captured := 0, 0, 0, 0
			exists := scenario == "tag-reconcile" || scenario == "foreign" || scenario == "recreated"
			physical := "db-original"
			if scenario == "recreated" {
				physical = "db-recreated"
			}
			response := func(id string) string {
				return `<DBInstance><DBInstanceIdentifier>database</DBInstanceIdentifier><DbiResourceId>` + id + `</DbiResourceId><Engine>postgres</Engine><EngineVersion>17.2</EngineVersion><DBInstanceStatus>available</DBInstanceStatus><TagList><Tag><Key>loomplane-operation</Key><Value>intent</Value></Tag></TagList></DBInstance>`
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "text/xml")
				switch r.Form.Get("Action") {
				case "DescribeDBInstances":
					reads++
					if !exists {
						w.WriteHeader(404)
						fmt.Fprint(w, `<ErrorResponse><Error><Code>DBInstanceNotFound</Code></Error></ErrorResponse>`)
						return
					}
					body := response(physical)
					if scenario == "foreign" {
						body = strings.ReplaceAll(body, ">intent<", ">foreign<")
					}
					fmt.Fprint(w, `<DescribeDBInstancesResponse><DescribeDBInstancesResult><DBInstances>`+body+`</DBInstances></DescribeDBInstancesResult></DescribeDBInstancesResponse>`)
				case "CreateDBInstance":
					writes++
					exists = true
					if r.Form.Get("Tags.Tag.1.Key") != "loomplane-operation" || r.Form.Get("Tags.Tag.1.Value") != "intent" {
						t.Error("creation has no stable intent tag")
					}
					if scenario == "lost-response" {
						w.WriteHeader(500)
						fmt.Fprint(w, `<ErrorResponse><Error><Code>InternalFailure</Code></Error></ErrorResponse>`)
						return
					}
					id := physical
					if scenario == "missing-id" {
						id = ""
					}
					fmt.Fprint(w, `<CreateDBInstanceResponse><CreateDBInstanceResult>`+response(id)+`</CreateDBInstanceResult></CreateDBInstanceResponse>`)
				default:
					t.Errorf("unexpected native mutation: %s", r.Form.Get("Action"))
					w.WriteHeader(400)
				}
			}))
			defer srv.Close()
			session := New(srv.URL).Session(&Key{AccessKeyID: "AKIATESTTESTTESTTEST", SecretAccessKey: "secret-test-secret-test"}, "us-east-1", "123456789012")
			interrupted := errors.New("checkpoint unavailable")
			track := DBMutation{Attempted: scenario == "tag-reconcile", Before: func() error {
				before++
				if scenario == "before-fails" {
					return interrupted
				}
				return nil
			}, Captured: func(id string) error {
				captured++
				if id != "db-original" {
					t.Error("captured wrong incarnation")
				}
				if scenario == "capture-fails" {
					return interrupted
				}
				return nil
			}}
			spec := DBInstanceSpec{ID: "database", Engine: "postgres", EngineVersion: "17", Intent: "intent", Class: "db.t4g.micro", StorageGB: 20, User: "app", Password: "password-test-test", Database: "app"}
			if scenario == "vanished" || scenario == "recreated" {
				spec.ResourceID = "db-original"
			}
			got, err := session.EnsureDBInstance(t.Context(), spec, track)
			switch scenario {
			case "new":
				if err != nil || got.ResourceID != "db-original" || writes != 1 || before != 1 || captured != 1 {
					t.Fatalf("creation not durably captured: writes=%d before=%d captured=%d err=%v", writes, before, captured, err)
				}
			case "tag-reconcile":
				if err != nil || writes != 0 || captured != 1 {
					t.Fatalf("lost response was not reconciled by its intent: %d %d %v", writes, captured, err)
				}
			case "before-fails":
				if !errors.Is(err, interrupted) || writes != 0 {
					t.Fatalf("write continued after checkpoint failure: %d %v", writes, err)
				}
			case "capture-fails":
				if !errors.Is(err, interrupted) || writes != 1 || reads != 1 {
					t.Fatalf("polling continued after capture failure: writes=%d reads=%d err=%v", writes, reads, err)
				}
			case "foreign", "vanished", "recreated":
				if err == nil || writes != 0 {
					t.Fatalf("uncertain identity caused a native write: %d %v", writes, err)
				}
			case "lost-response":
				if err == nil || writes != 1 {
					t.Fatalf("SDK replayed an uncertain create: %d %v", writes, err)
				}
				_, err = session.EnsureDBInstance(t.Context(), spec, DBMutation{Attempted: true, Captured: track.Captured})
				if err != nil || writes != 1 || captured != 1 {
					t.Fatalf("uncertain accepted creation did not reconcile: %d %d %v", writes, captured, err)
				}
			case "missing-id":
				if err == nil || captured != 0 {
					t.Fatalf("missing physical ID was accepted: %d %v", captured, err)
				}
			}
		})
	}
}
