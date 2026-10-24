package aws

import (
	"context"
	"errors"
	"fmt"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
)

// DeleteDBRecovery removes only a pinned temporary copy and its automated
// backups. Source removal has a separate final-snapshot retention contract.
func (s *Session) DeleteDBRecovery(ctx context.Context, spec DBRecoverySpec, control DBRecoveryControl) error {
	if !recoveryIntentRE.MatchString(spec.Intent) || spec.ID != "lp-recovery-"+spec.Intent[:32] || spec.ID == spec.Snapshot.InstanceID || spec.SourcePhysicalID == "" || spec.ResourceID == "" || spec.ResourceID == spec.SourcePhysicalID || control.Check == nil {
		return errors.New("RDS recovery cleanup requires the original temporary copy identity")
	}
	if checkErr := control.Check(ctx); checkErr != nil {
		return checkErr
	}
	db, err := s.GetDBInstance(ctx, spec.ID)
	if IsNotFound(err) {
		if control.Mutation.Attempted {
			return control.Check(ctx)
		}
		return errors.New("RDS recovery disappeared without its captured cleanup intent")
	}
	if err != nil {
		return err
	}
	if identityErr := s.checkDBRecoveryOwner(db, spec); identityErr != nil {
		return identityErr
	}
	if !control.Mutation.Attempted {
		if control.Mutation.Before == nil {
			return errors.New("RDS recovery cleanup requires a durable intent")
		}
		if beforeErr := control.Mutation.Before(); beforeErr != nil {
			return beforeErr
		}
		// Recheck the original instance after the intent checkpoint, before writing.
		if checkErr := control.Check(ctx); checkErr != nil {
			return checkErr
		}
		current, readErr := s.GetDBInstance(ctx, spec.ID)
		if readErr != nil {
			return readErr
		}
		if identityErr := s.checkDBRecoveryOwner(current, spec); identityErr != nil {
			return identityErr
		}
		if checkErr := control.Check(ctx); checkErr != nil {
			return checkErr
		}
		out, deleteErr := s.rdsWrites().DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{DBInstanceIdentifier: sdk.String(spec.ID), SkipFinalSnapshot: sdk.Bool(true), DeleteAutomatedBackups: sdk.Bool(true)})
		if deleteErr != nil {
			return fmt.Errorf("remove temporary RDS recovery copy: %w", deleteErr)
		}
		if out.DBInstance == nil || sdk.ToString(out.DBInstance.DBInstanceIdentifier) != spec.ID || sdk.ToString(out.DBInstance.DbiResourceId) != spec.ResourceID {
			return errors.New("RDS cleanup response did not confirm the original copy")
		}
	}
	for {
		if checkErr := control.Check(ctx); checkErr != nil {
			return checkErr
		}
		current, readErr := s.GetDBInstance(ctx, spec.ID)
		if IsNotFound(readErr) {
			return control.Check(ctx)
		}
		if readErr != nil {
			return readErr
		}
		if identityErr := s.checkDBRecoveryOwner(current, spec); identityErr != nil {
			return identityErr
		}
		// An uncertain request is observed, never issued again. Available can
		// briefly remain visible after acceptance; cancellation bounds the wait.
		if waitErr := s.wait(ctx); waitErr != nil {
			return waitErr
		}
	}
}
