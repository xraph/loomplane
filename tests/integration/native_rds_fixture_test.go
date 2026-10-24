//go:build integration

package integration

import (
	"fmt"
	"time"
)

func nativeRDSInstanceXML(name, physical, intent, kms string) string {
	return fmt.Sprintf(`<DBInstance><DBInstanceIdentifier>%s</DBInstanceIdentifier><DbiResourceId>%s</DbiResourceId><DBInstanceArn>arn:aws:rds:us-east-1:123456789012:db:%s</DBInstanceArn><Engine>postgres</Engine><EngineVersion>17.2</EngineVersion><DBInstanceStatus>available</DBInstanceStatus><DBInstanceClass>db.t4g.micro</DBInstanceClass><AllocatedStorage>20</AllocatedStorage><StorageType>gp3</StorageType><StorageEncrypted>true</StorageEncrypted><KmsKeyId>%s</KmsKeyId><MasterUsername>app</MasterUsername><PubliclyAccessible>false</PubliclyAccessible><MultiAZ>false</MultiAZ><AutoMinorVersionUpgrade>false</AutoMinorVersionUpgrade><BackupRetentionPeriod>0</BackupRetentionPeriod><DeletionProtection>false</DeletionProtection><DBSubnetGroup><DBSubnetGroupName>recovery-private</DBSubnetGroupName><VpcId>vpc-recovery</VpcId></DBSubnetGroup><VpcSecurityGroups><VpcSecurityGroupMembership><VpcSecurityGroupId>sg-copy</VpcSecurityGroupId><Status>active</Status></VpcSecurityGroupMembership></VpcSecurityGroups><DBParameterGroups><DBParameterGroup><DBParameterGroupName>default.postgres17</DBParameterGroupName><ParameterApplyStatus>in-sync</ParameterApplyStatus></DBParameterGroup></DBParameterGroups><Endpoint><Address>%s.abcdef.us-east-1.rds.amazonaws.com</Address><Port>5432</Port></Endpoint><TagList><Tag><Key>loomplane-native-recovery</Key><Value>%s</Value></Tag></TagList></DBInstance>`, name, physical, name, kms, name, intent)
}

func nativeRDSSnapshotXML(name, source, kms string, created time.Time) string {
	return fmt.Sprintf(`<DBSnapshot><DBSnapshotIdentifier>%s</DBSnapshotIdentifier><DBSnapshotArn>arn:aws:rds:us-east-1:123456789012:snapshot:%s</DBSnapshotArn><DBInstanceIdentifier>%s</DBInstanceIdentifier><DbiResourceId>db-source</DbiResourceId><SnapshotType>manual</SnapshotType><SnapshotCreateTime>%s</SnapshotCreateTime><Status>available</Status><Engine>postgres</Engine><EngineVersion>17.2</EngineVersion><Encrypted>true</Encrypted><KmsKeyId>%s</KmsKeyId><AllocatedStorage>20</AllocatedStorage><MasterUsername>app</MasterUsername></DBSnapshot>`, name, name, source, created.Format(time.RFC3339Nano), kms)
}
