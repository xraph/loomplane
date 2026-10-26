package aws

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/xraph/loomplane/internal/clouds/progress"
)

// --- Load balancing ---

// LoadBalancer is an Application Load Balancer.
type LoadBalancer struct {
	ARN string
	DNS string
}

// EnsureLoadBalancer creates an internet-facing Application Load Balancer
// across subnets, if there isn't one by that name, and waits until it's
// active.
func (s *Session) EnsureLoadBalancer(ctx context.Context, name string, subnets []string, group string) (*LoadBalancer, error) {
	return s.ensureLoadBalancer(ctx, name, subnets, group, elbtypes.LoadBalancerSchemeEnumInternetFacing)
}

// EnsureInternalLoadBalancer is EnsureLoadBalancer for a load balancer
// with private addresses only: what the other cloud reaches over a link.
func (s *Session) EnsureInternalLoadBalancer(ctx context.Context, name string, subnets []string, group string) (*LoadBalancer, error) {
	return s.ensureLoadBalancer(ctx, name, subnets, group, elbtypes.LoadBalancerSchemeEnumInternal)
}

func (s *Session) ensureLoadBalancer(ctx context.Context, name string, subnets []string, group string, scheme elbtypes.LoadBalancerSchemeEnum) (*LoadBalancer, error) {
	cli := elb.NewFromConfig(s.cfg)
	found, err := cli.DescribeLoadBalancers(ctx, &elb.DescribeLoadBalancersInput{Names: []string{name}})
	var lb elbtypes.LoadBalancer
	switch {
	case err == nil && len(found.LoadBalancers) > 0:
		lb = found.LoadBalancers[0]
	case err == nil || IsNotFound(err):
		out, cerr := cli.CreateLoadBalancer(ctx, &elb.CreateLoadBalancerInput{
			Name: sdk.String(name), Subnets: subnets, SecurityGroups: []string{group},
			Scheme: scheme, Type: elbtypes.LoadBalancerTypeEnumApplication,
		})
		if cerr != nil {
			return nil, fmt.Errorf("create load balancer %s: %w", name, cerr)
		}
		lb = out.LoadBalancers[0]
	default:
		return nil, fmt.Errorf("find load balancer %s: %w", name, err)
	}
	for lb.State == nil || lb.State.Code != elbtypes.LoadBalancerStateEnumActive {
		if lb.State != nil && lb.State.Code == elbtypes.LoadBalancerStateEnumFailed {
			return nil, fmt.Errorf("%w: %s", errNoLB, sdk.ToString(lb.State.Reason))
		}
		progress.Report(ctx, "Waiting for the load balancer")
		if err := s.wait(ctx); err != nil {
			return nil, err
		}
		got, err := cli.DescribeLoadBalancers(ctx, &elb.DescribeLoadBalancersInput{LoadBalancerArns: []string{sdk.ToString(lb.LoadBalancerArn)}})
		if err != nil {
			return nil, fmt.Errorf("read load balancer %s: %w", name, err)
		}
		lb = got.LoadBalancers[0]
	}
	return &LoadBalancer{ARN: sdk.ToString(lb.LoadBalancerArn), DNS: sdk.ToString(lb.DNSName)}, nil
}

// EnsureTargetGroup creates a target group for tasks listening on port,
// if there isn't one by that name, and returns its ARN. Anything below a
// server error counts as healthy: an app that answers / with a 404 is
// still up.
func (s *Session) EnsureTargetGroup(ctx context.Context, name, vpc string, port int32) (string, error) {
	cli := elb.NewFromConfig(s.cfg)
	found, err := cli.DescribeTargetGroups(ctx, &elb.DescribeTargetGroupsInput{Names: []string{name}})
	if err == nil && len(found.TargetGroups) > 0 {
		return sdk.ToString(found.TargetGroups[0].TargetGroupArn), nil
	}
	if err != nil && !IsNotFound(err) {
		return "", fmt.Errorf("find target group %s: %w", name, err)
	}
	out, err := cli.CreateTargetGroup(ctx, &elb.CreateTargetGroupInput{
		Name: sdk.String(name), Protocol: elbtypes.ProtocolEnumHttp, Port: sdk.Int32(port), VpcId: sdk.String(vpc),
		TargetType: elbtypes.TargetTypeEnumIp, HealthCheckPath: sdk.String("/"),
		Matcher: &elbtypes.Matcher{HttpCode: sdk.String("200-499")},
	})
	if err != nil {
		return "", fmt.Errorf("create target group %s: %w", name, err)
	}
	return sdk.ToString(out.TargetGroups[0].TargetGroupArn), nil
}

// Listener is a port on a load balancer forwarding to a target group.
type Listener struct {
	ARN   string
	Port  int32
	HTTPS bool
}

// EnsureListener finds the listener forwarding to a target group, or
// opens one on the first free port: 443 with a certificate, or 80, then
// 8000 and up. Each public service gets a port of its own, since without
// a domain there's no host to route on. A listener a canary splits
// between the target group and its candidate's still forwards to it,
// whatever its weight.
func (s *Session) EnsureListener(ctx context.Context, lb, targetGroup, certificate string) (*Listener, error) {
	return ensureListener(ctx, elb.NewFromConfig(s.cfg), lb, targetGroup, certificate)
}

func ensureListener(ctx context.Context, cli listenerAPI, lb, targetGroup, certificate string) (*Listener, error) {
	got, err := cli.DescribeListeners(ctx, &elb.DescribeListenersInput{LoadBalancerArn: sdk.String(lb)})
	if err != nil {
		return nil, fmt.Errorf("read listeners: %w", err)
	}
	used := map[int32]bool{}
	for _, l := range got.Listeners {
		used[sdk.ToInt32(l.Port)] = true
		if forwardsTo(l, targetGroup) {
			return &Listener{ARN: sdk.ToString(l.ListenerArn), Port: sdk.ToInt32(l.Port), HTTPS: l.Protocol == elbtypes.ProtocolEnumHttps}, nil
		}
	}
	first := int32(80)
	if certificate != "" {
		first = 443
	}
	port := first
	for p := int32(8000); used[port]; p++ {
		port = p
	}
	in := &elb.CreateListenerInput{
		LoadBalancerArn: sdk.String(lb), Port: sdk.Int32(port), Protocol: elbtypes.ProtocolEnumHttp,
		DefaultActions: []elbtypes.Action{{Type: elbtypes.ActionTypeEnumForward, TargetGroupArn: sdk.String(targetGroup)}},
	}
	if certificate != "" {
		in.Protocol = elbtypes.ProtocolEnumHttps
		in.Certificates = []elbtypes.Certificate{{CertificateArn: sdk.String(certificate)}}
	}
	out, err := cli.CreateListener(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("open port %d: %w", port, err)
	}
	return &Listener{ARN: sdk.ToString(out.Listeners[0].ListenerArn), Port: port, HTTPS: certificate != ""}, nil
}

// RemoveListener deletes a listener and its target group. Ones already
// gone are not an error.
func (s *Session) RemoveListener(ctx context.Context, listener, targetGroup string) error {
	cli := elb.NewFromConfig(s.cfg)
	if listener != "" {
		if _, err := cli.DeleteListener(ctx, &elb.DeleteListenerInput{ListenerArn: sdk.String(listener)}); err != nil && !IsNotFound(err) {
			return fmt.Errorf("delete listener: %w", err)
		}
	}
	if targetGroup != "" {
		if _, err := cli.DeleteTargetGroup(ctx, &elb.DeleteTargetGroupInput{TargetGroupArn: sdk.String(targetGroup)}); err != nil && !IsNotFound(err) {
			return fmt.Errorf("delete target group: %w", err)
		}
	}
	return nil
}

// --- ECS ---

// EnsureCluster creates a cluster, if it isn't there.
func (s *Session) EnsureCluster(ctx context.Context, name string) error {
	if _, err := ecs.NewFromConfig(s.cfg).CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: sdk.String(name)}); err != nil {
		return fmt.Errorf("create cluster %s: %w", name, err)
	}
	return nil
}

// Task is what a service runs: one container on Fargate.
type Task struct {
	Family        string
	Image         string
	Port          int32
	CPU           int
	MemoryMB      int
	Env           map[string]string
	Secrets       map[string]string // variable to Secrets Manager ARN
	ExecutionRole string
	// TaskRole is the role the application runs as. Empty means none.
	TaskRole string
	LogGroup string
}

// RegisterTask registers a task definition revision and returns its ARN.
func (s *Session) RegisterTask(ctx context.Context, t Task) (string, error) {
	c := ecstypes.ContainerDefinition{
		Name: sdk.String("main"), Image: sdk.String(t.Image), Essential: sdk.Bool(true),
		LogConfiguration: &ecstypes.LogConfiguration{LogDriver: ecstypes.LogDriverAwslogs, Options: map[string]string{
			"awslogs-group": t.LogGroup, "awslogs-region": s.region, "awslogs-stream-prefix": "loomplane",
		}},
	}
	if t.Port > 0 {
		// Service Connect finds the port by its name.
		c.PortMappings = []ecstypes.PortMapping{{
			Name: sdk.String(portName), ContainerPort: sdk.Int32(t.Port), Protocol: ecstypes.TransportProtocolTcp,
			AppProtocol: ecstypes.ApplicationProtocolHttp,
		}}
	}
	for _, k := range slices.Sorted(maps.Keys(t.Env)) {
		c.Environment = append(c.Environment, ecstypes.KeyValuePair{Name: sdk.String(k), Value: sdk.String(t.Env[k])})
	}
	for _, k := range slices.Sorted(maps.Keys(t.Secrets)) {
		c.Secrets = append(c.Secrets, ecstypes.Secret{Name: sdk.String(k), ValueFrom: sdk.String(t.Secrets[k])})
	}
	in := &ecs.RegisterTaskDefinitionInput{
		Family: sdk.String(t.Family), RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode: ecstypes.NetworkModeAwsvpc, Cpu: sdk.String(strconv.Itoa(t.CPU)), Memory: sdk.String(strconv.Itoa(t.MemoryMB)),
		ExecutionRoleArn: sdk.String(t.ExecutionRole), ContainerDefinitions: []ecstypes.ContainerDefinition{c},
	}
	if t.TaskRole != "" {
		in.TaskRoleArn = sdk.String(t.TaskRole)
	}
	out, err := ecs.NewFromConfig(s.cfg).RegisterTaskDefinition(ctx, in)
	if err != nil {
		return "", fmt.Errorf("register the task: %w", err)
	}
	return sdk.ToString(out.TaskDefinition.TaskDefinitionArn), nil
}

// portName names a task's port, for Service Connect.
const portName = "http"

// Service is what an ECS service runs and where.
type Service struct {
	Cluster     string
	Name        string
	Task        string
	Desired     int32
	Subnets     []string
	Group       string
	TargetGroup string
	Port        int32
	// Namespace is the Cloud Map namespace the service joins through
	// Service Connect, so it can reach the environment's other services by
	// name. Alias is the name it's reached by, when it has a port.
	Namespace string
	Alias     string
}

// serviceConnect is the Service Connect configuration for sv, or nil.
func (sv Service) serviceConnect() *ecstypes.ServiceConnectConfiguration {
	if sv.Namespace == "" {
		return nil
	}
	sc := &ecstypes.ServiceConnectConfiguration{Enabled: true, Namespace: sdk.String(sv.Namespace)}
	if sv.Alias != "" && sv.Port > 0 {
		sc.Services = []ecstypes.ServiceConnectService{{
			PortName: sdk.String(portName), DiscoveryName: sdk.String(sv.Alias),
			ClientAliases: []ecstypes.ServiceConnectClientAlias{{Port: sdk.Int32(sv.Port), DnsName: sdk.String(sv.Alias)}},
		}}
	}
	return sc
}

// ApplyService creates the service, or rolls the existing one to a new
// task, and waits until the rollout is done.
func (s *Session) ApplyService(ctx context.Context, sv Service) error {
	cli := ecs.NewFromConfig(s.cfg)
	got, err := cli.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: sdk.String(sv.Cluster), Services: []string{sv.Name}})
	if err != nil {
		return fmt.Errorf("read service %s: %w", sv.Name, err)
	}
	active := len(got.Services) > 0 && sdk.ToString(got.Services[0].Status) == "ACTIVE"
	if active {
		_, err = cli.UpdateService(ctx, &ecs.UpdateServiceInput{
			Cluster: sdk.String(sv.Cluster), Service: sdk.String(sv.Name), TaskDefinition: sdk.String(sv.Task),
			DesiredCount: sdk.Int32(sv.Desired), ServiceConnectConfiguration: sv.serviceConnect(),
		})
	} else {
		in := &ecs.CreateServiceInput{
			Cluster: sdk.String(sv.Cluster), ServiceName: sdk.String(sv.Name), TaskDefinition: sdk.String(sv.Task),
			DesiredCount: sdk.Int32(sv.Desired), LaunchType: ecstypes.LaunchTypeFargate,
			NetworkConfiguration: &ecstypes.NetworkConfiguration{AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets: sv.Subnets, SecurityGroups: []string{sv.Group}, AssignPublicIp: ecstypes.AssignPublicIpEnabled,
			}},
			ServiceConnectConfiguration: sv.serviceConnect(),
		}
		if sv.TargetGroup != "" {
			in.LoadBalancers = []ecstypes.LoadBalancer{{
				TargetGroupArn: sdk.String(sv.TargetGroup), ContainerName: sdk.String("main"), ContainerPort: sdk.Int32(sv.Port),
			}}
		}
		_, err = cli.CreateService(ctx, in)
	}
	if err != nil {
		return fmt.Errorf("deploy service %s: %w", sv.Name, err)
	}
	return s.waitStable(ctx, sv.Cluster, sv.Name)
}

// waitStable polls until the service's newest deployment has finished
// rolling out, and fails if ECS gave up on it.
func (s *Session) waitStable(ctx context.Context, cluster, name string) error {
	cli := ecs.NewFromConfig(s.cfg)
	for {
		got, err := cli.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: sdk.String(cluster), Services: []string{name}})
		if err != nil {
			return fmt.Errorf("read service %s: %w", name, err)
		}
		if len(got.Services) == 0 {
			return fmt.Errorf("ecs has no service %s", name)
		}
		svc := got.Services[0]
		for _, d := range svc.Deployments {
			if sdk.ToString(d.Status) != "PRIMARY" {
				continue
			}
			switch d.RolloutState {
			case ecstypes.DeploymentRolloutStateCompleted:
				if len(svc.Deployments) == 1 {
					return nil
				}
			case ecstypes.DeploymentRolloutStateFailed:
				return fmt.Errorf("ecs couldn't roll out %s: %s", name, sdk.ToString(d.RolloutStateReason))
			}
			progress.Report(ctx, fmt.Sprintf("Rolling out: %d of %d tasks running", d.RunningCount, d.DesiredCount))
		}
		if err := s.wait(ctx); err != nil {
			return err
		}
	}
}

// ServiceImage is the image a service runs now, or "" if the service is
// gone.
func (s *Session) ServiceImage(ctx context.Context, cluster, name string) (string, error) {
	cli := ecs.NewFromConfig(s.cfg)
	got, err := cli.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: sdk.String(cluster), Services: []string{name}})
	if err != nil {
		return "", fmt.Errorf("read service %s: %w", name, err)
	}
	if len(got.Services) == 0 || sdk.ToString(got.Services[0].Status) != "ACTIVE" {
		return "", nil
	}
	td, err := cli.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{TaskDefinition: got.Services[0].TaskDefinition})
	if err != nil {
		return "", fmt.Errorf("read the task: %w", err)
	}
	for _, c := range td.TaskDefinition.ContainerDefinitions {
		if sdk.ToString(c.Name) == "main" {
			return sdk.ToString(c.Image), nil
		}
	}
	return "", nil
}

// DeleteService scales a service to nothing and deletes it. One that's
// already gone is not an error.
func (s *Session) DeleteService(ctx context.Context, cluster, name string) error {
	_, err := ecs.NewFromConfig(s.cfg).DeleteService(ctx, &ecs.DeleteServiceInput{
		Cluster: sdk.String(cluster), Service: sdk.String(name), Force: sdk.Bool(true),
	})
	if err != nil && !IsNotFound(err) && !strings.Contains(err.Error(), "ServiceNotActive") {
		return fmt.Errorf("delete service %s: %w", name, err)
	}
	return nil
}
