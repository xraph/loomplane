// Package aws talks to Amazon Web Services for Loomplane: it turns an
// access key into SDK clients for one region, and checks what the key may
// do.
package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

// Actions are what an access key needs for Loomplane to build a service
// on CodeBuild into ECR, run it on ECS with Fargate behind a load
// balancer, keep its secrets in Secrets Manager, and provision RDS. A
// connection whose key lacks any of them is incomplete.
var Actions = []string{
	"codebuild:BatchGetBuilds", "codebuild:CreateProject", "codebuild:StartBuild", "codebuild:UpdateProject",
	"ec2:AuthorizeSecurityGroupIngress", "ec2:CreateSecurityGroup", "ec2:DescribeSecurityGroups",
	"ec2:DescribeSubnets", "ec2:DescribeVpcs",
	"ecr:CreateRepository", "ecr:DescribeRepositories",
	"ecs:CreateCluster", "ecs:CreateService", "ecs:DeleteService", "ecs:DescribeClusters", "ecs:DescribeServices",
	"ecs:RegisterTaskDefinition", "ecs:UpdateService",
	"elasticloadbalancing:CreateListener", "elasticloadbalancing:CreateLoadBalancer",
	"elasticloadbalancing:CreateRule", "elasticloadbalancing:CreateTargetGroup",
	"elasticloadbalancing:DescribeListeners", "elasticloadbalancing:DescribeLoadBalancers",
	"elasticloadbalancing:DescribeRules", "elasticloadbalancing:DescribeTargetGroups",
	"iam:AttachRolePolicy", "iam:CreateRole", "iam:GetRole", "iam:PassRole", "iam:PutRolePolicy",
	"logs:CreateLogGroup",
	"rds:CreateDBInstance", "rds:CreateDBSubnetGroup", "rds:DeleteDBInstance", "rds:DescribeDBInstances",
	"rds:DescribeDBSnapshots", "rds:AddTagsToResource",
	"s3:CreateBucket", "s3:DeleteBucket", "s3:DeleteObject", "s3:ListBucket", "s3:PutBucketPublicAccessBlock", "s3:PutObject",
	"elasticache:CreateCacheSubnetGroup", "elasticache:CreateReplicationGroup", "elasticache:DeleteReplicationGroup",
	"elasticache:DescribeReplicationGroups",
	"secretsmanager:CreateSecret", "secretsmanager:PutSecretValue",
	"servicediscovery:CreateHttpNamespace", "servicediscovery:CreateService", "servicediscovery:GetNamespace",
	"servicediscovery:GetOperation", "servicediscovery:ListNamespaces",
}

// CanaryActions are what a key needs on top of Actions to ramp a canary
// on ECS: split a listener between two target groups, wait for the
// candidate's targets, and remove its target group. A canary is opt in,
// so a key without them is still complete. A canary release refuses to
// start instead.
var CanaryActions = []string{
	"elasticloadbalancing:DeleteTargetGroup", "elasticloadbalancing:DescribeTargetHealth",
	"elasticloadbalancing:ModifyListener",
}

// Errors.
var (
	// ErrInvalidKey is something that isn't an access key.
	ErrInvalidKey = errors.New("that isn't an AWS access key")
	// ErrRejected is a key AWS refused.
	ErrRejected = errors.New("aws refused the access key")

	errNotFound = errors.New("not found")
)

var (
	keyIDRE  = regexp.MustCompile(`^(AKIA|ASIA)[A-Z0-9]{12,124}$`)
	regionRE = regexp.MustCompile(`^[a-z]{2}(-gov)?-[a-z]+-\d$`)
)

// Key is an access key, as Loomplane stores it: JSON with the key's ID
// and secret.
type Key struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

// ParseKey reads a stored access key.
func ParseKey(data []byte) (*Key, error) {
	var k Key
	if err := json.Unmarshal(data, &k); err != nil {
		return nil, fmt.Errorf("%w: it isn't the JSON Loomplane stores keys as", ErrInvalidKey)
	}
	k.AccessKeyID, k.SecretAccessKey = strings.TrimSpace(k.AccessKeyID), strings.TrimSpace(k.SecretAccessKey)
	switch {
	case !keyIDRE.MatchString(k.AccessKeyID):
		return nil, fmt.Errorf("%w: the key ID looks like AKIA followed by letters and digits", ErrInvalidKey)
	case len(k.SecretAccessKey) < 20:
		return nil, fmt.Errorf("%w: the secret is too short", ErrInvalidKey)
	}
	return &k, nil
}

// CheckRegion tells whether s could be a region, such as us-east-1.
func CheckRegion(s string) error {
	if !regionRE.MatchString(s) {
		return fmt.Errorf("%q isn't an AWS region, such as us-east-1", s)
	}
	return nil
}

// Client builds SDK configurations.
type Client struct {
	// endpoint replaces every AWS endpoint when set, for tests.
	endpoint string
}

// New returns a Client. An empty endpoint means AWS's own.
func New(endpoint string) *Client { return &Client{endpoint: strings.TrimRight(endpoint, "/")} }

// Config is an SDK configuration that acts as key in region.
func (c *Client) Config(key *Key, region string) sdk.Config {
	cfg := sdk.Config{
		Region:      region,
		Credentials: sdk.NewCredentialsCache(credentials.NewStaticCredentialsProvider(key.AccessKeyID, key.SecretAccessKey, "")),
	}
	if c.endpoint != "" {
		cfg.BaseEndpoint = sdk.String(c.endpoint)
	}
	return cfg
}

// Identity is who an access key acts as.
type Identity struct {
	Account string
	ARN     string
}

// Identify asks STS who key is.
func (c *Client) Identify(ctx context.Context, key *Key, region string) (*Identity, error) {
	out, err := sts.NewFromConfig(c.Config(key, region)).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		if refused(err) {
			return nil, fmt.Errorf("%w: %w", ErrRejected, err)
		}
		return nil, err
	}
	return &Identity{Account: sdk.ToString(out.Account), ARN: sdk.ToString(out.Arn)}, nil
}

// Missing returns which of Actions the key's principal isn't allowed,
// asking IAM's policy simulator. checked is false when the key may not
// run the simulator, so its permissions are unknown.
func (c *Client) Missing(ctx context.Context, key *Key, region, principal string) (missing []string, checked bool, err error) {
	return c.MissingOf(ctx, key, region, principal, Actions)
}

// MissingOf is Missing for any actions.
func (c *Client) MissingOf(ctx context.Context, key *Key, region, principal string, actions []string) (missing []string, checked bool, err error) {
	out, err := iam.NewFromConfig(c.Config(key, region)).SimulatePrincipalPolicy(ctx, &iam.SimulatePrincipalPolicyInput{
		PolicySourceArn: sdk.String(principal), ActionNames: actions,
	})
	if err != nil {
		if refused(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	for _, r := range out.EvaluationResults {
		if r.EvalDecision != "allowed" {
			missing = append(missing, sdk.ToString(r.EvalActionName))
		}
	}
	slices.Sort(missing)
	return missing, true, nil
}

// PrincipalARN is what the policy simulator takes for an identity STS
// names: an assumed role's session ARN names the role instead.
func PrincipalARN(arn string) string {
	if !strings.Contains(arn, ":assumed-role/") {
		return arn
	}
	parts := strings.Split(arn, ":")
	if len(parts) < 6 {
		return arn
	}
	role := strings.Split(strings.TrimPrefix(parts[5], "assumed-role/"), "/")[0]
	return "arn:aws:iam::" + parts[4] + ":role/" + role
}

// refused reports whether AWS turned the request down for its key or its
// permissions, as opposed to failing to answer.
func refused(err error) bool {
	var ae smithy.APIError
	if !errors.As(err, &ae) {
		return false
	}
	switch ae.ErrorCode() {
	case "InvalidClientTokenId", "SignatureDoesNotMatch", "AccessDenied", "AccessDeniedException",
		"UnrecognizedClientException", "ExpiredToken", "InvalidAccessKeyId":
		return true
	}
	return false
}

// IsNotFound reports whether err is AWS saying something doesn't exist.
func IsNotFound(err error) bool {
	if errors.Is(err, errNotFound) {
		return true
	}
	var ae smithy.APIError
	if !errors.As(err, &ae) {
		return false
	}
	code := ae.ErrorCode()
	return strings.Contains(code, "NotFound") || code == "NoSuchEntity" || code == "RepositoryNotFoundException"
}

// IsExists reports whether err is AWS saying something already exists.
func IsExists(err error) bool {
	var ae smithy.APIError
	if !errors.As(err, &ae) {
		return false
	}
	code := ae.ErrorCode()
	return strings.Contains(code, "AlreadyExists") || code == "EntityAlreadyExists" || code == "BucketAlreadyOwnedByYou" ||
		code == "InvalidGroup.Duplicate" || code == "DuplicateLoadBalancerName" || code == "DuplicateTargetGroupName" ||
		code == "InvalidPermission.Duplicate" || code == "ResourceAlreadyExistsException"
}
