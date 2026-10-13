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

// bucketName is a resource's S3 bucket: the one its binding names, or a
// new one's name, lower case, at most 63 long, with the session's region
// in it, and ending in its environment's key, which the tasks role grants.
// Bucket names are global, and creating one the account already has
// elsewhere answers with that one, so a move to a connection in another
// region needs a name the old bucket doesn't hold.
func bucketName(s *aws.Session, job *Job) string {
	if b := job.Binding; b != nil && b.ExternalID != "" {
		return b.ExternalID
	}
	suffix := "-" + awsEnvKey(s, job)
	if region := s.Region(); region != "" {
		suffix = "-" + region + suffix
	}
	room := 63 - len("lp-") - len(suffix)
	name := job.Action.Name
	if len(name) > room {
		name = name[:room]
	}
	return "lp-" + name + suffix
}
