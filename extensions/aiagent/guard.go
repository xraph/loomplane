package aiagent

import (
	"context"
	"slices"
	"sync"

	"github.com/xraph/nexus"
	"github.com/xraph/nexus/guard"
	"github.com/xraph/shield"
	"github.com/xraph/shield/awareness/perception"
	shieldengine "github.com/xraph/shield/engine"
	shieldid "github.com/xraph/shield/id"
	"github.com/xraph/shield/integrations/nexusguard"
	"github.com/xraph/shield/presets"
	"github.com/xraph/shield/scan"

	"github.com/xraph/loomplane/internal/agentscope"
)

// The reasons the output guard gives when it stops a reply. Nexus puts
// them in its *guard.BlockedError; none quotes the reply.
const (
	reasonNoShield = "Loomplane's safety checks aren't running, so the answer was stopped."
	reasonSecret   = "The answer held a credential, so it was stopped."
	reasonUnread   = "Loomplane couldn't read the safety check on the answer, so it was stopped."
)

// guardOptions make nexus stream each reply as the provider sends it and
// check the whole reply when it ends, with shield's output guard: chunks
// reach cortex (whose checked-segments gate decides what people see) at
// once, and a reply the guard stops ends with *guard.BlockedError instead
// of io.EOF, so cortex runs none of its tool calls.
func guardOptions(eng func() *shieldengine.Engine) []nexus.Option {
	return []nexus.Option{
		nexus.WithStreamGuardStrategy(guard.StrategyPassthrough),
		nexus.WithGuard(outputGuard(eng)),
	}
}

// outputGuard returns shield's nexus output guard, resolving shield's engine on each check
// (shield's extension may build its engine after nexus's options are made). A nil engine blocks.
//
// It scans each reply with nexusguard.Output under the InfraAgentOutput
// profile and agentscope.App, and maps shield's decision for a net that
// can't change text already delivered:
//   - block stays a block;
//   - a redaction stops the reply when any of its findings is a
//     credential (one of shield's secret detectors), since its tool calls
//     may carry it;
//   - any other redaction, such as an email address, and a flag let the
//     reply through: cortex's segment gate publishes that text redacted.
//
// Nexus would end a passthrough stream with a rewrite it can't apply as a
// clean io.EOF, so no verdict this guard returns asks for one.
func outputGuard(eng func() *shieldengine.Engine) guard.Guard {
	return &shieldOutput{shield: eng}
}

type shieldOutput struct {
	shield func() *shieldengine.Engine

	mu    sync.Mutex
	eng   *shieldengine.Engine
	inner guard.Guard
}

var _ guard.Guard = (*shieldOutput)(nil)

func (*shieldOutput) Name() string { return "shield" }

func (*shieldOutput) Phase() guard.Phase { return guard.PhaseOutput }

// Check implements guard.Guard.
func (g *shieldOutput) Check(ctx context.Context, in *guard.CheckInput) (*guard.CheckResult, error) {
	var eng *shieldengine.Engine
	if g.shield != nil {
		eng = g.shield()
	}
	if eng == nil {
		return stopped(reasonNoShield), nil
	}
	r, err := g.guardFor(eng).Check(ctx, in)
	if err != nil {
		return nil, err
	}
	switch {
	case r == nil:
		return allowed(), nil
	case r.Blocked:
		return r, nil
	case r.Action != guard.ActionRedact:
		// A flag: the reply goes on, unchanged.
		return r, nil
	}
	secret, err := holdsSecret(ctx, eng, in, r)
	switch {
	case err != nil:
		return stopped(reasonUnread), nil
	case secret:
		return stopped(reasonSecret), nil
	}
	return allowed(), nil
}

// guardFor is nexusguard's output guard over eng, built once per engine:
// it reads the engine's profile cache TTL when it's built, and keeps its
// verdict cache across checks.
func (g *shieldOutput) guardFor(eng *shieldengine.Engine) guard.Guard {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.eng != eng {
		g.eng = eng
		g.inner = nexusguard.Output(eng, nexusguard.WithProfile(presets.InfraAgentOutput), nexusguard.WithApp(agentscope.App))
	}
	return g.inner
}

// holdsSecret reports whether a redacting verdict's scans found a
// credential, reading each stored scan under the tenant nexusguard
// scanned it as: the shield tenant on ctx, else the call's nexus tenant.
func holdsSecret(ctx context.Context, eng *shieldengine.Engine, in *guard.CheckInput, r *guard.CheckResult) (bool, error) {
	ids, ok := r.Details["scan_ids"].([]string)
	if !ok || len(ids) == 0 || eng.Store() == nil {
		return false, shield.ErrScanNotFound
	}
	tenant := shield.TenantFromContext(ctx)
	if tenant == "" && in != nil {
		tenant = in.TenantID
	}
	ctx = shield.WithApp(shield.WithTenant(ctx, tenant), agentscope.App)
	secrets := perception.BuiltinSecrets()
	for _, raw := range ids {
		sid, err := shieldid.ParseScanID(raw)
		if err != nil {
			return false, err
		}
		res, err := eng.Store().GetScan(ctx, sid)
		if err != nil {
			return false, err
		}
		if secretFound(res, secrets) {
			return true, nil
		}
	}
	return false, nil
}

// secretFound reports whether a scan found a credential: the engine
// counted one, or an awareness finding names a secret detector.
func secretFound(res *scan.Result, secrets []string) bool {
	switch n := res.Metadata["secret_count"].(type) {
	case int:
		if n > 0 {
			return true
		}
	case int64:
		if n > 0 {
			return true
		}
	case float64:
		if n > 0 {
			return true
		}
	}
	for _, f := range res.Findings {
		if f == nil || f.Layer != "awareness" {
			continue
		}
		if d, ok := f.Details["detector"].(string); ok && slices.Contains(secrets, d) {
			return true
		}
	}
	return false
}

func stopped(reason string) *guard.CheckResult {
	return &guard.CheckResult{Blocked: true, Action: guard.ActionBlock, Reason: reason, Guard: "shield"}
}

func allowed() *guard.CheckResult {
	return &guard.CheckResult{Passed: true, Action: guard.ActionAllow}
}
