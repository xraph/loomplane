package agentsettings_test

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/nexus/pipeline"
	"github.com/xraph/nexus/provider"

	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/metering"
)

type gateSpy struct {
	err   error
	kinds []metering.Kind
}

func (g *gateSpy) CanRun(_ context.Context, _ string, k metering.Kind, _ bool) error {
	g.kinds = append(g.kinds, k)
	return g.err
}

type meterSpy struct{ events []metering.Event }

func (m *meterSpy) Record(_ context.Context, e metering.Event) error {
	m.events = append(m.events, e)
	return nil
}

func callCtx(ctx context.Context, request string) context.Context {
	return pipeline.WithRequestID(ctx, request)
}

func TestPlatformKeyCallIsGatedAndClaimed(t *testing.T) {
	h, ctx := on(t, "", "")
	g, m := &gateSpy{}, &meterSpy{}
	h.svc.WithMetering(g, m)
	_, err := h.svc.Credentials().ResolveCredential(callCtx(ctx, "req_1"), "anthropic")
	if !errors.Is(err, provider.ErrCredentialNotFound) {
		t.Fatalf("platform-key call: %v, want ErrCredentialNotFound (the server's key)", err)
	}
	if len(g.kinds) != 1 || g.kinds[0] != metering.KindAIPlatform {
		t.Fatalf("gate kinds %v", g.kinds)
	}
	if len(m.events) != 1 || m.events[0].ID != "ai-request:req_1" || m.events[0].Feature != agentsettings.FeatureAIPlatformRequests ||
		m.events[0].OrgID != org || m.events[0].ResourceRef != "tenant_o:req_1" || !m.events[0].PlatformOwned ||
		m.events[0].Quantity != 1 || m.events[0].OccurredAt.IsZero() {
		t.Fatalf("claim %+v", m.events)
	}
}

func TestPlatformKeyCallRefusedByTheGateIsNotClaimed(t *testing.T) {
	h, ctx := on(t, "", "")
	m := &meterSpy{}
	h.svc.WithMetering(&gateSpy{err: &metering.Blocked{Reason: "Turn on the AI add-on in Settings > Billing, or add your own model key."}}, m)
	_, err := h.svc.Credentials().ResolveCredential(callCtx(ctx, "req_1"), "anthropic")
	if !errors.Is(err, metering.ErrBlocked) || errors.Is(err, provider.ErrCredentialNotFound) {
		t.Fatalf("blocked call: %v", err)
	}
	if len(m.events) != 0 {
		t.Fatal("a refused call was claimed")
	}
}

func TestWorkspaceKeyCallNeverTouchesBilling(t *testing.T) {
	h, ctx := on(t, "claude", "")
	g, m := &gateSpy{err: &metering.Blocked{Reason: "no"}}, &meterSpy{}
	h.svc.WithMetering(g, m)
	cred, err := h.svc.Credentials().ResolveCredential(callCtx(ctx, "req_1"), "anthropic")
	if err != nil || cred.APIKey == "" {
		t.Fatalf("workspace-key call: %+v %v", cred, err)
	}
	if len(g.kinds) != 0 || len(m.events) != 0 {
		t.Fatalf("workspace key reached billing: gate %v events %v", g.kinds, m.events)
	}
	if err := h.svc.RequireBudget(context.Background(), org); errors.Is(err, metering.ErrBlocked) {
		t.Fatal("RequireBudget gated a workspace on its own key")
	}
	if len(g.kinds) != 0 {
		t.Fatalf("RequireBudget consulted the gate: %v", g.kinds)
	}
}

func TestRequireBudgetGatesTheServerKey(t *testing.T) {
	h, _ := on(t, "", "")
	h.svc.WithMetering(&gateSpy{err: &metering.Blocked{Reason: "Turn on the AI add-on"}}, &meterSpy{})
	if err := h.svc.RequireBudget(context.Background(), org); !errors.Is(err, metering.ErrBlocked) {
		t.Fatalf("RequireBudget: %v", err)
	}
}

func TestNoRequestIDRefusesTheServerKey(t *testing.T) {
	h, ctx := on(t, "", "")
	h.svc.WithMetering(&gateSpy{}, &meterSpy{})
	ctx = pipeline.WithRequestID(ctx, "")
	refused(t, mustErr(h.svc.Credentials().ResolveCredential(ctx, "anthropic")))
}

func mustErr(_ provider.Credential, err error) error { return err }

func TestEmbeddingsAreNeverGatedOrClaimed(t *testing.T) {
	h, ctx := on(t, "", "")
	g, m := &gateSpy{err: &metering.Blocked{Reason: "no"}}, &meterSpy{}
	h.svc.WithMetering(g, m)
	_, err := h.svc.Credentials().ResolveCredential(callCtx(ctx, "req_1"), agentsettings.EmbeddingsProvider)
	if !errors.Is(err, provider.ErrCredentialNotFound) || len(g.kinds) != 0 || len(m.events) != 0 {
		t.Fatalf("embeddings: %v gate %v events %v", err, g.kinds, m.events)
	}
}
