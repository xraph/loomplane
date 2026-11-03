package nodes

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// ErrDockerNotAllowed is an adoption of a node that doesn't serve Docker.
// Only the machine's owner can allow it.
var ErrDockerNotAllowed = errors.New("this node doesn't serve Docker; allow it on the machine with: loomctl node allow docker")

// NodeConnectionWriter writes the connection that reaches what runs on a
// node. *connections.Service satisfies it.
type NodeConnectionWriter interface {
	CreateNodeConnection(ctx context.Context, p security.Principal, in connections.NodeConnectionInput) (*connections.Connection, error)
}

// Adopter connects what runs on a joined node, so a Cloud can take it in.
// This package never imports cloudinfra: cloudinfra calls the Adopter.
type Adopter struct {
	Connections NodeConnectionWriter
}

// AdoptDocker connects the node's Docker as a server connection, written
// as p. It is ErrDockerNotAllowed when the node doesn't serve Docker, and
// the connection's own errors otherwise: connections.ErrUnavailable means
// the node or its daemon didn't answer the check, and nothing was stored.
func (a *Adopter) AdoptDocker(ctx context.Context, p security.Principal, n *Node, settings map[string]string) (*connections.Connection, error) {
	if a == nil || a.Connections == nil || n == nil {
		return nil, fmt.Errorf("%w: nothing to adopt with", ErrInvalid)
	}
	if p.OrgID == "" || p.OrgID != n.OrgID {
		return nil, security.ErrForbidden
	}
	if !slices.Contains(n.Endpoints, nodeproto.EndpointDocker) {
		return nil, ErrDockerNotAllowed
	}
	return a.Connections.CreateNodeConnection(ctx, p, connections.NodeConnectionInput{
		Cloud: connections.Server, NodeID: n.ID, Name: n.Name, IdentityGeneration: n.IdentityGeneration, Settings: settings,
	})
}
