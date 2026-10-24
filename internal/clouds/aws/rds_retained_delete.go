package aws

import (
	"context"
	"errors"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
)

func sameDBSnapshotIdentity(a, b *DBSnapshot) bool {
	return a != nil && b != nil && b.EncryptionKnown && a.ARN == b.ARN && a.ID == b.ID && a.ResourceID == b.ResourceID && a.InstanceID == b.InstanceID && a.CreatedAt.Equal(b.CreatedAt) && a.Type == b.Type && a.Engine == b.Engine && a.EngineVersion == b.EngineVersion && a.Encrypted == b.Encrypted && a.KMSKey == b.KMSKey
}

// DeleteRetainedDBSnapshot removes one independent manual snapshot. Automated
// snapshots belong to the provider's recovery chain and are never pruned here.
// Attempted requests are observed, never reposted, including after response loss.
func (s *Session) DeleteRetainedDBSnapshot(ctx context.Context, physical string, selected DBSnapshot, c DBRecoveryControl) error {
	if err := s.checkSnapshotIdentity(&selected, physical); err != nil {
		return err
	}
	if selected.Type != "manual" || selected.CreatedAt.IsZero() || selected.Engine == "" || selected.EngineVersion == "" || selected.InstanceID == "" || c.Check == nil {
		return errors.New("native deletion requires an independent manual snapshot and original scope")
	}
	if err := c.Check(ctx); err != nil {
		return err
	}
	if !c.Mutation.Attempted {
		if c.Mutation.Before == nil {
			return errors.New("native deletion requires a durable intent")
		}
		if _, readErr := s.ReadDBSnapshot(ctx, physical, selected); readErr != nil && !IsNotFound(readErr) {
			return readErr
		}
		if err := c.Mutation.Before(); err != nil {
			return err
		}
		if err := c.Check(ctx); err != nil {
			return err
		}
		if _, readErr := s.ReadDBSnapshot(ctx, physical, selected); IsNotFound(readErr) {
			return c.Check(ctx)
		} else if readErr != nil {
			return readErr
		}
		if err := c.Check(ctx); err != nil {
			return err
		}
		if _, err := s.rdsWrites().DeleteDBSnapshot(ctx, &rds.DeleteDBSnapshotInput{DBSnapshotIdentifier: sdk.String(selected.ID)}); err != nil {
			return dbRecoveryWriteError(err)
		}
	}
	for {
		if err := c.Check(ctx); err != nil {
			return err
		}
		point, err := s.GetDBSnapshot(ctx, selected.ID)
		if IsNotFound(err) {
			return c.Check(ctx)
		}
		if err != nil {
			return dbRecoveryObservationError(err)
		}
		if !sameDBSnapshotIdentity(&selected, point) {
			return errors.New("native deletion observed a different snapshot identity")
		}
		if err := s.wait(ctx); err != nil {
			return err
		}
	}
}
