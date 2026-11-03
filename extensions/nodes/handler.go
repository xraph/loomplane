package nodes

import (
	"context"
	"net/http"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/security"
)

// noMinter is the minter of a handler built without API keys.
type noMinter struct{}

func (noMinter) CreateNodeJoinToken(context.Context, security.Principal, apikeys.NodeJoinScope, time.Duration) (*apikeys.NodeJoinToken, error) {
	return nil, apikeys.ErrUnavailable
}

func (noMinter) ListNodeJoinTokens(context.Context, security.Principal) ([]apikeys.PendingNodeJoinToken, error) {
	return nil, apikeys.ErrUnavailable
}

func (noMinter) CancelNodeJoinToken(context.Context, security.Principal, string) error {
	return apikeys.ErrUnavailable
}

// NodeHandler serves the nodes routes the platform mounts (join, challenge,
// connect, hop and the rest of /api/v1/nodes) over a hub and service the
// caller already bound, with no guard. It is for tests that need the real
// handlers without a database; ending ctx ends the sessions it holds. The
// platform itself goes through Register.
func NodeHandler(ctx context.Context, hub *nodes.Hub, svc *nodes.Service, publicURL string) (http.Handler, error) {
	e := &Extension{
		BaseExtension: forge.NewBaseExtension(Name, "handler", ""),
		cfg:           config.Config{PublicURL: publicURL},
		hub:           hub, svc: svc, minter: noMinter{}, run: ctx,
	}
	e.SetLogger(forge.NewNoopLogger())
	r := forge.NewRouter()
	if err := e.routes(r); err != nil {
		return nil, err
	}
	if err := e.mountNodeEndpoints(r); err != nil {
		return nil, err
	}
	return r, nil
}
