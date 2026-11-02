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

type rdsRecoveryPasswordSetter interface {
	SetDBRecoveryPassword(context.Context, DBRecoverySpec, string, DBRecoveryControl) error
}

func TestRDSRecoveryPasswordChangesOnlyPinnedCopyOnce(t *testing.T) {
	for _, scenario := range []string{"new", "resume", "before-fails", "lost-response", "replacement", "revoked", "invalid-password"} {
		t.Run(scenario, func(t *testing.T) {
			intent := strings.Repeat("a", 64)
			name := "lp-recovery-" + intent[:32]
			kms := "arn:aws:kms:us-east-1:123456789012:key/original"
			spec := DBRecoverySpec{ID: name, ResourceID: "db-copy", Intent: intent, SourcePhysicalID: "db-source", Snapshot: DBSnapshot{ID: "original", ARN: "arn:aws:rds:us-east-1:123456789012:snapshot:original", ResourceID: "db-source", InstanceID: "source", Engine: "postgres", EngineVersion: "17.2", Encrypted: true, KMSKey: kms}, Class: "db.t4g.micro", StorageType: "gp3", StorageGB: 20, VPCID: "vpc-recovery", SubnetGroup: "recovery-private", SecurityGroupID: "sg-copy", VerifierGroupID: "sg-worker", ParameterGroup: "default.postgres17", AdminUser: "loomplane"}
			writes, before := 0, 0
			password := strings.Repeat("A", 52)
			if scenario == "invalid-password" {
				password = "unsafe/password"
			}
			failure := errors.New("password checkpoint refused")
			revoked := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "text/xml")
				switch r.Form.Get("Action") {
				case "DescribeDBInstances":
					physical := "db-copy"
					if scenario == "replacement" {
						physical = "replacement"
					}
					fmt.Fprint(w, `<DescribeDBInstancesResponse><DescribeDBInstancesResult><DBInstances>`+rdsRecoveryInstanceXML(name, physical, intent, "false", kms)+`</DBInstances></DescribeDBInstancesResult></DescribeDBInstancesResponse>`)
				case "ModifyDBInstance":
					writes++
					if r.Form.Get("DBInstanceIdentifier") != name || r.Form.Get("MasterUserPassword") != password || r.Form.Get("ApplyImmediately") != "true" || len(r.Form) != 5 {
						t.Error("password reset changed unreviewed settings", r.Form.Get("DBInstanceIdentifier"), len(r.Form))
					}
					if scenario == "lost-response" {
						w.WriteHeader(500)
						fmt.Fprint(w, `<ErrorResponse><Error><Code>InternalFailure</Code></Error></ErrorResponse>`)
						return
					}
					fmt.Fprint(w, `<ModifyDBInstanceResponse><ModifyDBInstanceResult>`+rdsRecoveryInstanceXML(name, "db-copy", intent, "false", kms)+`</ModifyDBInstanceResult></ModifyDBInstanceResponse>`)
				default:
					t.Error("unexpected password update call", r.Form.Get("Action"))
					w.WriteHeader(400)
				}
			}))
			defer server.Close()
			session := New(server.URL).Session(&Key{AccessKeyID: "AKIATESTTESTTESTTEST", SecretAccessKey: "test-test-test-test-secret"}, "us-east-1", "123456789012")
			setter, ok := any(session).(rdsRecoveryPasswordSetter)
			if !ok {
				t.Fatal("RDS recovery cannot rotate the temporary administrator")
			}
			control := DBRecoveryControl{Mutation: DBMutation{Attempted: scenario == "resume", Before: func() error {
				before++
				if scenario == "before-fails" {
					return failure
				}
				if scenario == "revoked" {
					revoked = true
				}
				return nil
			}}, Check: func(ctx context.Context) error {
				if revoked {
					return failure
				}
				return ctx.Err()
			}}
			err := setter.SetDBRecoveryPassword(t.Context(), spec, password, control)
			switch scenario {
			case "new":
				if err != nil || writes != 1 || before != 1 {
					t.Fatal("password update did not capture intent", writes, before, err)
				}
			case "resume":
				if err != nil || writes != 0 {
					t.Fatal("password update was replayed", writes, err)
				}
			case "lost-response":
				if err == nil || writes != 1 {
					t.Fatal("uncertain password update retried", writes, err)
				}
				control.Mutation.Attempted = true
				if err = setter.SetDBRecoveryPassword(t.Context(), spec, password, control); err != nil || writes != 1 {
					t.Fatal("password continuation replayed mutation", writes, err)
				}
			default:
				if err == nil || writes != 0 {
					t.Fatal("unowned password update wrote to provider", writes, err)
				}
			}
		})
	}
}
