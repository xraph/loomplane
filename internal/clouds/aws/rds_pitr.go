package aws

import (
	"context"
	"errors"
	"time"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/rds"
)

// DBPITRWindow describes the provider's recovery chain for one physical source.
// CreatedAt is the source instance creation time, not a snapshot creation time.
type DBPITRWindow struct {
	ARN, InstanceID, ResourceID, Engine, EngineVersion, KMSKey, MasterUser string
	Status                                                                 string
	CreatedAt, Earliest, Latest                                            time.Time
	StorageGB                                                              int32
	Encrypted, EncryptionKnown                                             bool
}

// ErrDBPITRUnavailable distinguishes an absent recovery window from a failed read.
var ErrDBPITRUnavailable = errors.New("RDS has no recoverable window for this original database")

func (s *Session) checkDBPITRIdentity(point *DBPITRWindow, physical string) error {
	if point == nil || physical == "" || point.ResourceID != physical || point.InstanceID == "" || point.CreatedAt.IsZero() || point.Engine == "" || point.EngineVersion == "" || !point.EncryptionKnown || point.Encrypted && point.KMSKey == "" || !point.Encrypted && point.KMSKey != "" {
		return errors.New("RDS PITR requires original source, creation, engine and encryption metadata")
	}
	identity, err := arn.Parse(point.ARN)
	if err != nil || identity.Service != "rds" || identity.AccountID != s.account || identity.Region != s.region || identity.Resource != "db:"+point.InstanceID {
		return errors.New("RDS PITR source belongs to a different native account, region or database")
	}
	return nil
}

// ReadDBPITRWindow reads by immutable DbiResourceId. Retained backups remain
// readable after source removal; a reusable instance name is never a fallback.
func (s *Session) ReadDBPITRWindow(ctx context.Context, physical string) (*DBPITRWindow, error) {
	if physical == "" {
		return nil, errors.New("RDS PITR requires a pinned physical source")
	}
	out, err := rds.NewFromConfig(s.cfg).DescribeDBInstanceAutomatedBackups(ctx, &rds.DescribeDBInstanceAutomatedBackupsInput{DbiResourceId: sdk.String(physical), MaxRecords: sdk.Int32(100)})
	if err != nil {
		return nil, err
	}
	if len(out.DBInstanceAutomatedBackups) == 0 && sdk.ToString(out.Marker) == "" {
		return nil, ErrDBPITRUnavailable
	}
	if len(out.DBInstanceAutomatedBackups) != 1 || sdk.ToString(out.Marker) != "" {
		return nil, errors.New("RDS PITR recovery-chain identity is ambiguous or incomplete")
	}
	in := out.DBInstanceAutomatedBackups[0]
	point := &DBPITRWindow{ARN: sdk.ToString(in.DBInstanceArn), InstanceID: sdk.ToString(in.DBInstanceIdentifier), ResourceID: sdk.ToString(in.DbiResourceId), Engine: sdk.ToString(in.Engine), EngineVersion: sdk.ToString(in.EngineVersion), KMSKey: sdk.ToString(in.KmsKeyId), MasterUser: sdk.ToString(in.MasterUsername), StorageGB: sdk.ToInt32(in.AllocatedStorage), Status: sdk.ToString(in.Status), Encrypted: sdk.ToBool(in.Encrypted), EncryptionKnown: in.Encrypted != nil}
	if in.InstanceCreateTime != nil {
		point.CreatedAt = in.InstanceCreateTime.UTC()
	}
	if identityErr := s.checkDBPITRIdentity(point, physical); identityErr != nil {
		return nil, identityErr
	}
	if point.Status != "active" && point.Status != "retained" || in.RestoreWindow == nil || in.RestoreWindow.EarliestTime == nil || in.RestoreWindow.LatestTime == nil {
		return nil, ErrDBPITRUnavailable
	}
	point.Earliest, point.Latest = in.RestoreWindow.EarliestTime.UTC(), in.RestoreWindow.LatestTime.UTC()
	now := time.Now().UTC()
	if point.Earliest.IsZero() || point.Latest.IsZero() || !point.Earliest.Before(point.Latest) || point.Latest.After(now) || point.CreatedAt.After(point.Latest) || point.Earliest.Before(point.CreatedAt) {
		return nil, errors.New("RDS PITR window has inconsistent provider timestamps")
	}
	return point, nil
}

// CheckRestoreTime validates the requested time without rounding or replacing
// it with the latest bound. It does not establish source identity or SQL proof.
func (w DBPITRWindow) CheckRestoreTime(at time.Time) error {
	_, offset := at.Zone()
	if at.IsZero() || offset != 0 || at.Nanosecond() != 0 || w.Earliest.IsZero() || w.Latest.IsZero() || !w.Earliest.Before(w.Latest) || w.Latest.After(time.Now()) || at.Before(w.Earliest) || !at.Before(w.Latest) {
		return errors.New("choose a whole UTC second within the provider PITR window, before its latest time")
	}
	return nil
}

// ReadSelectedDBPITRWindow permits rolling bounds only while the original
// epoch and exact requested time remain valid. It never selects a new time.
func (s *Session) ReadSelectedDBPITRWindow(ctx context.Context, physical string, selected DBPITRWindow, at time.Time) (*DBPITRWindow, error) {
	if err := s.checkDBPITRIdentity(&selected, physical); err != nil {
		return nil, err
	}
	if err := selected.CheckRestoreTime(at); err != nil {
		return nil, err
	}
	current, err := s.ReadDBPITRWindow(ctx, physical)
	if err != nil {
		return nil, err
	}
	if !sameDBPITRIdentity(selected, *current) {
		return nil, errors.New("RDS PITR source no longer matches its original epoch or encryption metadata")
	}
	if timeErr := current.CheckRestoreTime(at); timeErr != nil {
		return nil, timeErr
	}
	return current, nil
}
func sameDBPITRIdentity(a, b DBPITRWindow) bool {
	return a.ResourceID != "" && !a.CreatedAt.IsZero() && a.ResourceID == b.ResourceID && a.ARN == b.ARN && a.InstanceID == b.InstanceID && a.CreatedAt.Equal(b.CreatedAt) && a.Engine == b.Engine && a.EngineVersion == b.EngineVersion && a.EncryptionKnown && b.EncryptionKnown && a.Encrypted == b.Encrypted && a.KMSKey == b.KMSKey
}
