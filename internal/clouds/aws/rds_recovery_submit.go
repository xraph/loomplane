package aws

import (
	"context"
	"errors"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
)

// RestoreDBPointInTime uses one explicit time and the original physical source.
// The shared lifecycle pins identity before polling and never reposts an intent.
func (s *Session) RestoreDBPointInTime(ctx context.Context, spec DBRecoverySpec, c DBRecoveryControl) (*DBInstance, error) {
	if spec.PITR == nil {
		return nil, errors.New("RDS PITR requires an observed source window and explicit UTC time")
	}
	return s.restoreDBRecovery(ctx, spec, c)
}
func (s *Session) checkDBRecoverySource(ctx context.Context, spec DBRecoverySpec) error {
	var size int32
	var master string
	if spec.PITR != nil {
		point, err := s.ReadSelectedDBPITRWindow(ctx, spec.SourcePhysicalID, *spec.PITR, spec.RestoreTime)
		if err != nil {
			return err
		}
		size, master = point.StorageGB, point.MasterUser
	} else {
		point, err := s.ReadDBSnapshot(ctx, spec.SourcePhysicalID, spec.Snapshot)
		if err != nil {
			return err
		}
		size, master = point.StorageGB, point.MasterUser
	}
	if size <= 0 || size > spec.StorageGB || master != spec.AdminUser {
		return errors.New("RDS recovery point exceeds reviewed storage or has another administrator")
	}
	return nil
}
func (s *Session) submitDBRecovery(ctx context.Context, spec DBRecoverySpec) (*DBInstance, error) {
	tags := []rdstypes.Tag{{Key: sdk.String("loomplane-native-recovery"), Value: sdk.String(spec.Intent)}}
	var db *rdstypes.DBInstance
	if spec.PITR != nil {
		out, err := s.rdsWrites().RestoreDBInstanceToPointInTime(ctx, &rds.RestoreDBInstanceToPointInTimeInput{TargetDBInstanceIdentifier: sdk.String(spec.ID), SourceDbiResourceId: sdk.String(spec.SourcePhysicalID), RestoreTime: sdk.Time(spec.RestoreTime.UTC()), UseLatestRestorableTime: sdk.Bool(false), DBInstanceClass: sdk.String(spec.Class), AllocatedStorage: sdk.Int32(spec.StorageGB), MaxAllocatedStorage: sdk.Int32(spec.StorageGB), StorageType: sdk.String(spec.StorageType), Iops: sdk.Int32(3000), StorageThroughput: sdk.Int32(125), PubliclyAccessible: sdk.Bool(false), MultiAZ: sdk.Bool(false), AutoMinorVersionUpgrade: sdk.Bool(false), BackupRetentionPeriod: sdk.Int32(0), DeletionProtection: sdk.Bool(false), EnableIAMDatabaseAuthentication: sdk.Bool(false), DBSubnetGroupName: sdk.String(spec.SubnetGroup), VpcSecurityGroupIds: []string{spec.SecurityGroupID}, DBParameterGroupName: sdk.String(spec.ParameterGroup), Port: sdk.Int32(5432), Tags: tags})
		if err != nil {
			return nil, dbRecoveryWriteError(err)
		}
		db = out.DBInstance
	} else {
		out, err := s.rdsWrites().RestoreDBInstanceFromDBSnapshot(ctx, &rds.RestoreDBInstanceFromDBSnapshotInput{DBInstanceIdentifier: sdk.String(spec.ID), DBSnapshotIdentifier: sdk.String(spec.Snapshot.ARN), DBInstanceClass: sdk.String(spec.Class), AllocatedStorage: sdk.Int32(spec.StorageGB), StorageType: sdk.String(spec.StorageType), PubliclyAccessible: sdk.Bool(false), MultiAZ: sdk.Bool(false), AutoMinorVersionUpgrade: sdk.Bool(false), BackupRetentionPeriod: sdk.Int32(0), DeletionProtection: sdk.Bool(false), DBSubnetGroupName: sdk.String(spec.SubnetGroup), VpcSecurityGroupIds: []string{spec.SecurityGroupID}, DBParameterGroupName: sdk.String(spec.ParameterGroup), Port: sdk.Int32(5432), Tags: tags})
		if err != nil {
			return nil, dbRecoveryWriteError(err)
		}
		db = out.DBInstance
	}
	if db == nil {
		return nil, dbRecoveryWriteError(errors.New("RDS recovery returned no physical copy"))
	}
	return readDBInstance(*db), nil
}
