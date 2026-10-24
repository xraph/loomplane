package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/internal/security"
)

func (n *NativeDatabases) nativeRDSSession(ctx context.Context, r recovery.NativeRestore) (*aws.Session, error) {
	if n == nil || n.AWS == nil || n.Credentials == nil || n.Secrets == nil || r.Target.Provider != "rds" {
		return nil, recovery.ErrInvalid
	}
	conn, credential, err := n.Credentials.Credential(ctx, r.OrgID, r.Target.ConnectionID)
	if err != nil {
		return nil, err
	}
	if conn == nil || conn.ID != r.Target.ConnectionID || conn.OrgID != r.OrgID || conn.Cloud != connections.AWS || conn.Status != connections.StatusReady || conn.Account != r.Target.NativeAccount || conn.Details["region"] != r.Target.Region || connections.AccountIdentity(conn.Cloud, conn.Account, conn.Details) != r.Target.AccountIdentity {
		return nil, recovery.ErrConflict
	}
	key, err := aws.ParseKey(credential)
	if err != nil {
		return nil, err
	}
	return n.AWS.Session(key, r.Target.Region, conn.Account), nil
}

func (s *nativeSQLCopyState) rdsSpec(r recovery.NativeRestore, cfg *planning.ResourceConfig) aws.DBRecoverySpec {
	network := r.Target.RDSNetwork
	spec := aws.DBRecoverySpec{ID: "lp-recovery-" + s.Scope[:32], ResourceID: s.PhysicalID, Intent: s.Scope, SourcePhysicalID: r.Source.PhysicalID, Snapshot: aws.DBSnapshot{ID: r.Point.Name, ARN: r.Point.ProviderID, InstanceID: r.Source.ExternalID, ResourceID: r.Source.PhysicalID, Type: r.Point.Kind, Engine: r.Point.Engine, EngineVersion: r.Point.Version, Encrypted: r.Point.Encrypted, KMSKey: r.Point.KMSKey, CreatedAt: r.Point.CreatedAt}, Class: cfg.Managed.Profile.ComputeClass, StorageGB: cfg.Managed.Profile.StorageGB, StorageType: cfg.Managed.Profile.StorageType, VPCID: network.VPCID, SubnetGroup: network.SubnetGroup, SecurityGroupID: network.SecurityGroupID, VerifierGroupID: network.VerifierGroupID, ParameterGroup: "default.postgres17", AdminUser: sqlUser}
	if r.Point.Kind == "pitr" && r.Point.PITR != nil && r.Point.RestoreTime != nil {
		spec.PITR = &aws.DBPITRWindow{ARN: r.Point.ProviderID, InstanceID: r.Source.ExternalID, ResourceID: r.Source.PhysicalID, Engine: r.Point.Engine, EngineVersion: r.Point.Version, KMSKey: r.Point.KMSKey, MasterUser: sqlUser, CreatedAt: r.Point.CreatedAt, Earliest: r.Point.PITR.Earliest, Latest: r.Point.PITR.Latest, Encrypted: r.Point.Encrypted, EncryptionKnown: true}
		spec.RestoreTime = *r.Point.RestoreTime
	}
	return spec
}

func restoreRDSRecovery(ctx context.Context, client *aws.Session, spec aws.DBRecoverySpec, c aws.DBRecoveryControl) (*aws.DBInstance, error) {
	if spec.PITR != nil {
		return client.RestoreDBPointInTime(ctx, spec, c)
	}
	return client.RestoreDBSnapshot(ctx, spec, c)
}

func (s *nativeSQLCopyState) rdsMutation(ctx context.Context, c recovery.NativeWorkerControl, remove bool) aws.DBRecoveryControl {
	attempted := s.Uncertain
	if remove {
		attempted = s.DeleteUncertain
	}
	return aws.DBRecoveryControl{Check: c.Check, Mutation: aws.DBMutation{Attempted: attempted, Before: func() error {
		if remove {
			s.DeleteUncertain = true
		} else {
			s.Uncertain = true
		}
		return s.save(ctx, c)
	}, Captured: func(physical string) error { s.PhysicalID = physical; return s.save(ctx, c) }}}
}

func (s *nativeSQLCopyState) rdsGuard(client *aws.Session, r recovery.NativeRestore, cfg *planning.ResourceConfig, c recovery.NativeWorkerControl) func(context.Context) error {
	return func(ctx context.Context) error {
		if s.PhysicalID == "" {
			return recovery.ErrConflict
		}
		_, err := restoreRDSRecovery(ctx, client, s.rdsSpec(r, cfg), aws.DBRecoveryControl{Check: c.Check, Mutation: aws.DBMutation{Attempted: true}})
		return err
	}
}

func (n *NativeDatabases) restoreRDSCopy(ctx context.Context, p security.Principal, r recovery.NativeRestore, c recovery.NativeCopyControl) (recovery.NativeRestoreResult, error) {
	if c.Check == nil || c.Save == nil || c.Selected == nil || p.OrgID != r.OrgID || p.UserID == "" {
		return recovery.NativeRestoreResult{}, recovery.ErrInvalid
	}
	if err := c.Check(ctx); err != nil {
		return recovery.NativeRestoreResult{}, err
	}
	// Keep ambiguous checkpoint acknowledgments resumable, but never turn a
	// definitive lease or authority failure into permission to proceed.
	save := c.Save
	c.Save = func(ctx context.Context, raw json.RawMessage) error {
		err := save(ctx, raw)
		if err == nil {
			return nil
		}
		for _, rejected := range []error{recovery.ErrConflict, recovery.ErrInvalid, recovery.ErrNotFound, security.ErrForbidden, security.ErrUnauthenticated, security.ErrNotMember, security.ErrOrgMismatch, security.ErrNoOrganization} {
			if errors.Is(err, rejected) {
				return err
			}
		}
		return errors.Join(recovery.ErrNativePending, err)
	}
	s, cfg, err := readNativeSQLCopy(r)
	if err != nil {
		return recovery.NativeRestoreResult{}, err
	}
	if s.Deleted || s.DeleteUncertain || !r.ExpiresAt.After(time.Now()) {
		return recovery.NativeRestoreResult{}, recovery.ErrConflict
	}
	client, err := n.nativeRDSSession(ctx, r)
	if err != nil {
		return recovery.NativeRestoreResult{}, err
	}
	passwords, err := n.copyPasswords(ctx, r, s, c.NativeWorkerControl)
	if err != nil {
		return recovery.NativeRestoreResult{}, err
	}
	if s.Stage == "restore" {
		if !s.Uncertain {
			point, pointErr := c.Selected(ctx)
			if pointErr != nil {
				return recovery.NativeRestoreResult{}, pointErr
			}
			if point == nil || point.ProviderID != r.Point.ProviderID || !point.CreatedAt.Equal(r.Point.CreatedAt) || point.Kind != r.Point.Kind || point.Engine != r.Point.Engine || point.Version != r.Point.Version || point.KMSKey != r.Point.KMSKey || !point.Encrypted || point.Status != "available" {
				return recovery.NativeRestoreResult{}, recovery.ErrConflict
			}
		}
		if _, restoreErr := restoreRDSRecovery(ctx, client, s.rdsSpec(r, cfg), s.rdsMutation(ctx, c.NativeWorkerControl, false)); restoreErr != nil {
			return recovery.NativeRestoreResult{}, s.rdsWriteFailure(ctx, c.NativeWorkerControl, restoreErr, false)
		}
		s.Stage, s.Uncertain = "admin", false
		if saveErr := s.save(ctx, c.NativeWorkerControl); saveErr != nil {
			return recovery.NativeRestoreResult{}, saveErr
		}
	}
	guard := s.rdsGuard(client, r, cfg, c.NativeWorkerControl)
	if s.Stage != "admin" || !s.Uncertain {
		if guardErr := guard(ctx); guardErr != nil {
			return recovery.NativeRestoreResult{}, guardErr
		}
	}
	if s.Stage == "admin" {
		if updateErr := client.SetDBRecoveryPassword(ctx, s.rdsSpec(r, cfg), passwords.AdminPassword, s.rdsMutation(ctx, c.NativeWorkerControl, false)); updateErr != nil {
			return recovery.NativeRestoreResult{}, nativeCredentialMutationError{cause: s.rdsWriteFailure(ctx, c.NativeWorkerControl, updateErr, false)}
		}
		s.Stage, s.Uncertain = "verify", false
		if saveErr := s.save(ctx, c.NativeWorkerControl); saveErr != nil {
			return recovery.NativeRestoreResult{}, saveErr
		}
	}
	if verifyErr := n.checkRDSReader(ctx, client, r, s, cfg, passwords, guard, false); verifyErr != nil {
		return recovery.NativeRestoreResult{}, verifyErr
	}
	s.Stage = "ready"
	if saveErr := s.save(ctx, c.NativeWorkerControl); saveErr != nil {
		return recovery.NativeRestoreResult{}, saveErr
	}
	db, err := restoreRDSRecovery(ctx, client, s.rdsSpec(r, cfg), aws.DBRecoveryControl{Check: c.Check, Mutation: aws.DBMutation{Attempted: true}})
	if err != nil {
		return recovery.NativeRestoreResult{}, err
	}
	return recovery.NativeRestoreResult{ExternalID: db.Address + ":5432", PhysicalID: s.PhysicalID, SQLVerified: true}, nil
}

func (n *NativeDatabases) checkRDSReader(ctx context.Context, client *aws.Session, r recovery.NativeRestore, s *nativeSQLCopyState, cfg *planning.ResourceConfig, passwords nativeCopyPasswords, guard func(context.Context) error, existingOnly bool) error {
	db, err := restoreRDSRecovery(ctx, client, s.rdsSpec(r, cfg), aws.DBRecoveryControl{Check: guard, Mutation: aws.DBMutation{Attempted: true}})
	if err != nil {
		return err
	}
	var connector aws.DBConnector
	if n.RDSConnector != nil {
		connector, err = n.RDSConnector(ctx, client, db.ID, db.Address, db.Port)
	} else {
		connector, err = client.PostgresConnector(ctx, db.ID, db.Address, db.Port)
	}
	if err != nil {
		return err
	}
	return verifyNativeReader(ctx, r, s, passwords, guard, connector, sqlUser, existingOnly)
}

func (n *NativeDatabases) deleteRDSCopy(ctx context.Context, p security.Principal, r recovery.NativeRestore, c recovery.NativeWorkerControl) (recovery.NativeCleanupResult, error) {
	if c.Check == nil || c.Save == nil || p.OrgID != r.OrgID || p.UserID == "" {
		return recovery.NativeCleanupResult{}, recovery.ErrInvalid
	}
	if err := c.Check(ctx); err != nil {
		return recovery.NativeCleanupResult{}, err
	}
	s, cfg, err := readNativeSQLCopy(r)
	if err != nil {
		return recovery.NativeCleanupResult{}, err
	}
	client, err := n.nativeRDSSession(ctx, r)
	if err != nil {
		return recovery.NativeCleanupResult{}, err
	}
	if !s.Deleted {
		if s.PhysicalID == "" {
			if s.Uncertain {
				if _, captureErr := client.InspectDBRecoveryForCleanup(ctx, s.rdsSpec(r, cfg), s.rdsMutation(ctx, c, false)); captureErr != nil {
					return recovery.NativeCleanupResult{}, captureErr
				}
			}
			if s.PhysicalID == "" {
				if _, readErr := client.GetDBInstance(ctx, "lp-recovery-"+s.Scope[:32]); !aws.IsNotFound(readErr) {
					if readErr != nil {
						return recovery.NativeCleanupResult{}, readErr
					}
					return recovery.NativeCleanupResult{}, recovery.ErrConflict
				}
			}
		}
		if s.PhysicalID != "" {
			if deleteErr := client.DeleteDBRecovery(ctx, s.rdsSpec(r, cfg), s.rdsMutation(ctx, c, true)); deleteErr != nil {
				return recovery.NativeCleanupResult{}, s.rdsWriteFailure(ctx, c, deleteErr, true)
			}
		}
		s.Deleted, s.DeleteUncertain = true, false
		if saveErr := s.save(ctx, c); saveErr != nil {
			return recovery.NativeCleanupResult{}, saveErr
		}
	}
	if checkErr := c.Check(ctx); checkErr != nil {
		return recovery.NativeCleanupResult{}, checkErr
	}
	if _, absenceErr := client.GetDBInstance(ctx, "lp-recovery-"+s.Scope[:32]); !aws.IsNotFound(absenceErr) {
		if absenceErr != nil {
			return recovery.NativeCleanupResult{}, absenceErr
		}
		return recovery.NativeCleanupResult{}, recovery.ErrConflict
	}
	secretCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if secretErr := n.Secrets.Delete(secretCtx, nativeCopySecretKey(r)); secretErr != nil {
		return recovery.NativeCleanupResult{}, secretErr
	}
	if checkErr := c.Check(ctx); checkErr != nil {
		return recovery.NativeCleanupResult{}, checkErr
	}
	return recovery.NativeCleanupResult{ConfirmedAbsent: true}, nil
}

func (n *NativeDatabases) rdsCopyCredentials(ctx context.Context, p security.Principal, r recovery.NativeRestore, check func(context.Context) error) (*recovery.NativeCopyCredentials, error) {
	if check == nil || p.OrgID != r.OrgID || p.UserID == "" {
		return nil, recovery.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if checkErr := check(ctx); checkErr != nil {
		return nil, checkErr
	}
	now := time.Now()
	if r.Status != "succeeded" || r.VerifiedAt == nil || r.VerifiedAt.Before(r.CreatedAt) || r.VerifiedAt.After(now) || !r.ExpiresAt.After(now) || r.CleanupRequestedAt != nil || r.CleanupBy != "" {
		return nil, recovery.ErrConflict
	}
	s, cfg, err := readNativeSQLCopy(r)
	if err != nil {
		return nil, err
	}
	if s.Stage != "ready" || s.Deleted || s.DeleteUncertain || s.Uncertain || s.PhysicalID == "" || s.PhysicalID != r.PhysicalID {
		return nil, recovery.ErrConflict
	}
	client, err := n.nativeRDSSession(ctx, r)
	if err != nil {
		return nil, err
	}
	guard := s.rdsGuard(client, r, cfg, recovery.NativeWorkerControl{Check: check})
	if guardErr := guard(ctx); guardErr != nil {
		return nil, guardErr
	}
	passwords, err := n.readCopyPasswords(ctx, r, s)
	if err != nil {
		return nil, err
	}
	if verifyErr := n.checkRDSReader(ctx, client, r, s, cfg, passwords, guard, true); verifyErr != nil {
		return nil, verifyErr
	}
	db, err := restoreRDSRecovery(ctx, client, s.rdsSpec(r, cfg), aws.DBRecoveryControl{Check: check, Mutation: aws.DBMutation{Attempted: true}})
	if err != nil {
		return nil, err
	}
	endpoint := db.Address + ":5432"
	if r.ExternalID != endpoint || !r.ExpiresAt.After(time.Now()) {
		return nil, recovery.ErrConflict
	}
	return &recovery.NativeCopyCredentials{CopyID: r.ID, Provider: "rds", ConnectionName: endpoint, Database: databaseName(r.Source.Name), Username: "lp_recovery_" + s.Scope[:32], Password: passwords.ReaderPassword, ExpiresAt: r.ExpiresAt, CheckedAt: time.Now().UTC()}, nil
}

func (s *nativeSQLCopyState) rdsWriteFailure(ctx context.Context, c recovery.NativeWorkerControl, err error, remove bool) error {
	if !remove && aws.DBRecoveryRequestUncertain(err) {
		return errors.Join(recovery.ErrNativePending, err)
	}
	if !aws.DBRecoveryRequestRejected(err) {
		return err
	}
	if remove {
		s.DeleteUncertain = false
	} else {
		s.Uncertain = false
	}
	return errors.Join(err, s.save(ctx, c))
}
