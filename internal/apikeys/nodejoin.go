package apikeys

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xraph/keysmith"
	"github.com/xraph/keysmith/key"
	"github.com/xraph/keysmith/store"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// NodeJoinPrefix starts every node join token: lpj_live_ and 64 hex
// characters. It differs from Prefix on purpose, so a join token is never
// taken for an API key.
const NodeJoinPrefix = "lpj"

// MaxNodeJoinLifetime is the longest a join token lives, and what zero means.
// A token is typed into a new machine within minutes; an hour is generous.
const MaxNodeJoinLifetime = time.Hour

// Edge scopes. An org edge serves one organization; an instance edge serves
// the whole instance and so needs a platform admin.
const (
	EdgeScopeOrg      = "org"
	EdgeScopeInstance = "instance"
)

// Limits on what a join token may carry.
const (
	maxJoinLabels = 32
	maxJoinField  = 128
)

// PlatformAdmins tells whether a user administers the whole instance.
type PlatformAdmins interface {
	IsPlatformAdmin(ctx context.Context, userID string) (bool, error)
}

// NodeJoinScope is what a joining machine is allowed to become.
type NodeJoinScope struct {
	CloudID   string
	Labels    map[string]string
	Endpoints []nodeproto.Endpoint
	Edge      bool
	EdgeScope string
}

// NodeJoinToken is a new join token, with the secret, which is never shown again.
type NodeJoinToken struct {
	ID        string
	Secret    string
	ExpiresAt time.Time
}

// NodeJoinClaim is what a consumed join token grants.
type NodeJoinClaim struct {
	OrgID, MintedBy, MintedByKeyID string
	Scope                          NodeJoinScope
}

// IsNodeJoinToken reports whether s looks like a node join token.
func IsNodeJoinToken(s string) bool { return strings.HasPrefix(s, NodeJoinPrefix+"_") }

// CreateNodeJoinToken mints a single-use token for one machine to join the
// caller's organization. An admin may mint it from a session or from a write
// API key. That is a deliberate exception to CreateFleetCredential: the
// token is short-lived, spends itself on first use, and the maker's live
// role is checked now.
func (s *Service) CreateNodeJoinToken(ctx context.Context, p security.Principal, scope NodeJoinScope, lifetime time.Duration) (*NodeJoinToken, error) {
	if p.IsServiceAccount() || p.Fleet != nil || p.ReadOnly || p.OrgID == "" || p.UserID == "" {
		return nil, security.ErrForbidden
	}
	if s.authz == nil {
		return nil, security.ErrForbidden
	}
	allowed, err := s.authz.Allowed(ctx, p, security.NodeManage)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, security.ErrForbidden
	}
	if err = validNodeJoinScope(&scope); err != nil {
		return nil, err
	}
	if scope.EdgeScope == EdgeScopeInstance {
		if s.admins == nil {
			return nil, security.ErrForbidden
		}
		platform, perr := s.admins.IsPlatformAdmin(ctx, p.UserID)
		if perr != nil {
			return nil, perr
		}
		if !platform {
			return nil, security.ErrForbidden
		}
	}
	if lifetime <= 0 || lifetime > MaxNodeJoinLifetime {
		lifetime = MaxNodeJoinLifetime
	}
	expires := s.now().Add(lifetime).UTC()
	endpoints := make([]string, len(scope.Endpoints))
	for i, e := range scope.Endpoints {
		endpoints[i] = string(e)
	}
	name := "node join token"
	if p.KeyID != "" {
		name = "node join token (minted with an API key)"
	}
	res, err := s.engine.CreateKey(s.tenant(ctx, p.OrgID), &keysmith.CreateKeyInput{
		Name: name, Prefix: NodeJoinPrefix, Environment: key.EnvLive, TenantID: p.OrgID,
		CreatedBy: p.UserID, ExpiresAt: &expires,
		Metadata: map[string]any{
			"kind": string(security.KindNodeJoin), "cloud_id": scope.CloudID, "labels": scope.Labels,
			"endpoints": endpoints, "edge": scope.Edge, "edge_scope": scope.EdgeScope,
			"minted_by_key_id": p.KeyID,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create the join token: %w", err)
	}
	if s.audit != nil {
		s.audit.Record(ctx, "node.join_token.created", p, view(res.Key))
	}
	return &NodeJoinToken{ID: res.Key.ID.String(), Secret: res.RawKey, ExpiresAt: expires}, nil
}

func validNodeJoinScope(sc *NodeJoinScope) error {
	if len(sc.CloudID) > maxJoinField || len(sc.Labels) > maxJoinLabels {
		return fmt.Errorf("%w: the join scope is too large", ErrInvalid)
	}
	for k, v := range sc.Labels {
		if k == "" || len(k) > maxJoinField || len(v) > maxJoinField {
			return fmt.Errorf("%w: a label is empty or too long", ErrInvalid)
		}
	}
	for _, e := range sc.Endpoints {
		if e == "" || len(e) > maxJoinField {
			return fmt.Errorf("%w: an endpoint is empty or too long", ErrInvalid)
		}
	}
	switch sc.EdgeScope {
	case "":
		if sc.Edge {
			sc.EdgeScope = EdgeScopeOrg
		}
	case EdgeScopeOrg, EdgeScopeInstance:
		if !sc.Edge {
			return fmt.Errorf("%w: an edge scope needs an edge", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: the edge scope is org or instance", ErrInvalid)
	}
	return nil
}

// ConsumeNodeJoinToken spends a join token and returns what it granted. A
// token works once: the key is revoked with Keysmith's version check, so of
// two callers racing on one token exactly one wins and the other is
// rejected. Anything that isn't a live join token is ErrRejected.
func (s *Service) ConsumeNodeJoinToken(ctx context.Context, raw string) (*NodeJoinClaim, error) {
	if !IsNodeJoinToken(raw) {
		return nil, ErrRejected
	}
	res, err := s.engine.ValidateKey(ctx, raw)
	if err != nil {
		return nil, ErrRejected
	}
	k := res.Key
	claim, err := s.nodeJoinClaim(k)
	if err != nil {
		return nil, err
	}
	tctx := s.tenant(ctx, k.TenantID)
	// RevokeKey re-reads the key and writes only if its version is the one it
	// read. A lost race shows as a conflict or as an already revoked key. A
	// conflict can also be an unrelated write to the row, so look again: if
	// the key is still active the token is still ours to spend.
	for range 3 {
		err = s.engine.RevokeKey(tctx, k.ID, "node joined")
		if err == nil {
			return claim, nil
		}
		if !errors.Is(err, store.ErrKeyConflict) {
			return nil, ErrRejected
		}
		cur, gerr := s.engine.GetKey(tctx, k.ID)
		if gerr != nil || cur.State != key.StateActive || cur.RevokedAt != nil {
			return nil, ErrRejected
		}
	}
	return nil, ErrRejected
}

func (s *Service) nodeJoinClaim(k *key.Key) (*NodeJoinClaim, error) {
	if k == nil || k.AppID != s.appID || k.TenantID == "" || k.CreatedBy == "" || k.State != key.StateActive || k.RevokedAt != nil || k.ExpiresAt == nil || !k.ExpiresAt.After(s.now()) {
		return nil, ErrRejected
	}
	if kind, _ := k.Metadata["kind"].(string); kind != string(security.KindNodeJoin) {
		return nil, ErrRejected
	}
	var meta struct {
		CloudID       string            `json:"cloud_id"`
		Labels        map[string]string `json:"labels"`
		Endpoints     []string          `json:"endpoints"`
		Edge          bool              `json:"edge"`
		EdgeScope     string            `json:"edge_scope"`
		MintedByKeyID string            `json:"minted_by_key_id"`
	}
	// Metadata is native values in memory and decoded JSON in Postgres; a
	// round trip through JSON reads both the same way.
	b, err := json.Marshal(k.Metadata)
	if err != nil || json.Unmarshal(b, &meta) != nil {
		return nil, ErrRejected
	}
	scope := NodeJoinScope{CloudID: meta.CloudID, Labels: meta.Labels, Edge: meta.Edge, EdgeScope: meta.EdgeScope}
	for _, e := range meta.Endpoints {
		scope.Endpoints = append(scope.Endpoints, nodeproto.Endpoint(e))
	}
	return &NodeJoinClaim{OrgID: k.TenantID, MintedBy: k.CreatedBy, MintedByKeyID: meta.MintedByKeyID, Scope: scope}, nil
}
