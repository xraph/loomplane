package agentauthz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xraph/cortex"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/security"
)

// Codec carries an agent run's principal across a pause: the workspace's
// agent and, in a chat, the person it acts for. Cortex stores what
// EncodePrincipal writes with the run and hands it to DecodePrincipal on
// every continuation, which checks again that both may still act before
// the run goes on.
type Codec struct {
	// Verifier rechecks the API key a person chatted with:
	// *apikeys.Service.
	Verifier security.PrincipalVerifier
	Authz    *security.Authorizer
	// Enabled reports whether the agent is on in a workspace:
	// agentsettings.Service.Enabled.
	Enabled func(ctx context.Context, orgID string) (bool, error)
}

var _ cortex.PrincipalCodec = (*Codec)(nil)

// EncodePrincipal writes the agent principal. Anything else is refused:
// no run of the agent starts as anyone else.
func (c *Codec) EncodePrincipal(_ context.Context, principal any) ([]byte, error) {
	p, ok := principal.(security.Principal)
	if !ok {
		return nil, fmt.Errorf("agentauthz: an agent run's principal is the agent, not %T", principal)
	}
	if err := agentShape(p); err != nil {
		return nil, fmt.Errorf("agentauthz: %w", err)
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("agentauthz: encode the agent's principal: %w", err)
	}
	return data, nil
}

// DecodePrincipal reads the principal back and checks it may still act.
// It's cortex.ErrPrincipalRevoked, which fails the run, when the bytes
// aren't the workspace's agent, the agent is off for the workspace, the
// person's API key was revoked, or the person may no longer use the
// agent (they left, or their role changed). Not being able to tell is
// cortex.ErrPrincipalUnavailable, and the run waits for the next try.
func (c *Codec) DecodePrincipal(ctx context.Context, data []byte) (any, error) {
	var p security.Principal
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, revoked("the stored principal can't be read")
	}
	if err := agentShape(p); err != nil {
		return nil, revoked(err.Error())
	}
	if c.Enabled == nil {
		return nil, unavailable("the agent's settings aren't ready")
	}
	on, err := c.Enabled(ctx, p.OrgID)
	if err != nil {
		return nil, unavailable("the workspace's agent settings couldn't be read")
	}
	if !on {
		return nil, revoked("the agent is off for this workspace")
	}
	person, acting := p.Person()
	if !acting {
		return p, nil
	}
	if person.KeyID != "" {
		if c.Verifier == nil {
			return nil, unavailable("API keys can't be checked")
		}
		if err = c.Verifier.ValidatePrincipal(ctx, person); err != nil {
			if errors.Is(err, apikeys.ErrRejected) {
				return nil, revoked("the API key the person used is no longer valid")
			}
			return nil, unavailable("the person's API key couldn't be checked")
		}
	}
	if c.Authz == nil {
		return nil, unavailable("permissions can't be checked")
	}
	if err = c.Authz.Require(ctx, person, security.AgentUse, ""); err != nil {
		if errors.Is(err, security.ErrForbidden) || errors.Is(err, security.ErrNotMember) || errors.Is(err, security.ErrUnauthenticated) {
			return nil, revoked("the person the agent acts for may no longer use it")
		}
		return nil, unavailable("the person's permissions couldn't be checked")
	}
	return p, nil
}

// agentShape checks p is a workspace's agent, acting alone or for one of
// that workspace's people.
func agentShape(p security.Principal) error {
	if !p.IsAgent() || p.OrgID == "" || p.UserID != security.AgentSubject(p.OrgID) {
		return errors.New("the principal isn't the workspace's agent")
	}
	person, acting := p.Person()
	if acting && (person.Kind != security.KindUser || person.OnBehalfOf != nil || person.OrgID != p.OrgID || person.UserID == "") {
		return errors.New("the agent acts only for its own workspace's people")
	}
	return nil
}

func revoked(why string) error {
	return fmt.Errorf("%w: %s", cortex.ErrPrincipalRevoked, why)
}

func unavailable(why string) error {
	return fmt.Errorf("%w: %s", cortex.ErrPrincipalUnavailable, why)
}
