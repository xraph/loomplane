package connections

import (
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/aws/awstest"
)

// A key without what only a canary needs is complete: every deploy still
// works through it, and the connection says what a canary would lack.
func TestAWSCheckKeepsCanaryOnlyActionsOutOfMissing(t *testing.T) {
	fake := awstest.NewIAM(t, "elasticloadbalancing:ModifyListener", "elasticloadbalancing:DescribeTargetHealth")
	checker := AWSChecker{Client: aws.New(fake.URL)}
	c, err := checker.Check(t.Context(), []byte(awstest.Key), map[string]string{"region": "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Missing) != 0 {
		t.Fatalf("missing %v", c.Missing)
	}
	if got := c.Details[DetailCanaryMissing]; got != "elasticloadbalancing:DescribeTargetHealth,elasticloadbalancing:ModifyListener" {
		t.Fatalf("canary missing %q", got)
	}
	asked := fake.Asked()
	if len(asked) != 1 || !slices.Contains(asked[0], "elasticloadbalancing:ModifyListener") || !slices.Contains(asked[0], "ecs:CreateService") {
		t.Fatalf("asked %v", asked)
	}
	conn := &Connection{}
	apply(conn, c, conn.UpdatedAt)
	if conn.Status != StatusReady {
		t.Fatalf("status %s", conn.Status)
	}
}

func TestAWSCheckStillRequiresWhatEveryDeployNeeds(t *testing.T) {
	fake := awstest.NewIAM(t, "ecs:CreateService", "elasticloadbalancing:DeleteTargetGroup")
	c, err := AWSChecker{Client: aws.New(fake.URL)}.Check(t.Context(), []byte(awstest.Key), map[string]string{"region": "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Missing, []string{"ecs:CreateService"}) || c.Details[DetailCanaryMissing] != "elasticloadbalancing:DeleteTargetGroup" {
		t.Fatalf("missing %v, details %v", c.Missing, c.Details)
	}
	conn := &Connection{}
	apply(conn, c, conn.UpdatedAt)
	if conn.Status != StatusIncomplete {
		t.Fatalf("status %s", conn.Status)
	}
}

func TestAWSCheckWithEverythingRecordsNoCanaryGap(t *testing.T) {
	fake := awstest.NewIAM(t)
	c, err := AWSChecker{Client: aws.New(fake.URL)}.Check(t.Context(), []byte(awstest.Key), map[string]string{"region": "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Details[DetailCanaryMissing]; ok || len(c.Missing) != 0 {
		t.Fatalf("missing %v, details %v", c.Missing, c.Details)
	}
}
