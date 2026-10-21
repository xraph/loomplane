package aws

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"

	"github.com/xraph/loomplane/internal/clouds/progress"
	"github.com/xraph/loomplane/internal/manageddb"
)

// AllowPortFromGroup opens one TCP port on a group to another group.
func (s *Session) AllowPortFromGroup(ctx context.Context, group, from string, port int32) error {
	_, err := ec2.NewFromConfig(s.cfg).AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: sdk.String(group), IpPermissions: []ec2types.IpPermission{{
			IpProtocol: sdk.String("tcp"), FromPort: sdk.Int32(port), ToPort: sdk.Int32(port),
			UserIdGroupPairs: []ec2types.UserIdGroupPair{{GroupId: sdk.String(from)}},
		}},
	})
	if err != nil && !IsExists(err) {
		return fmt.Errorf("let %s reach port %d on %s: %w", from, port, group, err)
	}
	return nil
}

// EnsureDBSubnetGroup creates a DB subnet group over subnets, if there
// isn't one by that name.
func (s *Session) EnsureDBSubnetGroup(ctx context.Context, name string, subnets []string) error {
	_, err := rds.NewFromConfig(s.cfg).CreateDBSubnetGroup(ctx, &rds.CreateDBSubnetGroupInput{
		DBSubnetGroupName: sdk.String(name), DBSubnetGroupDescription: sdk.String("Loomplane databases"),
		SubnetIds: subnets,
	})
	if err != nil && !IsExists(err) {
		return fmt.Errorf("create db subnet group %s: %w", name, err)
	}
	return nil
}

// DBInstanceSpec is an RDS instance Loomplane creates.
type DBInstanceSpec struct {
	ID            string
	Engine        string // postgres or mysql
	EngineVersion string // a major version, such as 17 or 8.4
	Class         string
	StorageGB     int32
	Database      string
	User          string
	Password      string
	SubnetGroup   string
	Group         string
}

// DBInstance is an RDS instance as it is.
type DBInstance struct {
	ID            string
	ResourceID    string
	Engine        string
	EngineVersion string
	Status        string
	Address       string
	Port          int32
}

// GetDBInstance reads an instance. It returns an error IsNotFound
// recognises when there isn't one.
func (s *Session) GetDBInstance(ctx context.Context, id string) (*DBInstance, error) {
	out, err := rds.NewFromConfig(s.cfg).DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: sdk.String(id)})
	if err != nil {
		return nil, err
	}
	if len(out.DBInstances) == 0 {
		return nil, fmt.Errorf("db instance %s: %w", id, errNotFound)
	}
	in := out.DBInstances[0]
	db := &DBInstance{
		ID: sdk.ToString(in.DBInstanceIdentifier), Engine: sdk.ToString(in.Engine),
		ResourceID:    sdk.ToString(in.DbiResourceId),
		EngineVersion: sdk.ToString(in.EngineVersion), Status: sdk.ToString(in.DBInstanceStatus),
	}
	if in.Endpoint != nil {
		db.Address, db.Port = sdk.ToString(in.Endpoint.Address), sdk.ToInt32(in.Endpoint.Port)
	}
	return db, nil
}

// EnsureDBInstance creates the instance if there isn't one, and waits
// until it's available. Creating one takes several minutes; an instance
// that already exists keeps the password it was created with, and
// created reports whether this call made it.
func (s *Session) EnsureDBInstance(ctx context.Context, spec DBInstanceSpec) (db *DBInstance, created bool, err error) {
	if _, err = s.GetDBInstance(ctx, spec.ID); IsNotFound(err) {
		defaults, defaultsErr := manageddb.Defaults("rds")
		if defaultsErr != nil {
			return nil, false, defaultsErr
		}
		_, err = rds.NewFromConfig(s.cfg).CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
			DBInstanceIdentifier: sdk.String(spec.ID), DBInstanceClass: sdk.String(spec.Class),
			Engine: sdk.String(spec.Engine), EngineVersion: sdk.String(spec.EngineVersion),
			AllocatedStorage: sdk.Int32(spec.StorageGB), StorageType: sdk.String(defaults.StorageType), StorageEncrypted: sdk.Bool(defaults.StorageEncrypted), MultiAZ: sdk.Bool(false),
			DBName: sdk.String(spec.Database), MasterUsername: sdk.String(spec.User), MasterUserPassword: sdk.String(spec.Password),
			DBSubnetGroupName: sdk.String(spec.SubnetGroup), VpcSecurityGroupIds: []string{spec.Group},
			PubliclyAccessible: sdk.Bool(false), BackupRetentionPeriod: sdk.Int32(defaults.BackupRetentionValue), CopyTagsToSnapshot: sdk.Bool(true),
		})
		if err != nil && !IsExists(err) {
			return nil, false, fmt.Errorf("create db instance %s: %w", spec.ID, err)
		}
		created = err == nil
	} else if err != nil {
		return nil, false, fmt.Errorf("read db instance %s: %w", spec.ID, err)
	}
	for {
		if db, err = s.GetDBInstance(ctx, spec.ID); err != nil {
			return nil, created, fmt.Errorf("read db instance %s: %w", spec.ID, err)
		}
		switch db.Status {
		case "available":
			return db, created, nil
		case "failed", "incompatible-parameters", "incompatible-network", "storage-full", "deleting":
			return nil, created, fmt.Errorf("db instance %s is %s", spec.ID, db.Status)
		}
		progress.Report(ctx, "The database is "+db.Status)
		if werr := s.wait(ctx); werr != nil {
			return nil, created, werr
		}
	}
}

// SetDBPassword changes the instance's master password.
func (s *Session) SetDBPassword(ctx context.Context, id, password string) error {
	_, err := rds.NewFromConfig(s.cfg).ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier: sdk.String(id), MasterUserPassword: sdk.String(password), ApplyImmediately: sdk.Bool(true),
	})
	if err != nil {
		return fmt.Errorf("set the password on %s: %w", id, err)
	}
	return nil
}

// DeleteDBInstance removes compute while retaining automated backups and a
// final snapshot. Failure to create that snapshot must block removal.
func (s *Session) DeleteDBInstance(ctx context.Context, id string) error {
	db, err := s.GetDBInstance(ctx, id)
	if IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read db instance before removal: %w", err)
	}
	if db.Status == "deleting" {
		return nil
	}
	if db.ResourceID == "" {
		return errors.New("the database has no physical resource identity; removal is blocked")
	}
	// Stable for retries, distinct if an identifier is reused for another DB.
	sum := sha256.Sum256([]byte(db.ResourceID))
	snapshot := fmt.Sprintf("lp-final-%x", sum[:20])
	_, err = rds.NewFromConfig(s.cfg).DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
		DBInstanceIdentifier: sdk.String(id), SkipFinalSnapshot: sdk.Bool(false), DeleteAutomatedBackups: sdk.Bool(false),
		FinalDBSnapshotIdentifier: sdk.String(snapshot),
	})
	if err != nil && !IsNotFound(err) {
		return fmt.Errorf("delete db instance %s: %w", id, err)
	}
	return nil
}
