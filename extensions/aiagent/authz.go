package aiagent

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/xraph/cortex"
	"github.com/xraph/cortex/llm"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/agentauthz"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// bindAuthorization gives the risk table and the principal codec the
// services they read, binds the tool authorizer cortex already holds to
// the table layered under shield, and provides the table, so release
// preparation can install its promotion gate.
func (e *Extension) bindAuthorization(c forge.Container, settings *agentsettings.Service, projectSvc *projects.Service) error {
	authz, err := forge.InjectType[*security.Authorizer](c)
	if err != nil {
		return fmt.Errorf("the agent's authorizer needs the identity extension's authorizer: %w", err)
	}
	keys, err := forge.InjectType[security.PrincipalVerifier](c)
	if err != nil {
		return fmt.Errorf("the agent's principal codec needs the API key verifier: %w", err)
	}
	e.authz.Plans, e.authz.Projects, e.authz.Authz = e.deps.Plans, projectSvc, authz
	e.codec.Verifier, e.codec.Authz, e.codec.Enabled = keys, authz, settings.Enabled
	e.toolAuthorizer.bind(agentauthz.Layered(e.authz, e.shield.Engine()))
	if err = forge.ProvideValue(c, e.authz); err != nil {
		return fmt.Errorf("provide the agent's authorizer: %w", err)
	}
	return nil
}

// lateAuthorizer is the tool authorizer cortex is built with, before the
// one it stands for exists. Until bind, it shows the model nothing and
// refuses every call.
type lateAuthorizer struct {
	inner atomic.Pointer[authorizerBox]
}

type authorizerBox struct{ a cortex.ToolAuthorizer }

func (l *lateAuthorizer) bind(a cortex.ToolAuthorizer) { l.inner.Store(&authorizerBox{a: a}) }

func (l *lateAuthorizer) Visible(ctx context.Context, s cortex.Subject, tools []llm.Tool) []llm.Tool {
	if b := l.inner.Load(); b != nil {
		return b.a.Visible(ctx, s, tools)
	}
	return nil
}

func (l *lateAuthorizer) Authorize(ctx context.Context, s cortex.Subject, call llm.ToolCall) error {
	if b := l.inner.Load(); b != nil {
		return b.a.Authorize(ctx, s, call)
	}
	return errNotReady
}
