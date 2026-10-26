package aws

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"

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
	ResourceID    string
	Intent        string
	Existing      bool
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
	Recovery      *DBRecoverySettings
	Tags          map[string]string
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
	if len(out.DBInstances) != 1 || sdk.ToString(out.DBInstances[0].DBInstanceIdentifier) != id {
		return nil, errors.New("RDS returned a different database identity")
	}
	return readDBInstance(out.DBInstances[0]), nil
}

func readDBInstance(in rdstypes.DBInstance) *DBInstance {
	db := &DBInstance{ID: sdk.ToString(in.DBInstanceIdentifier), ResourceID: sdk.ToString(in.DbiResourceId), Engine: sdk.ToString(in.Engine), EngineVersion: sdk.ToString(in.EngineVersion), Status: sdk.ToString(in.DBInstanceStatus), Tags: map[string]string{}}
	db.Recovery = readDBRecoverySettings(in)
	for _, tag := range in.TagList {
		db.Tags[sdk.ToString(tag.Key)] = sdk.ToString(tag.Value)
	}
	if in.Endpoint != nil {
		db.Address, db.Port = sdk.ToString(in.Endpoint.Address), sdk.ToInt32(in.Endpoint.Port)
	}
	return db
}

// DBMutation links a stable creation intent and physical identity to the
// deployment checkpoint. Native writes are never automatically retried.
type DBMutation struct {
	Attempted bool
	Before    func() error
	Captured  func(string) error
}

func (s *Session) rdsWrites() *rds.Client {
	return rds.NewFromConfig(s.cfg, func(o *rds.Options) { o.Retryer = sdk.NopRetryer{} })
}

func checkDBInstance(db *DBInstance, spec DBInstanceSpec) error {
	if db == nil || db.ID != spec.ID || db.ResourceID == "" || (spec.ResourceID != "" && db.ResourceID != spec.ResourceID) || db.Engine != spec.Engine || (db.EngineVersion != spec.EngineVersion && !strings.HasPrefix(db.EngineVersion, spec.EngineVersion+".")) {
		return errors.New("RDS database no longer matches its physical identity and engine")
	}
	return nil
}

// EnsureDBInstance creates once and captures the immutable physical ID before
// waiting. A lost response can reconcile only the original intent tag.
func (s *Session) EnsureDBInstance(ctx context.Context, spec DBInstanceSpec, track DBMutation) (*DBInstance, error) {
	db, err := s.GetDBInstance(ctx, spec.ID)
	existing := err == nil
	if IsNotFound(err) {
		if spec.ResourceID != "" || track.Attempted {
			return nil, errors.New("the original RDS creation is not visible; reconcile it before creating another database")
		}
		if spec.Intent == "" || track.Before == nil || track.Captured == nil {
			return nil, errors.New("RDS creation requires a durable intent and physical identity checkpoint")
		}
		if beforeErr := track.Before(); beforeErr != nil {
			return nil, beforeErr
		}
		defaults, defaultsErr := manageddb.Defaults("rds")
		if defaultsErr != nil {
			return nil, defaultsErr
		}
		out, createErr := s.rdsWrites().CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
			DBInstanceIdentifier: sdk.String(spec.ID), DBInstanceClass: sdk.String(spec.Class), Engine: sdk.String(spec.Engine), EngineVersion: sdk.String(spec.EngineVersion),
			AllocatedStorage: sdk.Int32(spec.StorageGB), StorageType: sdk.String(defaults.StorageType), StorageEncrypted: sdk.Bool(defaults.StorageEncrypted), MultiAZ: sdk.Bool(false),
			DBName: sdk.String(spec.Database), MasterUsername: sdk.String(spec.User), MasterUserPassword: sdk.String(spec.Password), DBSubnetGroupName: sdk.String(spec.SubnetGroup), VpcSecurityGroupIds: []string{spec.Group},
			PubliclyAccessible: sdk.Bool(false), BackupRetentionPeriod: sdk.Int32(defaults.BackupRetentionValue), CopyTagsToSnapshot: sdk.Bool(true),
			Tags: []rdstypes.Tag{{Key: sdk.String("loomplane-operation"), Value: sdk.String(spec.Intent)}},
		})
		if createErr != nil {
			if !IsExists(createErr) {
				return nil, fmt.Errorf("create RDS database: %w", createErr)
			}
			db, err = s.GetDBInstance(ctx, spec.ID)
			existing = true
		} else {
			if out.DBInstance == nil {
				return nil, errors.New("RDS creation returned no physical database identity")
			}
			db = readDBInstance(*out.DBInstance)
			err = nil
		}
	}
	if err != nil {
		return nil, err
	}
	if checkErr := checkDBInstance(db, spec); checkErr != nil {
		return nil, checkErr
	}
	if existing && spec.ResourceID == "" && !spec.Existing && (spec.Intent == "" || db.Tags["loomplane-operation"] != spec.Intent) {
		return nil, errors.New("RDS database already exists without its original intent or binding; review ownership before adoption")
	}
	if spec.ResourceID == "" {
		if track.Captured == nil {
			return nil, errors.New("RDS physical identity requires a durable checkpoint")
		}
		if captureErr := track.Captured(db.ResourceID); captureErr != nil {
			return nil, captureErr
		}
		spec.ResourceID = db.ResourceID
	}
	for {
		if checkErr := checkDBInstance(db, spec); checkErr != nil {
			return nil, checkErr
		}
		switch db.Status {
		case "available":
			return db, nil
		case "failed", "incompatible-parameters", "incompatible-restore", "incompatible-network", "storage-full", "deleting":
			return nil, fmt.Errorf("RDS database is %s", db.Status)
		}
		progress.Report(ctx, "The database is "+db.Status)
		if waitErr := s.wait(ctx); waitErr != nil {
			return nil, waitErr
		}
		db, err = s.GetDBInstance(ctx, spec.ID)
		if err != nil {
			return nil, err
		}
	}
}

// DBSnapshot is native recovery metadata. It is separate from Trove backup files.
type DBSnapshot struct {
	StorageGB       int32     `json:"storage_gb,omitempty"`
	MasterUser      string    `json:"master_user,omitempty"`
	ID              string    `json:"id"`
	ARN             string    `json:"arn"`
	InstanceID      string    `json:"instance_id"`
	ResourceID      string    `json:"resource_id"`
	Status          string    `json:"status"`
	Type            string    `json:"type"`
	Engine          string    `json:"engine,omitempty"`
	EngineVersion   string    `json:"engine_version,omitempty"`
	Encrypted       bool      `json:"encrypted"`
	EncryptionKnown bool      `json:"-"`
	KMSKey          string    `json:"kms_key,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// FinalDBSnapshotID remains stable across retries and changes with incarnation.
func FinalDBSnapshotID(resourceID string) string {
	sum := sha256.Sum256([]byte(resourceID))
	return fmt.Sprintf("lp-final-%x", sum[:20])
}

// GetDBSnapshot reads one native snapshot without changing it.
func (s *Session) GetDBSnapshot(ctx context.Context, id string) (*DBSnapshot, error) {
	out, err := rds.NewFromConfig(s.cfg).DescribeDBSnapshots(ctx, &rds.DescribeDBSnapshotsInput{DBSnapshotIdentifier: sdk.String(id)})
	if err != nil {
		return nil, err
	}
	if len(out.DBSnapshots) == 0 {
		return nil, errNotFound
	}
	if len(out.DBSnapshots) != 1 || sdk.ToString(out.DBSnapshots[0].DBSnapshotIdentifier) != id {
		return nil, errors.New("RDS returned a different snapshot identity")
	}
	return readDBSnapshot(out.DBSnapshots[0]), nil
}

// DBRemovalSpec pins both the original database and its required final snapshot.
type DBRemovalSpec struct{ ID, ResourceID, SnapshotID string }

// DBRemoval checkpoints intent before deletion and acceptance before waiting.
type DBRemoval struct {
	Attempted bool
	Before    func() error
	Accepted  func() error
}

func (s *Session) checkFinalSnapshot(point *DBSnapshot, spec DBRemovalSpec) error {
	if err := s.checkSnapshotIdentity(point, spec.ResourceID); err != nil {
		return err
	}
	if point.ID != spec.SnapshotID || point.InstanceID != spec.ID || point.Type != "manual" {
		return errors.New("RDS final snapshot does not match the original physical database and account")
	}
	if point.Status != "available" && point.Status != "creating" {
		return fmt.Errorf("RDS final snapshot is %s", point.Status)
	}
	return nil
}

// DeleteDBInstance retains automated backups and waits for a verified available
// final snapshot and source disappearance. An uncertain retry never deletes again.
func (s *Session) DeleteDBInstance(ctx context.Context, spec DBRemovalSpec, track DBRemoval) (*DBSnapshot, error) {
	if spec.ID == "" || spec.ResourceID == "" || spec.SnapshotID != FinalDBSnapshotID(spec.ResourceID) {
		return nil, errors.New("RDS removal requires the original physical database and final snapshot identity")
	}
	db, err := s.GetDBInstance(ctx, spec.ID)
	if err != nil && !IsNotFound(err) {
		return nil, err
	}
	if err == nil && db.ResourceID != spec.ResourceID {
		return nil, errors.New("RDS database physical identity changed; the replacement will not be removed")
	}
	if !track.Attempted {
		if IsNotFound(err) || db.Status == "deleting" {
			return nil, errors.New("RDS removal is already missing or in progress without its captured recovery intent")
		}
		if _, snapshotErr := s.GetDBSnapshot(ctx, spec.SnapshotID); !IsNotFound(snapshotErr) {
			if snapshotErr != nil {
				return nil, snapshotErr
			}
			return nil, errors.New("the RDS final snapshot identity already exists; reconcile it before removal")
		}
		if track.Before == nil || track.Accepted == nil {
			return nil, errors.New("RDS removal requires a durable checkpoint")
		}
		if beforeErr := track.Before(); beforeErr != nil {
			return nil, beforeErr
		}
		out, deleteErr := s.rdsWrites().DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{DBInstanceIdentifier: sdk.String(spec.ID), SkipFinalSnapshot: sdk.Bool(false), DeleteAutomatedBackups: sdk.Bool(false), FinalDBSnapshotIdentifier: sdk.String(spec.SnapshotID)})
		if deleteErr != nil {
			return nil, fmt.Errorf("remove RDS database with final snapshot: %w", deleteErr)
		}
		if out.DBInstance == nil || sdk.ToString(out.DBInstance.DBInstanceIdentifier) != spec.ID || sdk.ToString(out.DBInstance.DbiResourceId) != spec.ResourceID {
			return nil, errors.New("RDS removal response did not confirm the original physical database")
		}
		if acceptedErr := track.Accepted(); acceptedErr != nil {
			return nil, acceptedErr
		}
	}
	for {
		db, readErr := s.GetDBInstance(ctx, spec.ID)
		missing := IsNotFound(readErr)
		if readErr != nil && !missing {
			return nil, readErr
		}
		if !missing && db.ResourceID != spec.ResourceID {
			return nil, errors.New("RDS database physical identity changed during removal")
		}
		point, snapshotErr := s.GetDBSnapshot(ctx, spec.SnapshotID)
		if snapshotErr != nil && !IsNotFound(snapshotErr) {
			return nil, snapshotErr
		}
		if snapshotErr == nil {
			if checkErr := s.checkFinalSnapshot(point, spec); checkErr != nil {
				return nil, checkErr
			}
			if missing && point.Status == "available" {
				return point, nil
			}
		}
		progress.Report(ctx, "Waiting for the original RDS database to disappear and its final snapshot to become available")
		if waitErr := s.wait(ctx); waitErr != nil {
			return nil, waitErr
		}
	}
}
