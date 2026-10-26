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

type rdsSnapshotRestorer interface {
	RestoreDBSnapshot(context.Context, DBRecoverySpec, DBRecoveryControl) (*DBInstance, error)
}

func TestRDSRecoveryRestoresOnlyReviewedPrivateCopyAndCapturesIdentity(t *testing.T) {
	for _, scenario := range []string{"new", "resume", "before-fails", "capture-fails", "lost-response", "foreign-intent", "replacement", "public-copy", "wrong-subnet", "public-ingress", "wrong-worker", "snapshot-changed", "snapshot-oversize", "revoked-before-write", "unsafe-created-copy"} {
		t.Run(scenario, func(t *testing.T) {
			intent := strings.Repeat("a", 64)
			name := "lp-recovery-" + intent[:32]
			created := time.Date(2026, 10, 22, 1, 2, 3, 4, time.UTC)
			point := DBSnapshot{ID: "original", ARN: "arn:aws:rds:us-east-1:123456789012:snapshot:original", InstanceID: "orders", ResourceID: "db-source", Type: "manual", CreatedAt: created, Engine: "postgres", EngineVersion: "17.2", Encrypted: true, KMSKey: "arn:aws:kms:us-east-1:123456789012:key/original"}
			spec := DBRecoverySpec{ID: name, Intent: intent, SourcePhysicalID: "db-source", Snapshot: point, Class: "db.t4g.micro", StorageGB: 20, StorageType: "gp3", VPCID: "vpc-recovery", SubnetGroup: "recovery-private", SecurityGroupID: "sg-copy", VerifierGroupID: "sg-worker", ParameterGroup: "default.postgres17", AdminUser: "loomplane"}
			exists := scenario == "resume" || scenario == "foreign-intent" || scenario == "replacement" || scenario == "public-copy"
			writes, before, captures, reads := 0, 0, 0, 0
			revoked := false
			instance := func() string {
				physical, tag, public := "db-copy", intent, "false"
				if scenario == "foreign-intent" {
					tag = "unowned"
				}
				if scenario == "replacement" {
					physical = "db-replaced"
				}
				if scenario == "public-copy" || scenario == "unsafe-created-copy" {
					public = "true"
				}
				return rdsRecoveryInstanceXML(name, physical, tag, public, point.KMSKey)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "text/xml")
				switch r.Form.Get("Action") {
				case "DescribeDBInstances":
					reads++
					if !exists {
						w.WriteHeader(http.StatusNotFound)
						fmt.Fprint(w, `<ErrorResponse><Error><Code>DBInstanceNotFound</Code></Error></ErrorResponse>`)
						return
					}
					fmt.Fprint(w, `<DescribeDBInstancesResponse><DescribeDBInstancesResult><DBInstances>`+instance()+`</DBInstances></DescribeDBInstancesResult></DescribeDBInstancesResponse>`)
				case "DescribeDBSnapshots":
					when, size := created, 20
					if scenario == "snapshot-changed" {
						when = when.Add(time.Second)
					}
					if scenario == "snapshot-oversize" {
						size = 100
					}
					fmt.Fprintf(w, `<DescribeDBSnapshotsResponse><DescribeDBSnapshotsResult><DBSnapshots><DBSnapshot><DBSnapshotIdentifier>original</DBSnapshotIdentifier><DBSnapshotArn>%s</DBSnapshotArn><DBInstanceIdentifier>orders</DBInstanceIdentifier><DbiResourceId>db-source</DbiResourceId><SnapshotType>manual</SnapshotType><SnapshotCreateTime>%s</SnapshotCreateTime><Status>available</Status><Engine>postgres</Engine><EngineVersion>17.2</EngineVersion><Encrypted>true</Encrypted><KmsKeyId>%s</KmsKeyId><AllocatedStorage>%d</AllocatedStorage><MasterUsername>loomplane</MasterUsername></DBSnapshot></DBSnapshots></DescribeDBSnapshotsResult></DescribeDBSnapshotsResponse>`, point.ARN, when.Format(time.RFC3339Nano), point.KMSKey, size)
				case "DescribeDBSubnetGroups":
					vpc := "vpc-recovery"
					if scenario == "wrong-subnet" {
						vpc = "vpc-unreviewed"
					}
					fmt.Fprintf(w, `<DescribeDBSubnetGroupsResponse><DescribeDBSubnetGroupsResult><DBSubnetGroups><DBSubnetGroup><DBSubnetGroupName>recovery-private</DBSubnetGroupName><VpcId>%s</VpcId><SubnetGroupStatus>Complete</SubnetGroupStatus></DBSubnetGroup></DBSubnetGroups></DescribeDBSubnetGroupsResult></DescribeDBSubnetGroupsResponse>`, vpc)
				case "DescribeSecurityGroups":
					peer := "sg-worker"
					if scenario == "wrong-worker" {
						peer = "sg-application"
					}
					cidr := ""
					if scenario == "public-ingress" {
						cidr = `<ipRanges><item><cidrIp>0.0.0.0/0</cidrIp></item></ipRanges>`
					}
					fmt.Fprintf(w, `<DescribeSecurityGroupsResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><securityGroupInfo><item><ownerId>123456789012</ownerId><groupId>sg-copy</groupId><vpcId>vpc-recovery</vpcId><ipPermissions><item><ipProtocol>tcp</ipProtocol><fromPort>5432</fromPort><toPort>5432</toPort><groups><item><userId>123456789012</userId><groupId>%s</groupId></item></groups>%s</item></ipPermissions></item><item><ownerId>123456789012</ownerId><groupId>sg-worker</groupId><vpcId>vpc-recovery</vpcId></item></securityGroupInfo></DescribeSecurityGroupsResponse>`, peer, cidr)
				case "RestoreDBInstanceFromDBSnapshot":
					writes++
					exists = true
					for key, want := range map[string]string{"DBInstanceIdentifier": name, "DBSnapshotIdentifier": point.ARN, "DBInstanceClass": "db.t4g.micro", "AllocatedStorage": "20", "StorageType": "gp3", "PubliclyAccessible": "false", "MultiAZ": "false", "AutoMinorVersionUpgrade": "false", "BackupRetentionPeriod": "0", "DeletionProtection": "false", "DBSubnetGroupName": "recovery-private", "VpcSecurityGroupIds.VpcSecurityGroupId.1": "sg-copy", "DBParameterGroupName": "default.postgres17", "Port": "5432", "Tags.Tag.1.Key": "loomplane-native-recovery", "Tags.Tag.1.Value": intent} {
						if r.Form.Get(key) != want {
							t.Error("unreviewed restore setting", key, r.Form.Get(key))
						}
					}
					if r.Form.Get("MasterUserPassword") != "" {
						t.Error("snapshot restore submitted a password")
					}
					if scenario == "lost-response" {
						w.WriteHeader(http.StatusInternalServerError)
						fmt.Fprint(w, `<ErrorResponse><Error><Code>InternalFailure</Code></Error></ErrorResponse>`)
						return
					}
					fmt.Fprint(w, `<RestoreDBInstanceFromDBSnapshotResponse><RestoreDBInstanceFromDBSnapshotResult>`+instance()+`</RestoreDBInstanceFromDBSnapshotResult></RestoreDBInstanceFromDBSnapshotResponse>`)
				default:
					t.Errorf("unexpected RDS recovery call: %s", r.Form.Get("Action"))
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()
			session := New(server.URL).Session(&Key{AccessKeyID: "AKIATESTTESTTESTTEST", SecretAccessKey: "test-test-test-test-secret"}, "us-east-1", "123456789012")
			restorer, ok := any(session).(rdsSnapshotRestorer)
			if !ok {
				t.Fatal("RDS native recovery has no guarded snapshot restore primitive")
			}
			failure := errors.New("checkpoint refused")
			control := DBRecoveryControl{Mutation: DBMutation{Attempted: exists, Before: func() error {
				before++
				if scenario == "before-fails" {
					return failure
				}
				if scenario == "revoked-before-write" {
					revoked = true
				}
				return nil
			}, Captured: func(id string) error {
				captures++
				if id != "db-copy" {
					t.Error("wrong physical copy captured", id)
				}
				if scenario == "capture-fails" {
					return failure
				}
				return nil
			}}, Check: func(ctx context.Context) error {
				if revoked {
					return failure
				}
				return ctx.Err()
			}}
			if scenario == "replacement" {
				spec.ResourceID = "db-original-copy"
			}
			restored, err := restorer.RestoreDBSnapshot(t.Context(), spec, control)
			switch scenario {
			case "new":
				if err != nil || restored == nil || restored.ResourceID != "db-copy" || writes != 1 || before != 1 || captures != 1 {
					t.Fatal("reviewed copy not captured", writes, before, captures, err)
				}
			case "resume":
				if err != nil || restored == nil || writes != 0 || captures != 1 {
					t.Fatal("captured restore did not resume", writes, captures, err)
				}
			case "lost-response":
				if err == nil || writes != 1 || captures != 0 {
					t.Fatal("uncertain native restore was retried", writes, captures, err)
				}
				control.Mutation.Attempted = true
				if restored, err = restorer.RestoreDBSnapshot(t.Context(), spec, control); err != nil || restored == nil || writes != 1 || captures != 1 {
					t.Fatal("lost response was not reconciled by original intent", writes, captures, err)
				}
			case "capture-fails":
				if !errors.Is(err, failure) || writes != 1 || reads != 1 {
					t.Fatal("restore continued after capture failure", writes, reads, err)
				}
			case "unsafe-created-copy":
				if err == nil || writes != 1 || captures != 1 {
					t.Fatal("unsafe created copy lost its cleanup pin", writes, captures, err)
				}
			default:
				if err == nil || writes != 0 {
					t.Fatal("unreviewed native restore wrote a database", scenario, writes, err)
				}
			}
		})
	}
}

func rdsRecoveryInstanceXML(name, physical, intent, public, kms string) string {
	return fmt.Sprintf(`<DBInstance><DBInstanceIdentifier>%s</DBInstanceIdentifier><DBInstanceArn>arn:aws:rds:us-east-1:123456789012:db:%s</DBInstanceArn><DbiResourceId>%s</DbiResourceId><Engine>postgres</Engine><EngineVersion>17.2</EngineVersion><DBInstanceStatus>available</DBInstanceStatus><DBInstanceClass>db.t4g.micro</DBInstanceClass><AllocatedStorage>20</AllocatedStorage><StorageType>gp3</StorageType><StorageEncrypted>true</StorageEncrypted><KmsKeyId>%s</KmsKeyId><MasterUsername>loomplane</MasterUsername><PubliclyAccessible>%s</PubliclyAccessible><MultiAZ>false</MultiAZ><AutoMinorVersionUpgrade>false</AutoMinorVersionUpgrade><BackupRetentionPeriod>0</BackupRetentionPeriod><DeletionProtection>false</DeletionProtection><DBSubnetGroup><DBSubnetGroupName>recovery-private</DBSubnetGroupName><VpcId>vpc-recovery</VpcId></DBSubnetGroup><VpcSecurityGroups><VpcSecurityGroupMembership><VpcSecurityGroupId>sg-copy</VpcSecurityGroupId><Status>active</Status></VpcSecurityGroupMembership></VpcSecurityGroups><DBParameterGroups><DBParameterGroup><DBParameterGroupName>default.postgres17</DBParameterGroupName><ParameterApplyStatus>in-sync</ParameterApplyStatus></DBParameterGroup></DBParameterGroups><Endpoint><Address>%s.abcdef.us-east-1.rds.amazonaws.com</Address><Port>5432</Port></Endpoint><TagList><Tag><Key>loomplane-native-recovery</Key><Value>%s</Value></Tag></TagList></DBInstance>`, name, name, physical, kms, public, name, intent)
}
