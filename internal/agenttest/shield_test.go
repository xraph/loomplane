package agenttest_test

import (
	"context"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agenttest"
	"github.com/xraph/loomplane/internal/security"
)

type tenants struct{}

func (tenants) NexusTenant(_ context.Context, orgID string) (string, error) {
	return "tenant_" + orgID, nil
}

// The seeded engine scans the workspace's outbound text: a plain line
// goes as it is, an AWS key doesn't.
func TestShieldScansOutboundText(t *testing.T) {
	s := &agentsafety.Scanner{Engine: agenttest.Shield(t, "org_a")}
	b := &agentscope.Binder{Tenants: tenants{}}
	ctx, err := b.Bind(context.Background(), security.AgentPrincipal("org_a", nil), agentscope.Scope{OrgID: "org_a"})
	if err != nil {
		t.Fatal(err)
	}
	if got, oerr := s.Outbound(ctx, "The deploy finished."); oerr != nil || got != "The deploy finished." {
		t.Fatalf("plain text: %q, %v", got, oerr)
	}
	const key = "AKIAIOSFODNN7EXAMPLE"
	got, err := s.Outbound(ctx, "The key is "+key)
	if err == nil && strings.Contains(got, key) {
		t.Fatalf("the key went out: %q", got)
	}
}
