package aws

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
)

// DBRecoverySpec is private placement reviewed before a native restore. It
// cannot adopt or replace a source instance or select a public SQL endpoint.
type DBRecoverySpec struct {
	ID, ResourceID, Intent, SourcePhysicalID             string
	Snapshot                                             DBSnapshot
	Class, StorageType                                   string
	StorageGB                                            int32
	VPCID, SubnetGroup, SecurityGroupID, VerifierGroupID string
	ParameterGroup, AdminUser                            string
}

type DBRecoveryControl struct {
	Mutation DBMutation
	Check    func(context.Context) error
}

// DBRecoverySettings retains provider observations needed before SQL access.
// Missing flags are unknown, never evidence of a private or encrypted copy.
type DBRecoverySettings struct {
	Known                                                           bool
	ARN, Class, StorageType, KMSKey, MasterUser, SubnetGroup, VPCID string
	StorageGB, BackupDays                                           int32
	Encrypted, Public, MultiAZ, AutoMinor, DeleteProtected, Pending bool
	Groups                                                          []string
	GroupsReady                                                     bool
	Parameters                                                      []string
	ParametersReady                                                 bool
}

func readDBRecoverySettings(in rdstypes.DBInstance) *DBRecoverySettings {
	out := &DBRecoverySettings{Known: in.StorageEncrypted != nil && in.PubliclyAccessible != nil && in.MultiAZ != nil && in.AutoMinorVersionUpgrade != nil && in.BackupRetentionPeriod != nil && in.DeletionProtection != nil && in.AllocatedStorage != nil,
		ARN: sdk.ToString(in.DBInstanceArn), Class: sdk.ToString(in.DBInstanceClass), StorageType: sdk.ToString(in.StorageType), KMSKey: sdk.ToString(in.KmsKeyId), MasterUser: sdk.ToString(in.MasterUsername), StorageGB: sdk.ToInt32(in.AllocatedStorage), BackupDays: sdk.ToInt32(in.BackupRetentionPeriod), Encrypted: sdk.ToBool(in.StorageEncrypted), Public: sdk.ToBool(in.PubliclyAccessible), MultiAZ: sdk.ToBool(in.MultiAZ), AutoMinor: sdk.ToBool(in.AutoMinorVersionUpgrade), DeleteProtected: sdk.ToBool(in.DeletionProtection), GroupsReady: true, ParametersReady: true}
	if in.DBSubnetGroup != nil {
		out.SubnetGroup = sdk.ToString(in.DBSubnetGroup.DBSubnetGroupName)
		out.VPCID = sdk.ToString(in.DBSubnetGroup.VpcId)
	}
	for _, group := range in.VpcSecurityGroups {
		out.Groups = append(out.Groups, sdk.ToString(group.VpcSecurityGroupId))
		out.GroupsReady = out.GroupsReady && sdk.ToString(group.Status) == "active"
	}
	for _, group := range in.DBParameterGroups {
		out.Parameters = append(out.Parameters, sdk.ToString(group.DBParameterGroupName))
		out.ParametersReady = out.ParametersReady && sdk.ToString(group.ParameterApplyStatus) == "in-sync"
	}
	out.Pending = in.PendingModifiedValues != nil && !reflect.ValueOf(*in.PendingModifiedValues).IsZero()
	return out
}

var recoveryIntentRE = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (s *Session) validDBRecovery(spec DBRecoverySpec, c DBRecoveryControl) error {
	if !recoveryIntentRE.MatchString(spec.Intent) || spec.ID != "lp-recovery-"+spec.Intent[:32] || spec.ID == spec.Snapshot.InstanceID || spec.SourcePhysicalID == "" || spec.ResourceID == spec.SourcePhysicalID || spec.Class == "" || spec.StorageGB < 20 || spec.StorageType != "gp3" || spec.VPCID == "" || spec.SubnetGroup == "" || spec.SecurityGroupID == "" || spec.VerifierGroupID == "" || spec.SecurityGroupID == spec.VerifierGroupID || spec.ParameterGroup != "default.postgres17" || spec.AdminUser == "" || spec.Snapshot.Engine != "postgres" || (spec.Snapshot.EngineVersion != "17" && !strings.HasPrefix(spec.Snapshot.EngineVersion, "17.")) || !spec.Snapshot.Encrypted || c.Check == nil || spec.ResourceID == "" && c.Mutation.Captured == nil {
		return errors.New("RDS recovery requires a reviewed private PostgreSQL 17 copy and durable controls")
	}
	return s.checkSnapshotIdentity(&spec.Snapshot, spec.SourcePhysicalID)
}

func (s *Session) checkDBRecoveryOwner(db *DBInstance, spec DBRecoverySpec) error {
	if db == nil || db.ID != spec.ID || db.ResourceID == "" || db.ResourceID == spec.SourcePhysicalID || spec.ResourceID != "" && db.ResourceID != spec.ResourceID || db.Tags["loomplane-native-recovery"] != spec.Intent || db.Recovery == nil {
		return errors.New("RDS recovery copy no longer matches its original identity")
	}
	identity, err := arn.Parse(db.Recovery.ARN)
	if err != nil || identity.Service != "rds" || identity.Region != s.region || identity.AccountID != s.account || identity.Resource != "db:"+spec.ID {
		return errors.New("RDS recovery copy belongs to a different native account or region")
	}
	return nil
}

func (s *Session) checkDBRecoveryIdentity(db *DBInstance, spec DBRecoverySpec) error {
	if err := s.checkDBRecoveryOwner(db, spec); err != nil {
		return err
	}
	if db.Engine != "postgres" || db.EngineVersion != spec.Snapshot.EngineVersion {
		return errors.New("RDS recovery engine differs from the selected snapshot")
	}
	return nil
}

func checkDBRecoverySettings(db *DBInstance, spec DBRecoverySpec) error {
	r := db.Recovery
	if r == nil || !r.Known || r.Class != spec.Class || r.StorageGB != spec.StorageGB || r.StorageType != spec.StorageType || !r.Encrypted || r.KMSKey != spec.Snapshot.KMSKey || r.Public || r.MultiAZ || r.AutoMinor || r.DeleteProtected || r.BackupDays != 0 || r.Pending || r.MasterUser != spec.AdminUser || r.SubnetGroup != spec.SubnetGroup || r.VPCID != spec.VPCID || !r.GroupsReady || len(r.Groups) != 1 || r.Groups[0] != spec.SecurityGroupID || !r.ParametersReady || len(r.Parameters) != 1 || r.Parameters[0] != spec.ParameterGroup {
		return errors.New("RDS recovery copy settings differ from its private capacity review")
	}
	return nil
}

// RestoreDBSnapshot creates once, pins DbiResourceId before further polling,
// and observes only that copy on continuation. Available is not SQL proof.
func (s *Session) RestoreDBSnapshot(ctx context.Context, spec DBRecoverySpec, c DBRecoveryControl) (*DBInstance, error) {
	if inputErr := s.validDBRecovery(spec, c); inputErr != nil {
		return nil, inputErr
	}
	if checkErr := c.Check(ctx); checkErr != nil {
		return nil, checkErr
	}
	db, err := s.GetDBInstance(ctx, spec.ID)
	if IsNotFound(err) {
		if c.Mutation.Attempted || spec.ResourceID != "" {
			return nil, errors.New("original RDS recovery request is not visible; reconcile it without reposting")
		}
		if c.Mutation.Before == nil {
			return nil, errors.New("RDS recovery requires a durable restore intent")
		}
		if checkErr := c.Check(ctx); checkErr != nil {
			return nil, checkErr
		}
		selected, selectErr := s.ReadDBSnapshot(ctx, spec.SourcePhysicalID, spec.Snapshot)
		if selectErr != nil {
			return nil, selectErr
		}
		if selected.StorageGB <= 0 || selected.StorageGB > spec.StorageGB || selected.MasterUser != spec.AdminUser {
			return nil, errors.New("RDS snapshot exceeds reviewed storage or has another administrator")
		}
		if networkErr := s.checkDBRecoveryNetwork(ctx, spec, c.Check); networkErr != nil {
			return nil, networkErr
		}
		if beforeErr := c.Mutation.Before(); beforeErr != nil {
			return nil, beforeErr
		}
		if checkErr := c.Check(ctx); checkErr != nil {
			return nil, checkErr
		}
		out, restoreErr := s.rdsWrites().RestoreDBInstanceFromDBSnapshot(ctx, &rds.RestoreDBInstanceFromDBSnapshotInput{DBInstanceIdentifier: sdk.String(spec.ID), DBSnapshotIdentifier: sdk.String(spec.Snapshot.ARN), DBInstanceClass: sdk.String(spec.Class), AllocatedStorage: sdk.Int32(spec.StorageGB), StorageType: sdk.String(spec.StorageType), PubliclyAccessible: sdk.Bool(false), MultiAZ: sdk.Bool(false), AutoMinorVersionUpgrade: sdk.Bool(false), BackupRetentionPeriod: sdk.Int32(0), DeletionProtection: sdk.Bool(false), DBSubnetGroupName: sdk.String(spec.SubnetGroup), VpcSecurityGroupIds: []string{spec.SecurityGroupID}, DBParameterGroupName: sdk.String(spec.ParameterGroup), Port: sdk.Int32(5432), Tags: []rdstypes.Tag{{Key: sdk.String("loomplane-native-recovery"), Value: sdk.String(spec.Intent)}}})
		if restoreErr != nil {
			return nil, fmt.Errorf("restore RDS snapshot: %w", restoreErr)
		}
		if out.DBInstance == nil {
			return nil, errors.New("RDS recovery returned no physical copy")
		}
		db, err = readDBInstance(*out.DBInstance), nil
	} else if err == nil && !c.Mutation.Attempted && spec.ResourceID == "" {
		return nil, errors.New("RDS recovery name exists without its original restore intent")
	}
	if err != nil {
		return nil, err
	}
	if identityErr := s.checkDBRecoveryIdentity(db, spec); identityErr != nil {
		return nil, identityErr
	}
	if spec.ResourceID == "" {
		if captureErr := c.Mutation.Captured(db.ResourceID); captureErr != nil {
			return nil, captureErr
		}
		spec.ResourceID = db.ResourceID
	}
	for {
		if checkErr := c.Check(ctx); checkErr != nil {
			return nil, checkErr
		}
		if identityErr := s.checkDBRecoveryIdentity(db, spec); identityErr != nil {
			return nil, identityErr
		}
		if db.Status == "available" {
			if settingsErr := checkDBRecoverySettings(db, spec); settingsErr != nil {
				return nil, settingsErr
			}
			if networkErr := s.checkDBRecoveryNetwork(ctx, spec, c.Check); networkErr != nil {
				return nil, networkErr
			}
			return db, c.Check(ctx)
		}
		switch db.Status {
		case "failed", "incompatible-parameters", "incompatible-restore", "incompatible-network", "storage-full", "deleting":
			return nil, fmt.Errorf("RDS recovery copy is %s", db.Status)
		}
		if waitErr := s.wait(ctx); waitErr != nil {
			return nil, waitErr
		}
		if checkErr := c.Check(ctx); checkErr != nil {
			return nil, checkErr
		}
		db, err = s.GetDBInstance(ctx, spec.ID)
		if err != nil {
			return nil, err
		}
	}
}

func (s *Session) checkDBRecoveryNetwork(ctx context.Context, spec DBRecoverySpec, check func(context.Context) error) error {
	if checkErr := check(ctx); checkErr != nil {
		return checkErr
	}
	subnets, err := rds.NewFromConfig(s.cfg).DescribeDBSubnetGroups(ctx, &rds.DescribeDBSubnetGroupsInput{DBSubnetGroupName: sdk.String(spec.SubnetGroup)})
	if err != nil {
		return err
	}
	if len(subnets.DBSubnetGroups) != 1 || sdk.ToString(subnets.DBSubnetGroups[0].DBSubnetGroupName) != spec.SubnetGroup || sdk.ToString(subnets.DBSubnetGroups[0].VpcId) != spec.VPCID || sdk.ToString(subnets.DBSubnetGroups[0].SubnetGroupStatus) != "Complete" {
		return errors.New("RDS recovery subnet group no longer matches its private VPC")
	}
	if checkErr := check(ctx); checkErr != nil {
		return checkErr
	}
	groups, err := ec2.NewFromConfig(s.cfg).DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{spec.SecurityGroupID, spec.VerifierGroupID}})
	if err != nil {
		return err
	}
	if len(groups.SecurityGroups) != 2 {
		return errors.New("RDS recovery security groups are incomplete")
	}
	seen := map[string]bool{}
	for _, group := range groups.SecurityGroups {
		id := sdk.ToString(group.GroupId)
		if (id != spec.SecurityGroupID && id != spec.VerifierGroupID) || seen[id] || sdk.ToString(group.VpcId) != spec.VPCID || sdk.ToString(group.OwnerId) != s.account {
			return errors.New("RDS recovery group belongs to another VPC or account")
		}
		seen[id] = true
		if id != spec.SecurityGroupID {
			continue
		}
		if len(group.IpPermissions) != 1 {
			return errors.New("RDS recovery must allow only its private SQL verifier")
		}
		rule := group.IpPermissions[0]
		if sdk.ToString(rule.IpProtocol) != "tcp" || sdk.ToInt32(rule.FromPort) != 5432 || sdk.ToInt32(rule.ToPort) != 5432 || len(rule.IpRanges) != 0 || len(rule.Ipv6Ranges) != 0 || len(rule.PrefixListIds) != 0 || len(rule.UserIdGroupPairs) != 1 || sdk.ToString(rule.UserIdGroupPairs[0].GroupId) != spec.VerifierGroupID || sdk.ToString(rule.UserIdGroupPairs[0].UserId) != s.account {
			return errors.New("RDS recovery permits unreviewed SQL ingress")
		}
	}
	return check(ctx)
}
