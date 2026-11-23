package aws

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// RunnerSpec is an on-demand runner's instance.
type RunnerSpec struct {
	// ID is the machine's ID, the launch's client token, so a retry finds
	// the instance an earlier attempt started.
	ID, Name, InstanceType, ImageID, UserData string
	// PoolTag is the pool every instance of it is tagged with.
	PoolTag string
	Spot    bool
}

var (
	amiID        = regexp.MustCompile(`^ami-[0-9a-f]{8,17}$`)
	instanceType = regexp.MustCompile(`^[a-z][a-z0-9-]*\.[a-z0-9]+$`)
)

// runnerGroup is the security group runners launch in. It allows nothing
// in: a runner dials out to Loomplane.
const runnerGroup = "loomplane-runner"

// poolTagKey is the instance tag naming the pool.
const poolTagKey = "loomplane-pool"

// StartRunner launches an on-demand runner's instance with nested
// virtualization on, so it can run Firecracker microVMs. It has no instance
// profile and no key pair, requires IMDSv2 with a hop limit of one, and sits
// in a security group that allows nothing in. Its client token is the
// machine's ID, so a retry returns the instance an earlier attempt started.
func (s *Session) StartRunner(ctx context.Context, spec RunnerSpec) (string, error) {
	if !amiID.MatchString(spec.ImageID) || !instanceType.MatchString(spec.InstanceType) || spec.ID == "" || len(spec.ID) > 64 {
		return "", errors.New("an EC2 runner needs an AMI ID and an instance type")
	}
	net, err := s.DefaultNetwork(ctx)
	if err != nil {
		return "", err
	}
	group, err := s.EnsureSecurityGroup(ctx, net.VPC, runnerGroup, "Loomplane runners: nothing in; they dial out")
	if err != nil {
		return "", err
	}
	in := &ec2.RunInstancesInput{
		ClientToken: sdk.String(spec.ID), ImageId: sdk.String(spec.ImageID), InstanceType: ec2types.InstanceType(spec.InstanceType),
		MinCount: sdk.Int32(1), MaxCount: sdk.Int32(1), SecurityGroupIds: []string{group},
		UserData:   sdk.String(base64.StdEncoding.EncodeToString([]byte(spec.UserData))),
		CpuOptions: &ec2types.CpuOptionsRequest{NestedVirtualization: ec2types.NestedVirtualizationSpecificationEnabled},
		MetadataOptions: &ec2types.InstanceMetadataOptionsRequest{HttpTokens: ec2types.HttpTokensStateRequired, HttpPutResponseHopLimit: sdk.Int32(1),
			HttpEndpoint: ec2types.InstanceMetadataEndpointStateEnabled},
		InstanceInitiatedShutdownBehavior: ec2types.ShutdownBehaviorTerminate,
		TagSpecifications: []ec2types.TagSpecification{{ResourceType: ec2types.ResourceTypeInstance, Tags: []ec2types.Tag{
			{Key: sdk.String("Name"), Value: sdk.String(spec.Name)}, {Key: sdk.String(poolTagKey), Value: sdk.String(spec.PoolTag)},
			{Key: sdk.String("loomplane-runner"), Value: sdk.String(spec.ID)},
		}}},
	}
	if spec.Spot {
		in.InstanceMarketOptions = &ec2types.InstanceMarketOptionsRequest{MarketType: ec2types.MarketTypeSpot}
	}
	out, err := ec2.NewFromConfig(s.cfg).RunInstances(ctx, in)
	if err != nil {
		return "", fmt.Errorf("launch the runner: %w", err)
	}
	if len(out.Instances) != 1 || sdk.ToString(out.Instances[0].InstanceId) == "" {
		return "", errors.New("EC2 launched no runner instance")
	}
	return sdk.ToString(out.Instances[0].InstanceId), nil
}

// StopRunner terminates a runner's instance. One already gone is fine.
func (s *Session) StopRunner(ctx context.Context, id string) error {
	_, err := ec2.NewFromConfig(s.cfg).TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{id}})
	if err != nil && !IsNotFound(err) {
		return fmt.Errorf("terminate the runner %s: %w", id, err)
	}
	return nil
}

// Runners lists the live instances of the pool poolTag names, by ID and
// name.
func (s *Session) Runners(ctx context.Context, poolTag string) (map[string]string, error) {
	out := map[string]string{}
	p := ec2.NewDescribeInstancesPaginator(ec2.NewFromConfig(s.cfg), &ec2.DescribeInstancesInput{Filters: []ec2types.Filter{
		{Name: sdk.String("tag:" + poolTagKey), Values: []string{poolTag}},
		{Name: sdk.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
	}})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list the pool's runners: %w", err)
		}
		for _, r := range page.Reservations {
			for _, i := range r.Instances {
				name := ""
				for _, t := range i.Tags {
					if sdk.ToString(t.Key) == "Name" {
						name = sdk.ToString(t.Value)
					}
				}
				out[sdk.ToString(i.InstanceId)] = name
			}
		}
	}
	return out, nil
}
