package nodes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
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
	// ErrNodeNewer is returned to a node that speaks a newer protocol.
	ErrNodeNewer = errors.New("this node is newer than Loomplane; update Loomplane first")
	// ErrInvalid is returned for a name, label or setting that is not allowed.
	ErrInvalid = errors.New("nodes: invalid value")
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
	// Log receives failures nobody else sees. Nil means slog.Default().
	Log *slog.Logger
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
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
	if in.ProtocolVersion > nodeproto.Version {
		return nil, ErrNodeNewer
	}
	if in.ProtocolVersion < nodeproto.Version || !s.versionOK(in.NodeVersion) {
		return nil, ErrUpdateNode
	}
	if len(in.PublicKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: bad public key", ErrInvalid)
	}
	name := strings.TrimSpace(in.Name)
	if name != "" && !dnsLabel.MatchString(name) {
		return nil, fmt.Errorf("%w: name must be a lowercase DNS label of at most 63 characters", ErrInvalid)
	}
	if err := validateLabels(in.Labels); err != nil {
		return nil, err
	}
	claim, err := s.Tokens.ConsumeNodeJoinToken(ctx, in.Token)
	if err != nil {
		return nil, err
	}
	id := "node_" + uuid.NewString()
	fromHost := false
	if name == "" {
		name = hostLabel(in.Inventory.Hostname)
		fromHost = name != ""
	}
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
	// The token is spent, so every failure from here on asks for a new one.
	err = s.Store.Create(ctx, n)
	if fromHost && errors.Is(err, ErrNameTaken) {
		// Two machines with one hostname: tell them apart once by id.
		n.Name = suffixed(name, id)
		err = s.Store.Create(ctx, n)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMintNewToken, err)
	}
	if s.Adopt != nil {
		if err := s.Adopt(ctx, n, claim); err != nil {
			// Free the name even when the caller has gone, and say so if
			// that fails: a pending node would hold it otherwise.
			rb := security.Principal{OrgID: n.OrgID, UserID: joinRollback}
			if rerr := s.revoke(context.WithoutCancel(ctx), n, joinRollback, rb, "node.revoked"); rerr != nil {
				s.log().Error("nodes: rolling back a join whose adoption failed", "node", n.ID, "org", n.OrgID, "error", rerr)
			}
			return nil, fmt.Errorf("%w: adopting into the cloud account: %w", ErrMintNewToken, err)
		}
	}
	s.record(ctx, "node.joined", security.ActorPrincipal(claim.OrgID, claim.MintedBy), *n, map[string]string{"role": role})
	return &JoinResult{NodeID: n.ID, OrgID: n.OrgID, IdentityGeneration: n.IdentityGeneration, Endpoints: n.Endpoints, CloudID: claim.Scope.CloudID}, nil
}

const joinRollback = "system:join-rollback"

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// suffixed is name with "-" and the first four characters of the id's uuid,
// trimmed so the result is still a DNS label.
func suffixed(name, id string) string {
	tag := strings.ReplaceAll(strings.TrimPrefix(id, "node_"), "-", "")[:4]
	if len(name) > 63-5 {
		name = strings.TrimRight(name[:63-5], "-")
	}
	return name + "-" + tag
}

// hostLabel turns a hostname into a DNS label, or "" when nothing is left.
func hostLabel(h string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(h)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := b.String()
	if len(out) > 63 {
		out = out[:63]
	}
	return strings.Trim(out, "-")
}

func validateLabels(l map[string]string) error {
	if len(l) > 32 {
		return fmt.Errorf("%w: at most 32 labels", ErrInvalid)
	}
	for k, v := range l {
		if k == "" || len(k) > 63 || len(v) > 255 {
			return fmt.Errorf("%w: label keys are 1 to 63 characters and values at most 255", ErrInvalid)
		}
	}
	return nil
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

// isDev reports a version that carries no number to compare.
func isDev(v string) bool { return v == "" || v == "dev" || strings.HasSuffix(v, "-dev") }

// versionOK allows an equal, newer or exactly one-minor-behind node on the
// same major. A dev build passes on either side. A node version that cannot
// be parsed is refused; an unparsable service version passes.
func (s *Service) versionOK(node string) bool {
	if isDev(node) || isDev(s.Version) {
		return true
	}
	nm, nn, ok := parseVersion(node)
	if !ok {
		return false
	}
	sm, sn, ok := parseVersion(s.Version)
	if !ok {
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
// ingress mode. A nil field stays as it is; a non-nil empty slice or map is a
// real change (narrow to nothing, clear the labels). Only the fields set are
// written, so a stale read cannot undo a concurrent status change or narrow.
// A live node hears every endpoints update and any ingress change at once.
func (s *Service) Update(ctx context.Context, p security.Principal, id string, u Update) (*Node, error) {
	if err := s.allow(ctx, p, security.NodeManage, true); err != nil {
		return nil, err
	}
	old, err := s.Store.Get(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if old.Status == StatusRevoked {
		return nil, ErrNotFound
	}
	set := SettingsUpdate{Name: u.Name, IngressMode: u.IngressMode}
	if u.Endpoints != nil {
		set.Endpoints = []nodeproto.Endpoint{}
		for _, e := range u.Endpoints {
			if !slices.Contains(old.Endpoints, e) {
				return nil, ErrWidenRemotely
			}
			if !slices.Contains(set.Endpoints, e) {
				set.Endpoints = append(set.Endpoints, e)
			}
		}
	}
	if u.IngressMode != nil {
		switch *u.IngressMode {
		case "none", "direct", "relay":
		default:
			return nil, fmt.Errorf("%w: ingress mode must be none, direct or relay", ErrInvalid)
		}
	}
	if u.Name != nil {
		name := strings.TrimSpace(*u.Name)
		if !dnsLabel.MatchString(name) {
			return nil, fmt.Errorf("%w: name must be a lowercase DNS label of at most 63 characters", ErrInvalid)
		}
		set.Name = &name
	}
	if u.Labels != nil {
		if err := validateLabels(u.Labels); err != nil {
			return nil, err
		}
		set.Labels = maps.Clone(u.Labels)
		if set.Labels == nil {
			set.Labels = map[string]string{}
		}
	}
	n, err := s.Store.UpdateSettings(ctx, p.OrgID, id, set)
	if err != nil {
		return nil, err
	}
	narrowed := set.Endpoints != nil && !slices.Equal(n.Endpoints, old.Endpoints)
	ingress := set.IngressMode != nil && n.IngressMode != old.IngressMode
	if s.Notify != nil {
		// Ruling R13: send every endpoints update, changed or not. Narrowing
		// is idempotent on the node, and a node that missed one converges.
		if set.Endpoints != nil {
			s.Notify(n.ID, nodeproto.Narrow{Endpoints: n.Endpoints})
		}
		if ingress {
			s.Notify(n.ID, nodeproto.SetIngress{Mode: n.IngressMode})
		}
	}
	if narrowed || ingress || n.Name != old.Name || !maps.Equal(n.Labels, old.Labels) {
		s.record(ctx, "node.updated", p, *n, nil)
	}
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
		// Say it again, so a message that was dropped can be retried.
		if s.Notify != nil {
			s.Notify(n.ID, nodeproto.Revoked{})
		}
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
