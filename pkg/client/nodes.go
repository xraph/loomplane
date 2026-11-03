package client

import (
	"context"
	"net/http"
	"time"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// NodeJoinScope scopes a join token to the machine it is for. Empty fields
// leave the machine free to choose.
type NodeJoinScope struct {
	Labels    map[string]string    `json:"labels,omitempty"`
	Endpoints []nodeproto.Endpoint `json:"endpoints,omitempty"`
	// Edge makes the machine an edge, which serves traffic for the org.
	Edge bool `json:"edge,omitempty"`
	// EdgeScope is "org" or "instance". Only a platform admin may mint an
	// instance edge.
	EdgeScope string `json:"edge_scope,omitempty"`
	CloudID   string `json:"cloud_id,omitempty"`
	// LifetimeMinutes is how long the token can be used. Zero means an hour,
	// which is also the most Loomplane allows.
	LifetimeMinutes int `json:"lifetime_minutes,omitempty"`
}

// NodeJoinToken is a new single-use join token. The token is shown once.
type NodeJoinToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	// Command is the line to run on the machine.
	Command string `json:"command"`
}

// JoinRequest is what a machine sends to join. The token authenticates it.
type JoinRequest struct {
	Token string `json:"token"`
	Name  string `json:"name,omitempty"`
	// PublicKey is the node's Ed25519 public key, base64.
	PublicKey string `json:"public_key"`
	// Signature is nodeproto.SignJoin over Token, Name and the key, base64.
	Signature       string               `json:"signature"`
	Endpoints       []nodeproto.Endpoint `json:"endpoints,omitempty"`
	Labels          map[string]string    `json:"labels,omitempty"`
	Inventory       nodeproto.Inventory  `json:"inventory"`
	NodeVersion     string               `json:"node_version"`
	ProtocolVersion int                  `json:"protocol_version"`
}

// JoinResult is what a machine learns on joining.
type JoinResult struct {
	NodeID             string               `json:"node_id"`
	OrgID              string               `json:"org_id"`
	IdentityGeneration int64                `json:"identity_generation"`
	Endpoints          []nodeproto.Endpoint `json:"endpoints"`
	CloudID            string               `json:"cloud_id,omitempty"`
}

// Node is one machine joined to an organization.
type Node struct {
	ID                 string               `json:"id"`
	OrgID              string               `json:"org_id"`
	Name               string               `json:"name"`
	KeyFingerprint     string               `json:"key_fingerprint"`
	IdentityGeneration int64                `json:"identity_generation"`
	Status             string               `json:"status"`
	Endpoints          []nodeproto.Endpoint `json:"endpoints"`
	IngressMode        string               `json:"ingress_mode"`
	Role               string               `json:"role"`
	EdgeScope          string               `json:"edge_scope"`
	Labels             map[string]string    `json:"labels"`
	Version            string               `json:"version"`
	ProtocolVersion    int                  `json:"protocol_version"`
	Inventory          nodeproto.Inventory  `json:"inventory"`
	LastSeenAt         *time.Time           `json:"last_seen_at"`
	CreatedAt          time.Time            `json:"created_at"`
	UpdatedAt          time.Time            `json:"updated_at"`
}

// NodeUpdate changes a node. A nil field stays as it is. An empty, non-nil
// Endpoints narrows the node to nothing and an empty Labels clears them.
// Endpoints can only be narrowed here: widen them on the machine.
type NodeUpdate struct {
	Name        *string              `json:"name"`
	Labels      map[string]string    `json:"labels"`
	Endpoints   []nodeproto.Endpoint `json:"endpoints"`
	IngressMode *string              `json:"ingress_mode"`
}

// CreateNodeJoinToken makes a single-use token for one machine to join, and
// the command that uses it.
func (c *Client) CreateNodeJoinToken(ctx context.Context, scope NodeJoinScope) (*NodeJoinToken, error) {
	var out NodeJoinToken
	if err := c.do(ctx, http.MethodPost, "/api/v1/nodes/join-tokens", scope, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// NodeJoin joins a machine to the Loomplane at base. It needs no API key: the
// join token in the request is the machine's only credential, and it is
// spent by this call.
func NodeJoin(ctx context.Context, base string, req JoinRequest) (*JoinResult, error) {
	var out JoinResult
	if err := New(base, "").do(ctx, http.MethodPost, "/api/v1/nodes/join", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Nodes lists the machines joined to your organization.
func (c *Client) Nodes(ctx context.Context) ([]Node, error) {
	var out struct {
		Nodes []Node `json:"nodes"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/nodes", nil, &out); err != nil {
		return nil, err
	}
	return out.Nodes, nil
}

// RevokeNode stops trusting a node at once. Its key can't connect again.
func (c *Client) RevokeNode(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/api/v1/nodes/"+esc(id)+"/revoke", nil, nil)
}

// UpdateNode renames a node, relabels it, narrows what it serves or sets its
// ingress mode.
func (c *Client) UpdateNode(ctx context.Context, id string, u NodeUpdate) (*Node, error) {
	var out Node
	if err := c.do(ctx, http.MethodPatch, "/api/v1/nodes/"+esc(id), u, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
