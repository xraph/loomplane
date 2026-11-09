package apikeys

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/xraph/keysmith"
	"github.com/xraph/keysmith/id"
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

// ErrUnavailable is a store or permission backend that failed, as opposed to
// a bad, spent or expired token (ErrRejected). A caller may answer 503.
var ErrUnavailable = errors.New("node join tokens are temporarily unavailable")

// MetaAuditor is an Auditor that can also take structured details. The join
// token audit uses it when the Auditor offers it.
type MetaAuditor interface {
	RecordWithMeta(ctx context.Context, action string, p security.Principal, k APIKey, meta map[string]string)
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
	if err := s.mayMintNodeJoin(ctx, p); err != nil {
		return nil, err
	}
	if verr := validNodeJoinScope(&scope); verr != nil {
		return nil, verr
	}
	if scope.EdgeScope == EdgeScopeInstance {
		if err := s.mayMintInstanceEdge(ctx, p); err != nil {
			return nil, err
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
		if ma, ok := s.audit.(MetaAuditor); ok {
			ma.RecordWithMeta(ctx, "node.join_token.created", p, view(res.Key), map[string]string{
				"minted_by_key_id": p.KeyID, "edge_scope": scope.EdgeScope, "minted_with_key": fmt.Sprint(p.KeyID != ""),
			})
		} else {
			s.audit.Record(ctx, "node.join_token.created", p, view(res.Key))
		}
	}
	return &NodeJoinToken{ID: res.Key.ID.String(), Secret: res.RawKey, ExpiresAt: expires}, nil
}

// mayMintNodeJoin refuses anyone who may not mint a join token in their
// organization: a person, with a session or a write API key, whose live role
// may manage nodes.
func (s *Service) mayMintNodeJoin(ctx context.Context, p security.Principal) error {
	if p.IsServiceAccount() || p.Fleet != nil || p.ReadOnly || p.OrgID == "" || p.UserID == "" {
		return security.ErrForbidden
	}
	if s.authz == nil {
		return security.ErrForbidden
	}
	allowed, err := s.authz.Allowed(ctx, p, security.NodeManage)
	if err != nil {
		return err
	}
	if !allowed {
		return security.ErrForbidden
	}
	return nil
}

// mayMintInstanceEdge refuses anyone who may not mint an instance-wide edge:
// that takes a platform admin, signed in.
func (s *Service) mayMintInstanceEdge(ctx context.Context, p security.Principal) error {
	// Instance-wide authority is session-only, like the instance console.
	if p.KeyID != "" || s.admins == nil {
		return security.ErrForbidden
	}
	platform, err := s.admins.IsPlatformAdmin(ctx, p.UserID)
	if err != nil {
		return err
	}
	if !platform {
		return security.ErrForbidden
	}
	return nil
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
	// node join --builder dedicated puts build_workers here, and a node
	// adopted with it builds untrusted source, so it takes no other value.
	if workers, ok := sc.Labels["build_workers"]; ok && workers != "dedicated" {
		return fmt.Errorf("%w: build_workers can only be dedicated", ErrInvalid)
	}
	allowed := nodeproto.AllowableEndpoints()
	var endpoints []nodeproto.Endpoint
	for _, e := range sc.Endpoints {
		if !e.Valid() || !slices.Contains(allowed, e) {
			return fmt.Errorf("%w: %q is not an endpoint a node can be allowed", ErrInvalid, e)
		}
		if !slices.Contains(endpoints, e) {
			endpoints = append(endpoints, e)
		}
	}
	sc.Endpoints = endpoints
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
// rejected. Who minted it is checked again now, with their live role; if
// that fails the token is burned all the same. Anything that isn't a live
// join token is ErrRejected; a failing backend is ErrUnavailable.
func (s *Service) ConsumeNodeJoinToken(ctx context.Context, raw string) (*NodeJoinClaim, error) {
	k, claim, err := s.liveNodeJoinKey(ctx, raw)
	if err != nil {
		return nil, err
	}
	tctx := s.tenant(ctx, k.TenantID)
	// Spend first, then check the minter: whoever wins the race owns the
	// outcome, and a token that fails the check is burned.
	if serr := s.spendNodeJoinKey(tctx, k, "node joined"); serr != nil {
		return nil, serr
	}
	ok, err := s.minterStillAllowed(ctx, claim)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrRejected
	}
	return claim, nil
}

// PeekNodeJoinToken returns what a live join token grants, without spending
// it, so a join can be refused for a reason the operator can fix (a name or
// key already in use) while the token still works. It answers like
// ConsumeNodeJoinToken: ErrRejected for anything that isn't a live join
// token, ErrUnavailable for a failing backend. Only Consume decides.
func (s *Service) PeekNodeJoinToken(ctx context.Context, raw string) (*NodeJoinClaim, error) {
	_, claim, err := s.liveNodeJoinKey(ctx, raw)
	return claim, err
}

// liveNodeJoinKey checks raw and reads the claim of the live join token it is.
func (s *Service) liveNodeJoinKey(ctx context.Context, raw string) (*key.Key, *NodeJoinClaim, error) {
	if !IsNodeJoinToken(raw) {
		return nil, nil, ErrRejected
	}
	res, err := s.engine.ValidateKey(ctx, raw)
	if err != nil {
		if errors.Is(err, keysmith.ErrInvalidKey) || errors.Is(err, keysmith.ErrKeyInactive) || errors.Is(err, keysmith.ErrKeyExpired) {
			return nil, nil, ErrRejected
		}
		return nil, nil, ErrUnavailable
	}
	claim, err := s.nodeJoinClaim(res.Key)
	if err != nil {
		return nil, nil, err
	}
	return res.Key, claim, nil
}

func (s *Service) spendNodeJoinKey(tctx context.Context, k *key.Key, reason string) error {
	// RevokeKey re-reads the key and writes only if its version is the one it
	// read. A lost race shows as a conflict or as an already revoked key. A
	// conflict can also be an unrelated write to the row, so look again: if
	// the key is still active the token is still ours to spend.
	for range 3 {
		err := s.engine.RevokeKey(tctx, k.ID, reason)
		if err == nil {
			return nil
		}
		switch {
		case errors.Is(err, keysmith.ErrInvalidStateTransition) || errors.Is(err, store.ErrKeyNotFound):
			return ErrRejected
		case !errors.Is(err, store.ErrKeyConflict):
			return ErrUnavailable
		}
		cur, gerr := s.engine.GetKey(tctx, k.ID)
		if gerr != nil {
			return ErrUnavailable
		}
		if cur.State != key.StateActive || cur.RevokedAt != nil {
			return ErrRejected
		}
	}
	return ErrUnavailable
}

// minterStillAllowed checks the person who minted a token, and the key they
// minted it with, as they are now.
func (s *Service) minterStillAllowed(ctx context.Context, c *NodeJoinClaim) (bool, error) {
	if s.authz == nil {
		return false, nil
	}
	ok, err := s.authz.Allowed(ctx, security.Principal{UserID: c.MintedBy, OrgID: c.OrgID}, security.NodeManage)
	if err != nil {
		return false, ErrUnavailable
	}
	if !ok {
		return false, nil
	}
	if c.MintedByKeyID != "" {
		kid, perr := id.ParseKeyID(c.MintedByKeyID)
		if perr != nil {
			return false, nil
		}
		mk, gerr := s.engine.GetKey(s.tenant(ctx, c.OrgID), kid)
		if gerr != nil {
			if errors.Is(gerr, store.ErrKeyNotFound) {
				return false, nil
			}
			return false, ErrUnavailable
		}
		if mk == nil || mk.AppID != s.appID || mk.TenantID != c.OrgID || mk.CreatedBy != c.MintedBy || mk.State != key.StateActive || mk.RevokedAt != nil || mk.PolicyID != nil || len(mk.Scopes) > 0 || mk.ExpiresAt == nil || !mk.ExpiresAt.After(s.now()) || access(mk) != Write {
			return false, nil
		}
	}
	if c.Scope.EdgeScope == EdgeScopeInstance {
		if s.admins == nil || c.MintedByKeyID != "" {
			return false, nil
		}
		platform, perr := s.admins.IsPlatformAdmin(ctx, c.MintedBy)
		if perr != nil {
			return false, ErrUnavailable
		}
		if !platform {
			return false, nil
		}
	}
	return true, nil
}

func (s *Service) nodeJoinClaim(k *key.Key) (*NodeJoinClaim, error) {
	if k == nil || k.AppID != s.appID || k.TenantID == "" || k.CreatedBy == "" || k.State != key.StateActive || k.RevokedAt != nil || k.ExpiresAt == nil || !k.ExpiresAt.After(s.now()) {
		return nil, ErrRejected
	}
	if kind, ok := k.Metadata["kind"].(string); !ok || kind != string(security.KindNodeJoin) {
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

// PendingNodeJoinToken is a join token no machine has used yet, as its
// organization sees it. The secret is never in it.
type PendingNodeJoinToken struct {
	ID string `json:"id"`
	// Hint is the end of the token, to tell it apart from others.
	Hint string `json:"hint"`
	// MintedBy is the user who minted it; MintedByKeyID the API key they
	// minted it with, empty when it came from a session.
	MintedBy      string               `json:"minted_by"`
	MintedByKeyID string               `json:"minted_by_key_id,omitempty"`
	CloudID       string               `json:"cloud_id,omitempty"`
	Labels        map[string]string    `json:"labels,omitempty"`
	Endpoints     []nodeproto.Endpoint `json:"endpoints"`
	Edge          bool                 `json:"edge"`
	EdgeScope     string               `json:"edge_scope,omitempty"`
	CreatedAt     time.Time            `json:"created_at"`
	ExpiresAt     time.Time            `json:"expires_at"`
}

// ListNodeJoinTokens returns the organization's join tokens that are still
// unspent and unexpired, newest first. Anyone who may mint one may list
// them all, whoever minted them. The list is audited.
func (s *Service) ListNodeJoinTokens(ctx context.Context, p security.Principal) ([]PendingNodeJoinToken, error) {
	if err := s.mayMintNodeJoin(ctx, p); err != nil {
		return nil, err
	}
	keys, err := s.engine.ListKeys(s.tenant(ctx, p.OrgID), &key.ListFilter{TenantID: p.OrgID, State: key.StateActive})
	if err != nil {
		return nil, fmt.Errorf("list join tokens: %w", err)
	}
	out := []PendingNodeJoinToken{}
	for _, k := range keys {
		if k.TenantID != p.OrgID {
			continue
		}
		// nodeJoinClaim refuses anything that isn't a live join token of
		// this app: other keys, spent, cancelled and expired tokens.
		claim, cerr := s.nodeJoinClaim(k)
		if cerr != nil {
			continue
		}
		out = append(out, pendingNodeJoinToken(k, claim))
	}
	slices.SortFunc(out, func(a, b PendingNodeJoinToken) int { return b.CreatedAt.Compare(a.CreatedAt) })
	if s.audit != nil {
		meta := map[string]string{"count": fmt.Sprint(len(out))}
		if ma, ok := s.audit.(MetaAuditor); ok {
			ma.RecordWithMeta(ctx, "node.join_token.listed", p, APIKey{Name: "node join tokens"}, meta)
		} else {
			s.audit.Record(ctx, "node.join_token.listed", p, APIKey{Name: "node join tokens"})
		}
	}
	return out, nil
}

// CancelNodeJoinToken revokes an unspent join token of the caller's
// organization, so no machine can join with it. Only someone who may mint
// that token may cancel it: an instance edge token takes a signed-in
// platform admin. A token that is spent, expired or not a join token of this
// organization is ErrNotFound. The cancel is audited.
func (s *Service) CancelNodeJoinToken(ctx context.Context, p security.Principal, tokenID string) error {
	if err := s.mayMintNodeJoin(ctx, p); err != nil {
		return err
	}
	kid, err := id.ParseKeyID(tokenID)
	if err != nil {
		return ErrNotFound
	}
	tctx := s.tenant(ctx, p.OrgID)
	k, err := s.engine.GetKey(tctx, kid)
	if err != nil {
		if errors.Is(err, store.ErrKeyNotFound) {
			return ErrNotFound
		}
		return ErrUnavailable
	}
	if k.TenantID != p.OrgID {
		return ErrNotFound
	}
	claim, err := s.nodeJoinClaim(k)
	if err != nil {
		return ErrNotFound
	}
	if claim.Scope.EdgeScope == EdgeScopeInstance {
		if ierr := s.mayMintInstanceEdge(ctx, p); ierr != nil {
			return ierr
		}
	}
	// The same versioned revoke a join spends it with, so a join racing
	// the cancel either wins and the cancel finds nothing, or loses.
	if serr := s.spendNodeJoinKey(tctx, k, "cancelled by "+p.UserID); serr != nil {
		if errors.Is(serr, ErrRejected) {
			return ErrNotFound
		}
		return serr
	}
	if s.audit != nil {
		v := view(k)
		now := s.now().UTC()
		v.State, v.RevokedAt = string(key.StateRevoked), &now
		meta := map[string]string{"minted_by": claim.MintedBy, "minted_by_key_id": claim.MintedByKeyID, "edge_scope": claim.Scope.EdgeScope}
		if ma, ok := s.audit.(MetaAuditor); ok {
			ma.RecordWithMeta(ctx, "node.join_token.cancelled", p, v, meta)
		} else {
			s.audit.Record(ctx, "node.join_token.cancelled", p, v)
		}
	}
	return nil
}

func pendingNodeJoinToken(k *key.Key, c *NodeJoinClaim) PendingNodeJoinToken {
	eps := c.Scope.Endpoints
	if eps == nil {
		eps = []nodeproto.Endpoint{}
	}
	t := PendingNodeJoinToken{
		ID: k.ID.String(), Hint: k.Hint, MintedBy: c.MintedBy, MintedByKeyID: c.MintedByKeyID,
		CloudID: c.Scope.CloudID, Labels: c.Scope.Labels, Endpoints: eps, Edge: c.Scope.Edge, EdgeScope: c.Scope.EdgeScope,
		CreatedAt: k.CreatedAt,
	}
	if k.ExpiresAt != nil {
		t.ExpiresAt = *k.ExpiresAt
	}
	return t
}
