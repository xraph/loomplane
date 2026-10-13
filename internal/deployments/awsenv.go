package deployments

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/xraph/loomplane/internal/clouds/aws"
)

// awsEnvKey names an environment's resources in an AWS account: every
// bucket of the environment ends in it, so its tasks' role can reach
// those buckets and no others.
func awsEnvKey(s *aws.Session, job *Job) string {
	sum := sha256.Sum256([]byte(s.Account() + "\x00" + job.Deployment.ProjectID + "\x00" + job.Deployment.Environment))
	return hex.EncodeToString(sum[:])[:10]
}

// tasksRole is the IAM role an environment's ECS tasks run as: what the
// application itself may do in the account, which is reach its own
// environment's buckets.
func tasksRole(s *aws.Session, job *Job) (name, policy string) {
	key := awsEnvKey(s, job)
	name = "loomplane-" + awsEnvironment(job) + "-tasks"
	if len(name) > 64 {
		name = "loomplane-tasks-" + key
	}
	buckets := "arn:aws:s3:::lp-*-" + key
	policy = `{"Version":"2012-10-17","Statement":[` +
		`{"Effect":"Allow","Action":["s3:ListBucket","s3:GetBucketLocation"],"Resource":"` + buckets + `"},` +
		`{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject","s3:DeleteObject"],"Resource":"` + buckets + `/*"}]}`
	return name, policy
}

// bucketName is a resource's S3 bucket: lower case, at most 63 long, and
// ending in its environment's key.
func bucketName(s *aws.Session, job *Job) string {
	key := awsEnvKey(s, job)
	room := 63 - len("lp-") - len(key) - 1
	name := job.Action.Name
	if len(name) > room {
		name = name[:room]
	}
	return "lp-" + name + "-" + key
}
