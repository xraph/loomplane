package aws

import (
	"context"
	"errors"
	"regexp"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
)

var rdsRecoveryPassword = regexp.MustCompile(`^[A-Za-z0-9]{40,128}$`)

// SetDBRecoveryPassword changes only the pinned temporary administrator. An
// attempted update is observed without replaying it. The subsequent SQL login
// with the kept password, not native availability, proves it actually applied.
func (s *Session) SetDBRecoveryPassword(ctx context.Context, spec DBRecoverySpec, password string, c DBRecoveryControl) error {
	if err := s.validDBRecovery(spec, c); err != nil {
		return err
	}
	if spec.ResourceID == "" || !rdsRecoveryPassword.MatchString(password) {
		return errors.New("RDS recovery requires its pinned copy and kept administrator password")
	}
	if err := c.Check(ctx); err != nil {
		return err
	}
	db, err := s.GetDBInstance(ctx, spec.ID)
	if err != nil {
		return err
	}
	if identityErr := s.checkDBRecoveryIdentity(db, spec); identityErr != nil {
		return identityErr
	}
	if !c.Mutation.Attempted {
		if c.Mutation.Before == nil {
			return errors.New("RDS recovery password requires a durable update intent")
		}
		if db.Status != "available" {
			return errors.New("RDS recovery copy is not ready for its password update")
		}
		if settingsErr := checkDBRecoverySettings(db, spec); settingsErr != nil {
			return settingsErr
		}
		if beforeErr := c.Mutation.Before(); beforeErr != nil {
			return beforeErr
		}
		if checkErr := c.Check(ctx); checkErr != nil {
			return checkErr
		}
		current, readErr := s.GetDBInstance(ctx, spec.ID)
		if readErr != nil {
			return readErr
		}
		if identityErr := s.checkDBRecoveryIdentity(current, spec); identityErr != nil {
			return identityErr
		}
		if settingsErr := checkDBRecoverySettings(current, spec); settingsErr != nil {
			return settingsErr
		}
		if checkErr := c.Check(ctx); checkErr != nil {
			return checkErr
		}
		out, modifyErr := s.rdsWrites().ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{DBInstanceIdentifier: sdk.String(spec.ID), MasterUserPassword: sdk.String(password), ApplyImmediately: sdk.Bool(true)})
		if modifyErr != nil {
			return errors.New("update the temporary RDS recovery administrator")
		}
		if out.DBInstance == nil {
			return errors.New("RDS recovery password update returned no copy")
		}
		db = readDBInstance(*out.DBInstance)
	}
	for {
		if checkErr := c.Check(ctx); checkErr != nil {
			return checkErr
		}
		if identityErr := s.checkDBRecoveryIdentity(db, spec); identityErr != nil {
			return identityErr
		}
		if db.Status == "available" && !db.Recovery.Pending {
			return checkDBRecoverySettings(db, spec)
		}
		switch db.Status {
		case "failed", "incompatible-parameters", "incompatible-restore", "incompatible-network", "storage-full", "deleting":
			return errors.New("RDS recovery copy cannot accept SQL verification")
		}
		if waitErr := s.wait(ctx); waitErr != nil {
			return waitErr
		}
		if checkErr := c.Check(ctx); checkErr != nil {
			return checkErr
		}
		db, err = s.GetDBInstance(ctx, spec.ID)
		if err != nil {
			return err
		}
	}
}
