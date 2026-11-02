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

type dbPITRRestorer interface {
	RestoreDBPointInTime(context.Context, DBRecoverySpec, DBRecoveryControl) (*DBInstance, error)
}

func TestRDSPITRRestoresExactTimeOnceWithPrivateReviewedSettings(t *testing.T) {
	for _, scenario := range []string{"new", "resume-expired", "lost-response", "capture-fails", "intent-fails", "latest", "expired", "changed-epoch", "wrong-master", "oversized", "foreign-intent", "replacement", "public-copy", "public-egress", "unsafe-iops", "unsafe-autoscale", "unsafe-iam", "rejected-time", "rejected-parameters"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			intent := strings.Repeat("b", 64)
			name := "lp-recovery-" + intent[:32]
			point := DBPITRWindow{ARN: "arn:aws:rds:us-east-1:123456789012:db:orders", InstanceID: "orders", ResourceID: "db-source", Engine: "postgres", EngineVersion: "17.2", CreatedAt: now.Add(-30 * 24 * time.Hour), Earliest: now.Add(-7 * 24 * time.Hour), Latest: now.Add(-time.Minute), Encrypted: true, EncryptionKnown: true, KMSKey: "arn:aws:kms:us-east-1:123456789012:key/original", MasterUser: "loomplane", StorageGB: 20}
			at := now.Add(-3 * 24 * time.Hour)
			if scenario == "latest" {
				at = point.Latest
			}
			spec := DBRecoverySpec{ID: name, Intent: intent, SourcePhysicalID: "db-source", PITR: &point, RestoreTime: at, Snapshot: DBSnapshot{InstanceID: point.InstanceID, ResourceID: point.ResourceID, Engine: point.Engine, EngineVersion: point.EngineVersion, Encrypted: true, KMSKey: point.KMSKey}, Class: "db.t4g.micro", StorageGB: 20, StorageType: "gp3", VPCID: "vpc-recovery", SubnetGroup: "recovery-private", SecurityGroupID: "sg-copy", VerifierGroupID: "sg-worker", ParameterGroup: "default.postgres17", AdminUser: "loomplane"}
			exists := scenario == "resume-expired" || scenario == "foreign-intent" || scenario == "replacement" || scenario == "public-copy" || scenario == "unsafe-iops" || scenario == "unsafe-autoscale" || scenario == "unsafe-iam"
			if scenario == "replacement" {
				spec.ResourceID = "db-original-copy"
			}
			writes, checkpoints, captures, windowReads := 0, 0, 0, 0
			instance := func() string {
				physical, tag, public, iops := "db-copy", intent, "false", "3000"
				if scenario == "foreign-intent" {
					tag = "foreign"
				}
				if scenario == "replacement" {
					physical = "db-replacement"
				}
				if scenario == "public-copy" {
					public = "true"
				}
				if scenario == "unsafe-iops" {
					iops = "12000"
				}
				xml := rdsRecoveryInstanceXML(name, physical, tag, public, point.KMSKey)
				xml = strings.Replace(xml, "</DBInstance>", "<Iops>"+iops+"</Iops><StorageThroughput>125</StorageThroughput><MaxAllocatedStorage>20</MaxAllocatedStorage><IAMDatabaseAuthenticationEnabled>false</IAMDatabaseAuthenticationEnabled></DBInstance>", 1)
				if scenario == "unsafe-autoscale" {
					xml = strings.Replace(xml, "<MaxAllocatedStorage>20</MaxAllocatedStorage>", "<MaxAllocatedStorage>100</MaxAllocatedStorage>", 1)
				}
				if scenario == "unsafe-iam" {
					xml = strings.Replace(xml, "<IAMDatabaseAuthenticationEnabled>false</IAMDatabaseAuthenticationEnabled>", "<IAMDatabaseAuthenticationEnabled>true</IAMDatabaseAuthenticationEnabled>", 1)
				}
				return xml
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "text/xml")
				switch r.Form.Get("Action") {
				case "DescribeDBInstances":
					if !exists {
						w.WriteHeader(http.StatusNotFound)
						fmt.Fprint(w, `<ErrorResponse><Error><Code>DBInstanceNotFound</Code></Error></ErrorResponse>`)
						return
					}
					fmt.Fprint(w, `<DescribeDBInstancesResponse><DescribeDBInstancesResult><DBInstances>`+instance()+`</DBInstances></DescribeDBInstancesResult></DescribeDBInstancesResponse>`)
				case "DescribeDBInstanceAutomatedBackups":
					windowReads++
					if r.Form.Get("DbiResourceId") != "db-source" {
						t.Error("PITR source pin lost", r.Form)
					}
					earliest, created, user, size := point.Earliest, point.CreatedAt, point.MasterUser, 20
					if scenario == "expired" || scenario == "resume-expired" {
						earliest = at.Add(time.Hour)
					}
					if scenario == "changed-epoch" {
						created = created.Add(time.Nanosecond)
					}
					if scenario == "wrong-master" {
						user = "other-admin"
					}
					if scenario == "oversized" {
						size = 100
					}
					fmt.Fprintf(w, `<DescribeDBInstanceAutomatedBackupsResponse><DescribeDBInstanceAutomatedBackupsResult><DBInstanceAutomatedBackups><DBInstanceAutomatedBackup><DBInstanceArn>%s</DBInstanceArn><DBInstanceIdentifier>orders</DBInstanceIdentifier><DbiResourceId>db-source</DbiResourceId><InstanceCreateTime>%s</InstanceCreateTime><Engine>postgres</Engine><EngineVersion>17.2</EngineVersion><Encrypted>true</Encrypted><KmsKeyId>%s</KmsKeyId><Status>active</Status><AllocatedStorage>%d</AllocatedStorage><MasterUsername>%s</MasterUsername><RestoreWindow><EarliestTime>%s</EarliestTime><LatestTime>%s</LatestTime></RestoreWindow></DBInstanceAutomatedBackup></DBInstanceAutomatedBackups></DescribeDBInstanceAutomatedBackupsResult></DescribeDBInstanceAutomatedBackupsResponse>`, point.ARN, created.Format(time.RFC3339Nano), point.KMSKey, size, user, earliest.Format(time.RFC3339), point.Latest.Format(time.RFC3339))
				case "DescribeDBSubnetGroups":
					fmt.Fprint(w, `<DescribeDBSubnetGroupsResponse><DescribeDBSubnetGroupsResult><DBSubnetGroups><DBSubnetGroup><DBSubnetGroupName>recovery-private</DBSubnetGroupName><VpcId>vpc-recovery</VpcId><SubnetGroupStatus>Complete</SubnetGroupStatus></DBSubnetGroup></DBSubnetGroups></DescribeDBSubnetGroupsResult></DescribeDBSubnetGroupsResponse>`)
				case "DescribeSecurityGroups":
					outbound := ""
					if scenario == "public-egress" {
						outbound = `<ipPermissionsEgress><item><ipProtocol>-1</ipProtocol><ipRanges><item><cidrIp>0.0.0.0/0</cidrIp></item></ipRanges></item></ipPermissionsEgress>`
					}
					fmt.Fprint(w, `<DescribeSecurityGroupsResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><securityGroupInfo><item><ownerId>123456789012</ownerId><groupId>sg-copy</groupId><vpcId>vpc-recovery</vpcId><ipPermissions><item><ipProtocol>tcp</ipProtocol><fromPort>5432</fromPort><toPort>5432</toPort><groups><item><userId>123456789012</userId><groupId>sg-worker</groupId></item></groups></item></ipPermissions>`+outbound+`</item><item><ownerId>123456789012</ownerId><groupId>sg-worker</groupId><vpcId>vpc-recovery</vpcId></item></securityGroupInfo></DescribeSecurityGroupsResponse>`)
				case "RestoreDBInstanceToPointInTime":
					writes++
					if checkpoints != 1 {
						t.Error("PITR preceded durable intent", checkpoints)
					}
					for key, want := range map[string]string{"TargetDBInstanceIdentifier": name, "SourceDbiResourceId": "db-source", "DBInstanceClass": "db.t4g.micro", "AllocatedStorage": "20", "MaxAllocatedStorage": "20", "EnableIAMDatabaseAuthentication": "false", "StorageType": "gp3", "Iops": "3000", "StorageThroughput": "125", "PubliclyAccessible": "false", "MultiAZ": "false", "AutoMinorVersionUpgrade": "false", "BackupRetentionPeriod": "0", "DeletionProtection": "false", "DBSubnetGroupName": "recovery-private", "VpcSecurityGroupIds.VpcSecurityGroupId.1": "sg-copy", "DBParameterGroupName": "default.postgres17", "Port": "5432", "Tags.Tag.1.Key": "loomplane-native-recovery", "Tags.Tag.1.Value": intent} {
						if r.Form.Get(key) != want {
							t.Error("PITR changed its private capacity review", key, r.Form.Get(key))
						}
					}
					actual, parseErr := time.Parse(time.RFC3339Nano, r.Form.Get("RestoreTime"))
					if parseErr != nil || !actual.Equal(at) {
						t.Error("PITR changed requested UTC time", r.Form.Get("RestoreTime"), parseErr)
					}
					if r.Form.Get("SourceDBInstanceIdentifier") != "" || r.Form.Get("UseLatestRestorableTime") == "true" || r.Form.Get("DBSnapshotIdentifier") != "" {
						t.Error("PITR fell back to latest, source name or snapshot", r.Form)
					}
					if scenario == "rejected-parameters" {
						w.WriteHeader(http.StatusBadRequest)
						fmt.Fprint(w, `<ErrorResponse><Error><Code>InvalidParameterCombination</Code></Error></ErrorResponse>`)
						return
					}
					if scenario == "rejected-time" {
						w.WriteHeader(http.StatusBadRequest)
						fmt.Fprint(w, `<ErrorResponse><Error><Code>InvalidRestoreFault</Code></Error></ErrorResponse>`)
						return
					}
					exists = true
					if scenario == "lost-response" {
						w.WriteHeader(http.StatusInternalServerError)
						fmt.Fprint(w, `<ErrorResponse><Error><Code>InternalFailure</Code></Error></ErrorResponse>`)
						return
					}
					fmt.Fprint(w, `<RestoreDBInstanceToPointInTimeResponse><RestoreDBInstanceToPointInTimeResult>`+instance()+`</RestoreDBInstanceToPointInTimeResult></RestoreDBInstanceToPointInTimeResponse>`)
				default:
					t.Error("PITR used an unexpected native operation", r.Form.Get("Action"))
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()
			restorer, ok := any(New(server.URL).Session(&Key{AccessKeyID: "AKIATESTTESTTESTTEST", SecretAccessKey: "test-secret-test-secret"}, "us-east-1", "123456789012")).(dbPITRRestorer)
			if !ok {
				t.Fatal("RDS has no guarded point-in-time restore primitive")
			}
			failure := errors.New("checkpoint refused")
			control := DBRecoveryControl{Check: func(ctx context.Context) error { return ctx.Err() }, Mutation: DBMutation{Attempted: exists, Before: func() error {
				checkpoints++
				if scenario == "intent-fails" {
					return failure
				}
				return nil
			}, Captured: func(id string) error {
				captures++
				if id != "db-copy" {
					t.Error("wrong PITR physical copy", id)
				}
				if scenario == "capture-fails" {
					return failure
				}
				return nil
			}}}
			result, err := restorer.RestoreDBPointInTime(t.Context(), spec, control)
			switch scenario {
			case "new":
				if err != nil || result == nil || writes != 1 || captures != 1 {
					t.Fatal("PITR did not capture its private copy", result, writes, captures, err)
				}
			case "resume-expired":
				if err != nil || result == nil || writes != 0 || windowReads != 0 {
					t.Fatal("existing PITR copy depended on an expired source window", result, writes, windowReads, err)
				}
			case "lost-response":
				if err == nil || writes != 1 || captures != 0 || !DBRecoveryRequestUncertain(err) {
					t.Fatal("PITR lost its uncertain request", writes, captures, err)
				}
				control.Mutation.Attempted = true
				result, err = restorer.RestoreDBPointInTime(t.Context(), spec, control)
				if err != nil || result == nil || writes != 1 || captures != 1 {
					t.Fatal("PITR reposted instead of reconciling original intent", result, writes, captures, err)
				}
			case "capture-fails":
				if !errors.Is(err, failure) || writes != 1 || captures != 1 {
					t.Fatal("PITR continued after capture failure", writes, captures, err)
				}
			case "rejected-time", "rejected-parameters":
				if err == nil || writes != 1 || !DBRecoveryRequestRejected(err) || DBRecoveryRequestUncertain(err) {
					t.Fatal("definitive PITR rejection became an uncertain write", writes, err)
				}
			default:
				if err == nil || result != nil || writes != 0 {
					t.Fatal("PITR accepted an unreviewed source, window or destination", result, writes, err)
				}
			}
		})
	}
}

func TestRDSRecoveryModesCannotSilentlyChangeSelection(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(http.StatusInternalServerError) }))
	defer server.Close()
	session := New(server.URL).Session(&Key{AccessKeyID: "AKIATESTTESTTESTTEST", SecretAccessKey: "test-secret-test-secret"}, "us-east-1", "123456789012")
	control := DBRecoveryControl{Check: func(context.Context) error { return nil }}
	if _, err := session.RestoreDBPointInTime(t.Context(), DBRecoverySpec{}, control); err == nil {
		t.Fatal("PITR without an explicit window fell back to a snapshot")
	}
	if _, err := session.RestoreDBSnapshot(t.Context(), DBRecoverySpec{PITR: &DBPITRWindow{}}, control); err == nil {
		t.Fatal("snapshot mode accepted a PITR selection")
	}
	if _, err := session.RestoreDBSnapshot(t.Context(), DBRecoverySpec{RestoreTime: time.Now().UTC()}, control); err == nil {
		t.Fatal("snapshot mode ignored an explicit restore time")
	}
	if calls != 0 {
		t.Fatal("invalid recovery mode contacted the provider", calls)
	}
}
