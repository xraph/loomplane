package aws

import (
	"context"
	"maps"
	"slices"
	"testing"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/xraph/loomplane/internal/clouds/aws/awstest"
)

// fakeServiceTask answers DescribeServices with one service, and
// DescribeTaskDefinition with its task.
type fakeServiceTask struct {
	service *ecstypes.Service
	task    *ecstypes.TaskDefinition
	read    int
}

func (f *fakeServiceTask) DescribeServices(_ context.Context, _ *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	if f.service == nil {
		return &ecs.DescribeServicesOutput{Failures: []ecstypes.Failure{{Reason: sdk.String("MISSING")}}}, nil
	}
	return &ecs.DescribeServicesOutput{Services: []ecstypes.Service{*f.service}}, nil
}

func (f *fakeServiceTask) DescribeTaskDefinition(_ context.Context, _ *ecs.DescribeTaskDefinitionInput, _ ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error) {
	f.read++
	return &ecs.DescribeTaskDefinitionOutput{TaskDefinition: f.task}, nil
}

func TestServiceTaskReadsTheMainContainer(t *testing.T) {
	f := &fakeServiceTask{
		service: &ecstypes.Service{Status: sdk.String("ACTIVE"), DesiredCount: 2, TaskDefinition: sdk.String("arn:task:7")},
		task: &ecstypes.TaskDefinition{ContainerDefinitions: []ecstypes.ContainerDefinition{
			{Name: sdk.String("sidecar"), Image: sdk.String("envoy")},
			{
				Name: sdk.String("main"), Image: sdk.String("repo:abc"),
				Environment: []ecstypes.KeyValuePair{{Name: sdk.String("MODE"), Value: sdk.String("new")}},
				Secrets:     []ecstypes.Secret{{Name: sdk.String("TOKEN"), ValueFrom: sdk.String("arn:secret:token")}},
			},
		}},
	}
	got, err := serviceTask(t.Context(), f, "loomplane", "api")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Serving() || got.Desired != 2 || got.Image != "repo:abc" ||
		!maps.Equal(got.Env, map[string]string{"MODE": "new"}) || !maps.Equal(got.Secrets, map[string]string{"TOKEN": "arn:secret:token"}) {
		t.Fatalf("task %+v", got)
	}
}

func TestServiceTaskScaledToNothingIsntServing(t *testing.T) {
	f := &fakeServiceTask{service: &ecstypes.Service{Status: sdk.String("ACTIVE"), DesiredCount: 0, TaskDefinition: sdk.String("arn:task:7")},
		task: &ecstypes.TaskDefinition{}}
	got, err := serviceTask(t.Context(), f, "loomplane", "api-canary")
	if err != nil || got == nil || got.Serving() {
		t.Fatalf("task %+v, err %v", got, err)
	}
	draining := &fakeServiceTask{service: &ecstypes.Service{Status: sdk.String("DRAINING"), DesiredCount: 1}}
	if got, err = serviceTask(t.Context(), draining, "loomplane", "api-canary"); err != nil || got.Serving() || draining.read != 0 {
		t.Fatalf("draining %+v, err %v, read %d", got, err, draining.read)
	}
	missing, err := serviceTask(t.Context(), &fakeServiceTask{}, "loomplane", "api-canary")
	if err != nil || missing != nil || missing.Serving() {
		t.Fatalf("missing %+v, err %v", missing, err)
	}
}

func TestMissingOfAsksForWhatItsGiven(t *testing.T) {
	fake := awstest.NewIAM(t, "elasticloadbalancing:ModifyListener")
	key, err := ParseKey([]byte(awstest.Key))
	if err != nil {
		t.Fatal(err)
	}
	c := New(fake.URL)
	missing, checked, err := c.MissingOf(t.Context(), key, "us-east-1", awstest.ARN, CanaryActions)
	if err != nil || !checked || !slices.Equal(missing, []string{"elasticloadbalancing:ModifyListener"}) {
		t.Fatalf("missing %v, checked %v, err %v", missing, checked, err)
	}
	if asked := fake.Asked(); len(asked) != 1 || !slices.Equal(asked[0], CanaryActions) {
		t.Fatalf("asked %v", asked)
	}
	// What every deploy needs leaves canary actions out.
	if missing, _, err = c.Missing(t.Context(), key, "us-east-1", awstest.ARN); err != nil || len(missing) != 0 {
		t.Fatalf("missing %v, err %v", missing, err)
	}
	for _, a := range CanaryActions {
		if slices.Contains(Actions, a) {
			t.Fatalf("%s is required of every key", a)
		}
	}
	fake.Refuse()
	if _, checked, err = c.MissingOf(t.Context(), key, "us-east-1", awstest.ARN, CanaryActions); err != nil || checked {
		t.Fatalf("refused: checked %v, err %v", checked, err)
	}
}

func TestPrincipalARNNamesAnAssumedRolesRole(t *testing.T) {
	if got := PrincipalARN("arn:aws:sts::123456789012:assumed-role/deployer/session-1"); got != "arn:aws:iam::123456789012:role/deployer" {
		t.Fatalf("assumed role %q", got)
	}
	if got := PrincipalARN(awstest.ARN); got != awstest.ARN {
		t.Fatalf("user %q", got)
	}
}
