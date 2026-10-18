package aws

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"
)

func TestRDSRemovalRetainsRecoveryData(t *testing.T) {
	for _, state := range []string{"available", "deleting", "missing", "failed"} {
		t.Run(state, func(t *testing.T) {
			var deletions []url.Values
			physical := "db-physical-one"
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
					return
				}
				w.Header().Set("Content-Type", "text/xml")
				switch r.Form.Get("Action") {
				case "DescribeDBInstances":
					if state == "missing" {
						w.WriteHeader(http.StatusNotFound)
						fmt.Fprint(w, `<ErrorResponse><Error><Code>DBInstanceNotFound</Code><Message>gone</Message></Error></ErrorResponse>`)
						return
					}
					fmt.Fprintf(w, `<DescribeDBInstancesResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><DescribeDBInstancesResult><DBInstances><DBInstance><DBInstanceIdentifier>database</DBInstanceIdentifier><DbiResourceId>%s</DbiResourceId><DBInstanceStatus>%s</DBInstanceStatus></DBInstance></DBInstances></DescribeDBInstancesResult></DescribeDBInstancesResponse>`, physical, state)
				case "DeleteDBInstance":
					deletions = append(deletions, r.Form)
					if state == "failed" {
						w.WriteHeader(http.StatusBadRequest)
						fmt.Fprint(w, `<ErrorResponse><Error><Code>InvalidDBInstanceState</Code><Message>cannot snapshot</Message></Error></ErrorResponse>`)
						return
					}
					fmt.Fprint(w, `<DeleteDBInstanceResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><DeleteDBInstanceResult><DBInstance><DBInstanceStatus>deleting</DBInstanceStatus></DBInstance></DeleteDBInstanceResult></DeleteDBInstanceResponse>`)
				default:
					t.Errorf("unexpected API action: %s", r.Form.Get("Action"))
				}
			}))
			defer srv.Close()
			s := New(srv.URL).Session(&Key{AccessKeyID: "AKIATESTTESTTESTTEST", SecretAccessKey: "secret-test-secret-test"}, "us-east-1", "account")
			err := s.DeleteDBInstance(t.Context(), "database")
			if state == "failed" {
				if err == nil {
					t.Fatal("snapshot refusal was ignored")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if state == "missing" || state == "deleting" {
				if len(deletions) != 0 {
					t.Fatal("replayed deletion after it was accepted")
				}
				return
			}
			if len(deletions) != 1 {
				t.Fatalf("delete attempts: %d", len(deletions))
			}
			in := deletions[0]
			if in.Get("SkipFinalSnapshot") != "false" || in.Get("DeleteAutomatedBackups") != "false" || !regexp.MustCompile(`^lp-final-[a-z0-9]+$`).MatchString(in.Get("FinalDBSnapshotIdentifier")) {
				t.Fatalf("removal destroyed recovery data: %v", in)
			}
			if state == "available" {
				if err := s.DeleteDBInstance(t.Context(), "database"); err != nil {
					t.Fatal(err)
				}
				physical = "db-recreated"
				if err := s.DeleteDBInstance(t.Context(), "database"); err != nil {
					t.Fatal(err)
				}
				if len(deletions) != 3 || deletions[0].Get("FinalDBSnapshotIdentifier") != deletions[1].Get("FinalDBSnapshotIdentifier") || deletions[0].Get("FinalDBSnapshotIdentifier") == deletions[2].Get("FinalDBSnapshotIdentifier") {
					t.Fatalf("unstable or reused snapshot identity: %v", deletions)
				}
			}
		})
	}
}
