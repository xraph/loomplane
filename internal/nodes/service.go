package nodes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
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

// JoinTokens reads and spends node join tokens.
type JoinTokens interface {
	// PeekNodeJoinToken reads what a live token grants without spending it.
	PeekNodeJoinToken(ctx context.Context, raw string) (*apikeys.NodeJoinClaim, error)
	ConsumeNodeJoinToken(ctx context.Context, raw string) (*apikeys.NodeJoinClaim, error)
}

// JoinConflictError is a join refused because its name or its key already belongs
// to a node. Its text tells the operator what to do. Spent says whether the
// join token was used up on the way, in which case they need a new one.
type JoinConflictError struct {
	Spent bool
	msg   string
	cause error
}

func (e *JoinConflictError) Error() string { return e.msg }
func (e *JoinConflictError) Unwrap() error { return e.cause }

func nameConflict(name string, spent bool) *JoinConflictError {
	msg := fmt.Sprintf("the name %q belongs to a live node in this organization: revoke that node, or pick another --name, then run the join again. The join token was not used", name)
	if spent {
		msg = fmt.Sprintf("the name %q was taken by another node while this one joined: revoke that node, or pick another --name, then mint a new join token and join again", name)
	}
	return &JoinConflictError{Spent: spent, msg: msg, cause: ErrNameTaken}
}

func keyConflict(spent bool) *JoinConflictError {
	msg := "this machine's key already belongs to a node Loomplane knows, live or revoked, and a key never joins twice: move identity.key out of the node's state directory so the join makes a fresh one, then run the join again. The join token was not used"
	if spent {
		msg = "this machine's key already belongs to a node Loomplane knows, live or revoked: move identity.key out of the node's state directory so the join makes a fresh one, then mint a new join token and join again"
	}
	return &JoinConflictError{Spent: spent, msg: msg, cause: ErrKeyKnown}
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
	// ErrAdoptionRefused from it means the node joins without a Cloud.
	Adopt func(ctx context.Context, n *Node, claim *apikeys.NodeJoinClaim) error
	// Servers finds the Cloud server a node was adopted as and the
	// connections that reach it, and Placed reads what is deployed there.
	// Phase 2 fills them. Without them a node runs nothing and has nothing
	// to drain.
	Servers NodeServers
	Placed  WorkloadReader
	// CheckAddress checks the public address of a node whose owner just
	// asked for direct ingress, and settles its mode on what it finds.
	// Phase 2 fills it. Update doesn't wait for it, so it runs the check
	// on its own. It may be nil.
	CheckAddress func(n *Node)
	// Log receives failures nobody else sees. Nil means slog.Default().
	Log *slog.Logger
	// clock is what time it is. Nil means time.Now. Tests set it.
	clock func() time.Time
}

func (s *Service) now() time.Time {
	if s.clock != nil {
		return s.clock().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// JoinRequest is what a machine sends to join.
type JoinRequest struct {
	Token     string
	Name      string
	PublicKey []byte
	// Signature is the machine's signature with its new key over the token,
	// Name as sent and PublicKey (nodeproto.SignJoin). It proves the joiner
	// holds the key, so nobody with a token can claim another machine's.
	Signature       []byte
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
	Name               string
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
// token run first, so a node that must update, or can't prove it holds its
// key, does not burn its token. A key that already belongs to a node,
// revoked ones included, and an explicit name a live node holds are refused
// before the token is spent too. A join racing another for the name or key
// is caught again after.
func (s *Service) Join(ctx context.Context, in JoinRequest) (*JoinResult, error) {
	if in.ProtocolVersion > nodeproto.Version {
		return nil, ErrNodeNewer
	}
	if in.ProtocolVersion < nodeproto.Version || !s.versionOK(in.NodeVersion) {
		return nil, ErrUpdateNode
	}
	// What the machine says about itself is stored and sent on every
	// node.updated, so it is bounded first.
	in.NodeVersion, in.Inventory = nodeproto.BoundVersion(in.NodeVersion), in.Inventory.Bounded()
	if len(in.PublicKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: bad public key", ErrInvalid)
	}
	// Before the token is looked at, let alone spent: a key the joiner can't
	// sign with is not theirs to join with.
	if err := nodeproto.VerifyJoin(in.PublicKey, in.Token, in.Name, in.Signature); err != nil {
		return nil, fmt.Errorf("%w: the join isn't signed by the key it carries", ErrInvalid)
	}
	name := strings.TrimSpace(in.Name)
	if name != "" && !dnsLabel.MatchString(name) {
		return nil, fmt.Errorf("%w: name must be a lowercase DNS label of at most 63 characters", ErrInvalid)
	}
	if err := validateLabels(in.Labels); err != nil {
		return nil, err
	}
	fp := nodeproto.Fingerprint(ed25519.PublicKey(in.PublicKey))
	peek, err := s.Tokens.PeekNodeJoinToken(ctx, in.Token)
	if err != nil {
		return nil, err
	}
	if known, kerr := s.Store.KeyKnown(ctx, fp); kerr != nil {
		return nil, kerr
	} else if known {
		return nil, keyConflict(false)
	}
	if name != "" {
		if taken, terr := s.Store.NameTaken(ctx, peek.OrgID, name); terr != nil {
			return nil, terr
		} else if taken {
			return nil, nameConflict(name, false)
		}
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
		KeyFingerprint: fp, IdentityGeneration: 1,
		Status: StatusPending, Endpoints: joinEndpoints(in.Endpoints, claim.Scope.Endpoints), IngressMode: nodeproto.IngressNone,
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
	switch {
	case errors.Is(err, ErrKeyKnown):
		return nil, keyConflict(true)
	case errors.Is(err, ErrNameTaken):
		return nil, nameConflict(n.Name, true)
	case err != nil:
		return nil, fmt.Errorf("%w: %w", ErrMintNewToken, err)
	}
	cloudID := claim.Scope.CloudID
	if s.Adopt != nil {
		err := s.Adopt(ctx, n, claim)
		if errors.Is(err, ErrAdoptionRefused) {
			// The node joins all the same, into no Cloud.
			cloudID, err = "", nil
		}
		if err != nil {
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
	return &JoinResult{NodeID: n.ID, OrgID: n.OrgID, Name: n.Name, IdentityGeneration: n.IdentityGeneration, Endpoints: n.Endpoints, CloudID: cloudID}, nil
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
	list, err := s.withDetails(s.Store.List(ctx, p.OrgID))
	for i := range list {
		s.drainingStatus(ctx, &list[i])
	}
	return list, err
}

// Get returns one node of the org.
func (s *Service) Get(ctx context.Context, p security.Principal, id string) (*Node, error) {
	if err := s.allow(ctx, p, security.NodeRead, false); err != nil {
		return nil, err
	}
	n, err := s.Store.Get(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	s.ingressDetail(n)
	s.drainingStatus(ctx, n)
	return n, nil
}

// JoinedByKey lists the nodes that joined with a token made by the API key.
func (s *Service) JoinedByKey(ctx context.Context, p security.Principal, keyID string) ([]Node, error) {
	if err := s.allow(ctx, p, security.NodeManage, false); err != nil {
		return nil, err
	}
	return s.withDetails(s.Store.ListJoinedByKey(ctx, p.OrgID, keyID))
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
		case nodeproto.IngressNone, nodeproto.IngressDirect, nodeproto.IngressRelay:
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
		if lerr := validateLabels(u.Labels); lerr != nil {
			return nil, lerr
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
	// Ruling R13: send every endpoints update, changed or not. Narrowing
	// is idempotent on the node, and a node that missed one converges.
	if set.Endpoints != nil && s.Notify != nil {
		s.Notify(n.ID, nodeproto.Narrow{Endpoints: n.Endpoints})
	}
	if ingress {
		s.sendIngress(n)
	}
	if narrowed || ingress || n.Name != old.Name || !maps.Equal(n.Labels, old.Labels) {
		s.record(ctx, "node.updated", p, *n, nil)
	}
	// Direct ingress needs an address that answers. One asked for anew, or
	// one without a confirmed address, has it checked.
	if s.CheckAddress != nil && n.IngressMode == nodeproto.IngressDirect && set.IngressMode != nil && (ingress || n.PublicAddressConfirmedAt == nil) {
		s.CheckAddress(n)
	}
	s.ingressDetail(n)
	s.drainingStatus(ctx, n)
	return n, nil
}

// NoPublicAddressDetail is what a node without direct ingress says when no
// public address of its answered the address check.
const NoPublicAddressDetail = "no public address answered on port 80; relay arrives in a later release"

// ingressApplyGrace is how long a node has to answer a SetIngress before
// its detail says it didn't apply the mode.
const ingressApplyGrace = 30 * time.Second

// SetIngress settles a node's address check. Direct records publicAddress
// as confirmed now; none records that no public address answered, and
// takes any earlier address back. The stored mode then goes to the node
// the way Update sends it, and the node's IngressApplied answer is
// recorded by its session. Nothing waits for that answer: a mode the node
// hasn't applied ingressApplyGrace later shows in its IngressDetail on
// read. Relay comes in a later phase, so it is ErrInvalid here.
func (s *Service) SetIngress(ctx context.Context, nodeID, mode, publicAddress string) error {
	now := s.now()
	set := IngressSetting{Mode: mode, SentAt: now}
	switch mode {
	case nodeproto.IngressDirect:
		if net.ParseIP(publicAddress) == nil {
			return fmt.Errorf("%w: direct ingress needs the confirmed public IP address", ErrInvalid)
		}
		set.PublicAddress, set.ConfirmedAt = publicAddress, &now
	case nodeproto.IngressNone:
		if publicAddress != "" {
			return fmt.Errorf("%w: a node without direct ingress has no public address", ErrInvalid)
		}
		set.Detail = NoPublicAddressDetail
	default:
		return fmt.Errorf("%w: an address check sets ingress mode direct or none", ErrInvalid)
	}
	old, err := s.Store.GetByID(ctx, nodeID)
	if err != nil {
		return err
	}
	n, err := s.Store.SetIngress(ctx, nodeID, set)
	if err != nil {
		return err
	}
	s.sendIngress(n)
	if n.IngressMode != old.IngressMode || n.PublicAddress != old.PublicAddress {
		s.record(ctx, "node.updated", security.SystemPrincipal(n.OrgID), *n, map[string]string{"ingress_mode": n.IngressMode, "public_address": n.PublicAddress})
	}
	return nil
}

// RecordIngressDetail records what a later address check found without
// settling anything: the mode, the confirmed address and when the mode was
// sent all stay, and nothing goes to the node. The next settled check
// replaces the detail.
func (s *Service) RecordIngressDetail(ctx context.Context, nodeID, detail string) error {
	return s.Store.SetIngressDetail(ctx, nodeID, detail)
}

// sendIngress tells a live node its stored ingress mode.
func (s *Service) sendIngress(n *Node) {
	if s.Notify != nil {
		s.Notify(n.ID, nodeproto.SetIngress{Mode: n.IngressMode})
	}
}

// ingressDetail fills in, on read, a mode the node hasn't applied within
// ingressApplyGrace of its being sent. It wins over a stored detail.
func (s *Service) ingressDetail(n *Node) {
	if n.Status == StatusRevoked || n.IngressSentAt == nil || n.IngressApplied == n.IngressMode {
		return
	}
	if s.now().Sub(*n.IngressSentAt) >= ingressApplyGrace {
		n.IngressDetail = fmt.Sprintf("the node didn't apply ingress mode %s", n.IngressMode)
	}
}

func (s *Service) withDetails(list []Node, err error) ([]Node, error) {
	if err != nil {
		return nil, err
	}
	for i := range list {
		s.ingressDetail(&list[i])
	}
	return list, nil
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
