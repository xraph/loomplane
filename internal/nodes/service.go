package nodes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

var (
	// ErrUpdateNode is returned to a node whose protocol or version is too far
	// behind this Loomplane to join.
	ErrUpdateNode = errors.New("update this node")
	// ErrWidenRemotely is returned when an update would add endpoints. Only
	// the machine's owner can widen what it serves.
	ErrWidenRemotely = errors.New("add endpoints on the machine with: loomctl node allow <endpoint>")
	// ErrMintNewToken wraps a failure to create the node after the join token
	// was spent. The operator needs a fresh token.
	ErrMintNewToken = errors.New("the join token was used but the node could not be created; mint a new join token and try again")
)

// JoinTokens spends node join tokens.
type JoinTokens interface {
	ConsumeNodeJoinToken(ctx context.Context, raw string) (*apikeys.NodeJoinClaim, error)
}

// Authorizer is the part of security.Authorizer this package uses.
type Authorizer interface {
	Allowed(ctx context.Context, p security.Principal, perm security.Permission) (bool, error)
}

// Auditor records node events. It may be nil.
type Auditor interface {
	Record(ctx context.Context, action string, p security.Principal, n Node, meta map[string]string)
}

// Service joins, lists, narrows and revokes nodes.
type Service struct {
	Store  Store
	Tokens JoinTokens
	Authz  Authorizer
	Audit  Auditor
	// Version is this Loomplane's version, for the one-minor-behind rule.
	Version string
	// Notify delivers a control message to the node's live session, if any.
	// The hub fills it. It may be nil.
	Notify func(nodeID string, msg any)
	// Adopt links a joined node to its cloud account. Phase 2 fills it. It may be nil.
	Adopt func(ctx context.Context, n *Node, claim *apikeys.NodeJoinClaim) error
}

// JoinRequest is what a machine sends to join.
type JoinRequest struct {
	Token           string
	Name            string
	PublicKey       []byte
	Endpoints       []nodeproto.Endpoint
	Labels          map[string]string
	Inventory       nodeproto.Inventory
	NodeVersion     string
	ProtocolVersion int
}

// JoinResult is what a machine learns on joining.
type JoinResult struct {
	NodeID             string
	OrgID              string
	IdentityGeneration int64
	Endpoints          []nodeproto.Endpoint
	CloudID            string
}

// Update changes a node. Nil and empty fields stay as they are.
type Update struct {
	Name        *string
	Labels      map[string]string
	Endpoints   []nodeproto.Endpoint
	IngressMode *string
}

// Join spends the token and creates a pending node. The checks that need no
// token run first, so a node that must update does not burn its token.
func (s *Service) Join(ctx context.Context, in JoinRequest) (*JoinResult, error) {
	if in.ProtocolVersion < nodeproto.Version || !s.versionOK(in.NodeVersion) {
		return nil, ErrUpdateNode
	}
	if len(in.PublicKey) != ed25519.PublicKeySize {
		return nil, errors.New("nodes: bad public key")
	}
	claim, err := s.Tokens.ConsumeNodeJoinToken(ctx, in.Token)
	if err != nil {
		return nil, err
	}
	id := "node_" + uuid.NewString()
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "node-" + strings.ReplaceAll(id[len("node_"):], "-", "")[:6]
	}
	role := "node"
	if claim.Scope.Edge {
		role = "edge"
	}
	labels := map[string]string{}
	maps.Copy(labels, in.Labels)
	maps.Copy(labels, claim.Scope.Labels)
	n := &Node{
		ID: id, OrgID: claim.OrgID, Name: name, PublicKey: in.PublicKey,
		KeyFingerprint: nodeproto.Fingerprint(ed25519.PublicKey(in.PublicKey)), IdentityGeneration: 1,
		Status: StatusPending, Endpoints: joinEndpoints(in.Endpoints, claim.Scope.Endpoints), IngressMode: "none",
		Role: role, EdgeScope: claim.Scope.EdgeScope, Labels: labels, Version: in.NodeVersion,
		ProtocolVersion: in.ProtocolVersion, Inventory: in.Inventory,
		JoinedBy: claim.MintedBy, JoinedByKeyID: claim.MintedByKeyID,
	}
	if !claim.Scope.Edge {
		n.EdgeScope = ""
	}
	if err := s.Store.Create(ctx, n); err != nil {
		if errors.Is(err, ErrNameTaken) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %w", ErrMintNewToken, err)
	}
	if s.Adopt != nil {
		if err := s.Adopt(ctx, n, claim); err != nil {
			return nil, fmt.Errorf("adopting into the cloud account: %w", err)
		}
	}
	s.record(ctx, "node.joined", security.ActorPrincipal(claim.OrgID, claim.MintedBy), *n, map[string]string{"role": role})
	return &JoinResult{NodeID: n.ID, OrgID: n.OrgID, IdentityGeneration: n.IdentityGeneration, Endpoints: n.Endpoints, CloudID: claim.Scope.CloudID}, nil
}

// joinEndpoints is the request limited to the scope, where an empty scope
// allows the request as is. Control and unknown endpoints are dropped.
func joinEndpoints(req, scope []nodeproto.Endpoint) []nodeproto.Endpoint {
	out := []nodeproto.Endpoint{}
	for _, e := range req {
		if !slices.Contains(nodeproto.AllowableEndpoints(), e) || slices.Contains(out, e) {
			continue
		}
		if len(scope) == 0 || slices.Contains(scope, e) {
			out = append(out, e)
		}
	}
	return out
}

// versionOK allows an equal, newer or exactly one-minor-behind node on the
// same major. A dev or unparsable version on either side passes.
func (s *Service) versionOK(node string) bool {
	sm, sn, ok1 := parseVersion(s.Version)
	nm, nn, ok2 := parseVersion(node)
	if !ok1 || !ok2 {
		return true
	}
	if nm != sm {
		return nm > sm
	}
	return nn >= sn-1
}

func parseVersion(v string) (major, minor int, ok bool) {
	parts := strings.SplitN(strings.TrimPrefix(v, "v"), ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	var err1, err2 error
	major, err1 = strconv.Atoi(parts[0])
	minor, err2 = strconv.Atoi(parts[1])
	return major, minor, err1 == nil && err2 == nil
}

func (s *Service) allow(ctx context.Context, p security.Principal, perm security.Permission, write bool) error {
	if write && p.ReadOnly {
		return security.ErrForbidden
	}
	if s.Authz == nil {
		return security.ErrForbidden
	}
	ok, err := s.Authz.Allowed(ctx, p, perm)
	if err != nil {
		return err
	}
	if !ok {
		return security.ErrForbidden
	}
	return nil
}

// List returns the org's nodes.
func (s *Service) List(ctx context.Context, p security.Principal) ([]Node, error) {
	if err := s.allow(ctx, p, security.NodeRead, false); err != nil {
		return nil, err
	}
	return s.Store.List(ctx, p.OrgID)
}

// Get returns one node of the org.
func (s *Service) Get(ctx context.Context, p security.Principal, id string) (*Node, error) {
	if err := s.allow(ctx, p, security.NodeRead, false); err != nil {
		return nil, err
	}
	return s.Store.Get(ctx, p.OrgID, id)
}

// JoinedByKey lists the nodes that joined with a token made by the API key.
func (s *Service) JoinedByKey(ctx context.Context, p security.Principal, keyID string) ([]Node, error) {
	if err := s.allow(ctx, p, security.NodeManage, false); err != nil {
		return nil, err
	}
	return s.Store.ListJoinedByKey(ctx, p.OrgID, keyID)
}

// Update renames a node, relabels it, narrows its endpoints or sets its
// ingress mode. A live node hears a narrowing at once.
func (s *Service) Update(ctx context.Context, p security.Principal, id string, u Update) (*Node, error) {
	if err := s.allow(ctx, p, security.NodeManage, true); err != nil {
		return nil, err
	}
	n, err := s.Store.Get(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if n.Status == StatusRevoked {
		return nil, ErrNotFound
	}
	narrowed := false
	if u.Endpoints != nil {
		for _, e := range u.Endpoints {
			if !slices.Contains(n.Endpoints, e) {
				return nil, ErrWidenRemotely
			}
		}
		next := joinEndpoints(u.Endpoints, nil)
		narrowed = len(next) != len(n.Endpoints)
		n.Endpoints = next
	}
	if u.IngressMode != nil {
		switch *u.IngressMode {
		case "none", "direct", "relay":
			n.IngressMode = *u.IngressMode
		default:
			return nil, fmt.Errorf("nodes: ingress mode must be none, direct or relay")
		}
	}
	if u.Name != nil {
		name := strings.TrimSpace(*u.Name)
		if name == "" {
			return nil, errors.New("nodes: name is required")
		}
		n.Name = name
	}
	if u.Labels != nil {
		n.Labels = maps.Clone(u.Labels)
	}
	if err := s.Store.Update(ctx, n); err != nil {
		return nil, err
	}
	if narrowed && s.Notify != nil {
		s.Notify(n.ID, nodeproto.Narrow{Endpoints: n.Endpoints})
	}
	s.record(ctx, "node.updated", p, *n, nil)
	return n, nil
}

// Revoke ends a node's trust for good and tells a live session.
func (s *Service) Revoke(ctx context.Context, p security.Principal, id string) error {
	if err := s.allow(ctx, p, security.NodeManage, true); err != nil {
		return err
	}
	n, err := s.Store.Get(ctx, p.OrgID, id)
	if err != nil {
		return err
	}
	return s.revoke(ctx, n, p.UserID, p, "node.revoked")
}

// Leave revokes a node at its own request, with the node as the actor.
func (s *Service) Leave(ctx context.Context, nodeID string) error {
	n, err := s.Store.GetByID(ctx, nodeID)
	if err != nil {
		return err
	}
	return s.revoke(ctx, n, n.ID, security.Principal{OrgID: n.OrgID, UserID: n.ID}, "node.left")
}

// revoke sets revoked_at and revoked_by through Update. A bare status change
// is not terminal in SQL.
func (s *Service) revoke(ctx context.Context, n *Node, by string, actor security.Principal, action string) error {
	if n.Status == StatusRevoked {
		return nil
	}
	now := time.Now().UTC()
	n.Status, n.RevokedAt, n.RevokedBy = StatusRevoked, &now, by
	if err := s.Store.Update(ctx, n); err != nil {
		return err
	}
	if s.Notify != nil {
		s.Notify(n.ID, nodeproto.Revoked{})
	}
	s.record(ctx, action, actor, *n, nil)
	return nil
}

func (s *Service) record(ctx context.Context, action string, p security.Principal, n Node, meta map[string]string) {
	if s.Audit != nil {
		s.Audit.Record(ctx, action, p, n, meta)
	}
}
