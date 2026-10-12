package aws

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	cbtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/xraph/loomplane/internal/clouds/progress"
)

// Session acts as one access key in one region.
type Session struct {
	c       *Client
	key     *Key
	region  string
	account string
	cfg     sdk.Config
}

// Session returns a session for key in region. account is the key's
// account, from the connection's check.
func (c *Client) Session(key *Key, region, account string) *Session {
	return &Session{c: c, key: key, region: region, account: account, cfg: c.Config(key, region)}
}

// Region is the session's region.
func (s *Session) Region() string { return s.region }

// Account is the session's account.
func (s *Session) Account() string { return s.account }

// Config is the session's SDK configuration.
func (s *Session) Config() sdk.Config { return s.cfg }

// wait sleeps between polls: briefly against a test's fake.
func (s *Session) wait(ctx context.Context) error {
	d := 5 * time.Second
	if s.c.endpoint != "" {
		d = 100 * time.Millisecond
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// --- Network ---

// Network is the default VPC and its subnets.
type Network struct {
	VPC     string
	Subnets []string
}

// DefaultNetwork finds the region's default VPC and its subnets, which
// give tasks public addresses so they can pull images without a NAT.
func (s *Session) DefaultNetwork(ctx context.Context) (*Network, error) {
	cli := ec2.NewFromConfig(s.cfg)
	vpcs, err := cli.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		Filters: []ec2types.Filter{{Name: sdk.String("is-default"), Values: []string{"true"}}},
	})
	if err != nil {
		return nil, fmt.Errorf("find the default vpc: %w", err)
	}
	if len(vpcs.Vpcs) == 0 {
		return nil, fmt.Errorf("%s has no default VPC; Loomplane runs services in it", s.region)
	}
	n := &Network{VPC: sdk.ToString(vpcs.Vpcs[0].VpcId)}
	subnets, err := cli.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		Filters: []ec2types.Filter{{Name: sdk.String("vpc-id"), Values: []string{n.VPC}}},
	})
	if err != nil {
		return nil, fmt.Errorf("find the default subnets: %w", err)
	}
	for _, sn := range subnets.Subnets {
		n.Subnets = append(n.Subnets, sdk.ToString(sn.SubnetId))
	}
	if len(n.Subnets) < 2 {
		return nil, fmt.Errorf("the default VPC has %d subnets; a load balancer needs two", len(n.Subnets))
	}
	return n, nil
}

// EnsureSecurityGroup creates a security group in vpc, if there isn't one
// by that name, and returns its ID.
func (s *Session) EnsureSecurityGroup(ctx context.Context, vpc, name, description string) (string, error) {
	cli := ec2.NewFromConfig(s.cfg)
	found, err := cli.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{Filters: []ec2types.Filter{
		{Name: sdk.String("vpc-id"), Values: []string{vpc}}, {Name: sdk.String("group-name"), Values: []string{name}},
	}})
	if err != nil {
		return "", fmt.Errorf("find security group %s: %w", name, err)
	}
	if len(found.SecurityGroups) > 0 {
		return sdk.ToString(found.SecurityGroups[0].GroupId), nil
	}
	out, err := cli.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName: sdk.String(name), Description: sdk.String(description), VpcId: sdk.String(vpc),
	})
	if err != nil {
		return "", fmt.Errorf("create security group %s: %w", name, err)
	}
	return sdk.ToString(out.GroupId), nil
}

// AllowFromAnywhere opens a TCP port on a group to the internet.
func (s *Session) AllowFromAnywhere(ctx context.Context, group string, port int32) error {
	_, err := ec2.NewFromConfig(s.cfg).AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: sdk.String(group), IpPermissions: []ec2types.IpPermission{{
			IpProtocol: sdk.String("tcp"), FromPort: sdk.Int32(port), ToPort: sdk.Int32(port),
			IpRanges: []ec2types.IpRange{{CidrIp: sdk.String("0.0.0.0/0")}},
		}},
	})
	if err != nil && !IsExists(err) {
		return fmt.Errorf("open port %d: %w", port, err)
	}
	return nil
}

// AllowFromGroup opens every TCP port on a group to another group.
func (s *Session) AllowFromGroup(ctx context.Context, group, from string) error {
	_, err := ec2.NewFromConfig(s.cfg).AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: sdk.String(group), IpPermissions: []ec2types.IpPermission{{
			IpProtocol: sdk.String("tcp"), FromPort: sdk.Int32(0), ToPort: sdk.Int32(65535),
			UserIdGroupPairs: []ec2types.UserIdGroupPair{{GroupId: sdk.String(from)}},
		}},
	})
	if err != nil && !IsExists(err) {
		return fmt.Errorf("let %s reach %s: %w", from, group, err)
	}
	return nil
}

// --- IAM ---

// EnsureRole creates a role a service may assume, if it isn't there, with
// the managed policies and inline policy given, and returns its ARN.
func (s *Session) EnsureRole(ctx context.Context, name, service string, managed []string, inline string) (string, error) {
	cli := iam.NewFromConfig(s.cfg)
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"` + service + `"},"Action":"sts:AssumeRole"}]}`
	var arn string
	got, err := cli.GetRole(ctx, &iam.GetRoleInput{RoleName: sdk.String(name)})
	switch {
	case err == nil:
		arn = sdk.ToString(got.Role.Arn)
	case IsNotFound(err):
		out, cerr := cli.CreateRole(ctx, &iam.CreateRoleInput{
			RoleName: sdk.String(name), AssumeRolePolicyDocument: sdk.String(trust),
			Description: sdk.String("Created by Loomplane"),
		})
		if cerr != nil && !IsExists(cerr) {
			return "", fmt.Errorf("create role %s: %w", name, cerr)
		}
		if out != nil {
			arn = sdk.ToString(out.Role.Arn)
		} else {
			arn = "arn:aws:iam::" + s.account + ":role/" + name
		}
	default:
		return "", fmt.Errorf("read role %s: %w", name, err)
	}
	for _, p := range managed {
		if _, err := cli.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{RoleName: sdk.String(name), PolicyArn: sdk.String(p)}); err != nil {
			return "", fmt.Errorf("attach %s to %s: %w", p, name, err)
		}
	}
	if inline != "" {
		if _, err := cli.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
			RoleName: sdk.String(name), PolicyName: sdk.String("loomplane"), PolicyDocument: sdk.String(inline),
		}); err != nil {
			return "", fmt.Errorf("set %s's policy: %w", name, err)
		}
	}
	return arn, nil
}

// --- S3 ---

// EnsureBucket creates a private bucket in the session's region, if it
// isn't there.
func (s *Session) EnsureBucket(ctx context.Context, name string) error {
	in := &s3.CreateBucketInput{Bucket: sdk.String(name)}
	if s.region != "us-east-1" {
		in.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{LocationConstraint: s3types.BucketLocationConstraint(s.region)}
	}
	_, err := s.s3().CreateBucket(ctx, in)
	if err != nil && !IsExists(err) {
		return fmt.Errorf("create bucket %s: %w", name, err)
	}
	return nil
}

// Upload writes an object.
func (s *Session) Upload(ctx context.Context, bucket, key string, r io.Reader, size int64) error {
	_, err := s.s3().PutObject(ctx, &s3.PutObjectInput{
		Bucket: sdk.String(bucket), Key: sdk.String(key), Body: r, ContentLength: sdk.Int64(size),
	})
	if err != nil {
		return fmt.Errorf("upload %s: %w", key, err)
	}
	return nil
}

func (s *Session) s3() *s3.Client {
	return s3.NewFromConfig(s.cfg, func(o *s3.Options) {
		// A test's fake serves every bucket on one host.
		o.UsePathStyle = s.c.endpoint != ""
	})
}

// --- ECR ---

// EnsureRepository creates an image repository, if it isn't there, and
// returns its URI.
func (s *Session) EnsureRepository(ctx context.Context, name string) (string, error) {
	cli := ecr.NewFromConfig(s.cfg)
	got, err := cli.DescribeRepositories(ctx, &ecr.DescribeRepositoriesInput{RepositoryNames: []string{name}})
	if err == nil && len(got.Repositories) > 0 {
		return sdk.ToString(got.Repositories[0].RepositoryUri), nil
	}
	if err != nil && !IsNotFound(err) {
		return "", fmt.Errorf("find repository %s: %w", name, err)
	}
	out, err := cli.CreateRepository(ctx, &ecr.CreateRepositoryInput{RepositoryName: sdk.String(name)})
	if err != nil {
		return "", fmt.Errorf("create repository %s: %w", name, err)
	}
	return sdk.ToString(out.Repository.RepositoryUri), nil
}

// --- CodeBuild ---

// EnsureBuildProject creates a CodeBuild project that builds containers
// from a zip in S3, or brings its role up to date.
func (s *Session) EnsureBuildProject(ctx context.Context, name, role, bucket string) error {
	cli := codebuild.NewFromConfig(s.cfg)
	source := &cbtypes.ProjectSource{Type: cbtypes.SourceTypeS3, Location: sdk.String(bucket + "/placeholder.zip")}
	env := &cbtypes.ProjectEnvironment{
		Type: cbtypes.EnvironmentTypeLinuxContainer, ComputeType: cbtypes.ComputeTypeBuildGeneral1Medium,
		Image: sdk.String("aws/codebuild/standard:7.0"), PrivilegedMode: sdk.Bool(true),
	}
	artifacts := &cbtypes.ProjectArtifacts{Type: cbtypes.ArtifactsTypeNoArtifacts}
	_, err := cli.CreateProject(ctx, &codebuild.CreateProjectInput{
		Name: sdk.String(name), ServiceRole: sdk.String(role), Source: source, Environment: env, Artifacts: artifacts,
		TimeoutInMinutes: sdk.Int32(30),
	})
	if IsExists(err) {
		_, err = cli.UpdateProject(ctx, &codebuild.UpdateProjectInput{Name: sdk.String(name), ServiceRole: sdk.String(role)})
	}
	if err != nil {
		return fmt.Errorf("set up the build project: %w", err)
	}
	return nil
}

// StartBuild starts a build of the zip at bucket/key with buildspec, and
// returns its ID. A role IAM hasn't finished creating is retried for a
// while: new roles take seconds to be assumable.
func (s *Session) StartBuild(ctx context.Context, project, bucket, key, buildspec string) (string, error) {
	cli := codebuild.NewFromConfig(s.cfg)
	for attempt := 0; ; attempt++ {
		out, err := cli.StartBuild(ctx, &codebuild.StartBuildInput{
			ProjectName: sdk.String(project), SourceTypeOverride: cbtypes.SourceTypeS3,
			SourceLocationOverride: sdk.String(bucket + "/" + key), BuildspecOverride: sdk.String(buildspec),
		})
		if err == nil {
			return sdk.ToString(out.Build.Id), nil
		}
		if attempt < 12 && strings.Contains(err.Error(), "assume") {
			progress.Report(ctx, "Waiting for the build role to take effect")
			if werr := s.wait(ctx); werr != nil {
				return "", werr
			}
			continue
		}
		return "", fmt.Errorf("start the build: %w", err)
	}
}

// WaitBuild polls a build until it finishes, and fails unless it
// succeeded.
func (s *Session) WaitBuild(ctx context.Context, id string) error {
	cli := codebuild.NewFromConfig(s.cfg)
	for {
		out, err := cli.BatchGetBuilds(ctx, &codebuild.BatchGetBuildsInput{Ids: []string{id}})
		if err != nil {
			return fmt.Errorf("read the build: %w", err)
		}
		if len(out.Builds) == 0 {
			return fmt.Errorf("codebuild has no build %s", id)
		}
		b := out.Builds[0]
		switch b.BuildStatus {
		case cbtypes.StatusTypeSucceeded:
			return nil
		case cbtypes.StatusTypeFailed, cbtypes.StatusTypeFault, cbtypes.StatusTypeStopped, cbtypes.StatusTypeTimedOut:
			link := ""
			if b.Logs != nil {
				link = sdk.ToString(b.Logs.DeepLink)
			}
			return fmt.Errorf("codebuild %s %s in %s. Its log: %s", id, strings.ToLower(string(b.BuildStatus)),
				strings.ToLower(sdk.ToString(b.CurrentPhase)), link)
		}
		progress.Report(ctx, "Building on CodeBuild ("+strings.ToLower(sdk.ToString(b.CurrentPhase))+")")
		if err := s.wait(ctx); err != nil {
			return err
		}
	}
}

// --- CloudWatch Logs ---

// EnsureLogGroup creates a log group, if it isn't there.
func (s *Session) EnsureLogGroup(ctx context.Context, name string) error {
	_, err := cloudwatchlogs.NewFromConfig(s.cfg).CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: sdk.String(name)})
	if err != nil && !IsExists(err) {
		return fmt.Errorf("create log group %s: %w", name, err)
	}
	return nil
}

// --- Secrets Manager ---

// PutSecret stores value as a secret's current version, creating the
// secret if it isn't there, and returns its ARN.
func (s *Session) PutSecret(ctx context.Context, name, value string) (string, error) {
	cli := secretsmanager.NewFromConfig(s.cfg)
	out, err := cli.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name: sdk.String(name), SecretString: sdk.String(value), Description: sdk.String("Set by Loomplane"),
	})
	if err == nil {
		return sdk.ToString(out.ARN), nil
	}
	if !IsExists(err) {
		return "", fmt.Errorf("create secret %s: %w", name, err)
	}
	put, err := cli.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{SecretId: sdk.String(name), SecretString: sdk.String(value)})
	if err != nil {
		return "", fmt.Errorf("update secret %s: %w", name, err)
	}
	return sdk.ToString(put.ARN), nil
}

// errNoLB is a load balancer that hasn't come up.
var errNoLB = errors.New("the load balancer isn't active")
