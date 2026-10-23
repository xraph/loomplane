package aws

import (
	"context"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
)

func readDBSnapshot(in rdstypes.DBSnapshot) *DBSnapshot {
	point := &DBSnapshot{ID: sdk.ToString(in.DBSnapshotIdentifier), ARN: sdk.ToString(in.DBSnapshotArn), InstanceID: sdk.ToString(in.DBInstanceIdentifier), ResourceID: sdk.ToString(in.DbiResourceId), Status: sdk.ToString(in.Status), Type: sdk.ToString(in.SnapshotType), Engine: sdk.ToString(in.Engine), EngineVersion: sdk.ToString(in.EngineVersion), Encrypted: sdk.ToBool(in.Encrypted), KMSKey: sdk.ToString(in.KmsKeyId)}
	if in.SnapshotCreateTime != nil {
		point.CreatedAt = in.SnapshotCreateTime.UTC()
	}
	point.EncryptionKnown = in.Encrypted != nil
	return point
}

func (s *Session) checkSnapshotIdentity(point *DBSnapshot, physical string) error {
	if point == nil || physical == "" || point.ID == "" || strings.ContainsAny(point.ID, "/\\?#") || point.ResourceID != physical {
		return errors.New("RDS snapshot does not match the original physical database")
	}
	identity, err := arn.Parse(point.ARN)
	if err != nil || identity.Service != "rds" || identity.AccountID != s.account || identity.Region != s.region || identity.Resource != "snapshot:"+point.ID {
		return errors.New("RDS snapshot does not match its account, region and native identity")
	}
	return nil
}

// ReadDBSnapshot rereads one retained snapshot and requires its original source,
// creation time and encryption metadata before admitting a recovery operation.
func (s *Session) ReadDBSnapshot(ctx context.Context, physical string, selected DBSnapshot) (*DBSnapshot, error) {
	if err := s.checkSnapshotIdentity(&selected, physical); err != nil {
		return nil, err
	}
	if selected.CreatedAt.IsZero() || selected.Engine == "" || selected.EngineVersion == "" || selected.InstanceID == "" || (selected.Type != "manual" && selected.Type != "automated") {
		return nil, errors.New("RDS selected snapshot requires retained creation and engine metadata")
	}
	if selected.Encrypted && selected.KMSKey == "" || !selected.Encrypted && selected.KMSKey != "" {
		return nil, errors.New("RDS selected snapshot requires consistent encryption key metadata")
	}
	point, err := s.GetDBSnapshot(ctx, selected.ID)
	if err != nil {
		return nil, err
	}
	if err := s.checkSnapshotIdentity(point, physical); err != nil {
		return nil, err
	}
	if !point.EncryptionKnown || point.ARN != selected.ARN || point.InstanceID != selected.InstanceID || !point.CreatedAt.Equal(selected.CreatedAt) || point.Type != selected.Type || point.Engine != selected.Engine || point.EngineVersion != selected.EngineVersion || point.Encrypted != selected.Encrypted || point.KMSKey != selected.KMSKey {
		return nil, errors.New("RDS selected snapshot no longer matches its retained identity")
	}
	if point.Status != "available" {
		return nil, errors.New("RDS selected snapshot is not currently available for recovery")
	}
	return point, nil
}

// ListDBSnapshots discovers owned manual and automated points by physical ID,
// including after source deletion. It never queries by a reusable instance name.
// Ten pages bound a refresh; complete=false explicitly reports more pages.
func (s *Session) ListDBSnapshots(ctx context.Context, physical string) ([]DBSnapshot, bool, error) {
	if physical == "" {
		return nil, false, errors.New("RDS inventory requires a pinned physical database")
	}
	var points []DBSnapshot
	markers, identities := map[string]bool{}, map[string]bool{}
	marker := ""
	client := rds.NewFromConfig(s.cfg)
	for range 10 {
		markers[marker] = true
		query := &rds.DescribeDBSnapshotsInput{DbiResourceId: sdk.String(physical), MaxRecords: sdk.Int32(100), IncludePublic: sdk.Bool(false), IncludeShared: sdk.Bool(false)}
		if marker != "" {
			query.Marker = sdk.String(marker)
		}
		page, err := client.DescribeDBSnapshots(ctx, query)
		if err != nil {
			return nil, false, fmt.Errorf("read RDS snapshot inventory: %w", err)
		}
		if len(page.DBSnapshots) > 100 {
			return nil, false, errors.New("RDS snapshot page exceeds the requested limit")
		}
		for _, item := range page.DBSnapshots {
			point := readDBSnapshot(item)
			if err := s.checkSnapshotIdentity(point, physical); err != nil {
				return nil, false, err
			}
			if point.CreatedAt.IsZero() || point.InstanceID == "" || point.Status == "" || (point.Type != "manual" && point.Type != "automated") || identities[point.ARN] {
				return nil, false, errors.New("RDS snapshot inventory has incomplete or inconsistent metadata")
			}
			identities[point.ARN] = true
			points = append(points, *point)
		}
		marker = sdk.ToString(page.Marker)
		if marker == "" {
			return points, true, nil
		}
		if markers[marker] {
			return nil, false, errors.New("RDS snapshot pagination repeated a marker")
		}
	}
	return points, false, nil
}
