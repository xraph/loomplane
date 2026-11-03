package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// Where a node's adoption into its Cloud stands.
const (
	// NodeAdoptionWaiting waits for the node to connect with Docker
	// answering. Every connect, and every inventory change, tries again.
	NodeAdoptionWaiting = "waiting"
	// NodeAdoptionApplied is an adoption applied as its approver. The
	// server operation it started does the rest.
	NodeAdoptionApplied = "applied"
	// NodeAdoptionReview is an adoption an admin has to look at: preflight
	// found something the review would have shown, or the approver can no
	// longer approve it. Reason says what.
	NodeAdoptionReview = "review"
	// NodeAdoptionFailed is an adoption that can never happen, such as one
	// into a Cloud outside the node's organization.
	NodeAdoptionFailed = "failed"
)

// The reasons an adoption stops, as an admin reads them.
const (
	reasonApproverLost  = "the person who approved this can no longer manage Clouds"
	reasonPortsInUse    = "ports 80/443 are in use"
	reasonWorkloads     = "Docker already runs containers on this node; move them off, or adopt it by review"
	reasonForeignCloud  = "the join token names a Cloud that isn't one of this organization's Clouds, so the node joined without one"
	reasonCloudGone     = "the Cloud this node was to join isn't one of this organization's Clouds any more"
	reasonNodeRevoked   = "the node was revoked before it was adopted"
	reasonDockerWaiting = "waiting for the node's Docker to answer"
)

// NodeAdoption is a joined node on its way into the Cloud its join token
// named. The token's minter is the approval: the adoption runs as them,
// and only while they may still manage Clouds.
type NodeAdoption struct {
	NodeID          string    `json:"node_id"`
	OrgID           string    `json:"org_id"`
	CloudID         string    `json:"cloud_id"`
	ApprovedBy      string    `json:"approved_by"`
	ApprovedByKeyID string    `json:"approved_by_key_id"`
	Status          string    `json:"status"`
	Reason          string    `json:"reason"`
	ServerID        string    `json:"server_id"`
	OperationID     string    `json:"operation_id"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// approver is the principal the adoption acts as.
func (a *NodeAdoption) approver() security.Principal {
	return security.Principal{UserID: a.ApprovedBy, OrgID: a.OrgID, KeyID: a.ApprovedByKeyID}
}

// NodeAdoptionStore keeps node adoptions, one per node.
type NodeAdoptionStore interface {
	// CreateNodeAdoption inserts a. A node that has one keeps it, and that
	// is not an error.
	CreateNodeAdoption(ctx context.Context, a *NodeAdoption) error
	// GetNodeAdoption is ErrNotFound for a node without one.
	GetNodeAdoption(ctx context.Context, orgID, nodeID string) (*NodeAdoption, error)
	SaveNodeAdoption(ctx context.Context, a *NodeAdoption) error
}

// NodeConnectionSource lists the connections that reach a node.
// *connections.Service satisfies it.
type NodeConnectionSource interface {
	NodeConnections(ctx context.Context, orgID, nodeID string) ([]*connections.Connection, error)
}

// errNotNodeCloud is a Cloud a node can't be adopted into.
var errNotNodeCloud = errors.New("not a Cloud of the node's organization")

// nodeCloud is the Cloud cloudID of org orgID. Every Cloud holds servers,
// so being one of the org's own Clouds is all a server Cloud needs; any
// other ID, a Cloud of another org included, is errNotNodeCloud.
func (s *Service) nodeCloud(ctx context.Context, orgID, cloudID string) (*cloudprofiles.Profile, error) {
	if orgID == "" || cloudID == "" {
		return nil, errNotNodeCloud
	}
	profile, err := s.Clouds.GetProfile(ctx, orgID, "", cloudID)
	if errors.Is(err, cloudprofiles.ErrNotFound) || errors.Is(err, ErrNotFound) {
		return nil, errNotNodeCloud
	}
	if err != nil {
		return nil, err
	}
	if profile == nil || profile.OrgID != orgID || profile.ID != cloudID {
		return nil, errNotNodeCloud
	}
	return profile, nil
}

func (s *Service) nodeAdoptions() (NodeAdoptionStore, error) {
	st, ok := s.Store.(NodeAdoptionStore)
	if !ok {
		return nil, fmt.Errorf("%w: this store can't keep node adoptions", ErrInvalid)
	}
	return st, nil
}

// RecordNodeAdoption is the nodes service's Adopt hook. It records that the
// node joined with a token scoped to a Cloud, which is the approval, so its
// first connect adopts it. A claim without a Cloud does nothing. A Cloud
// that isn't one of the node's own org's is refused: the node joins
// without one, nothing is ever applied, and the refusal is recorded and
// audited. An error fails the join.
func (s *Service) RecordNodeAdoption(ctx context.Context, n *nodes.Node, claim *apikeys.NodeJoinClaim) error {
	if claim == nil || strings.TrimSpace(claim.Scope.CloudID) == "" {
		return nil
	}
	if n == nil || n.ID == "" || n.OrgID == "" || claim.OrgID != n.OrgID {
		return fmt.Errorf("%w: the node and its join token belong to different organizations", ErrInvalid)
	}
	st, err := s.nodeAdoptions()
	if err != nil {
		return err
	}
	now := s.now().UTC()
	a := &NodeAdoption{NodeID: n.ID, OrgID: n.OrgID, CloudID: claim.Scope.CloudID, ApprovedBy: claim.MintedBy, ApprovedByKeyID: claim.MintedByKeyID, Status: NodeAdoptionWaiting, CreatedAt: now, UpdatedAt: now}
	if _, cloudErr := s.nodeCloud(ctx, n.OrgID, a.CloudID); cloudErr != nil {
		if !errors.Is(cloudErr, errNotNodeCloud) {
			return cloudErr
		}
		// The other org's Cloud ID is kept out of this org's row; the audit
		// event says what the token named.
		a.CloudID, a.Status, a.Reason = "", NodeAdoptionFailed, reasonForeignCloud
		s.warn("A node's join token named a Cloud outside its organization; the node joined without one", fmt.Errorf("node %s of org %s named Cloud %q", n.ID, n.OrgID, claim.Scope.CloudID))
		s.recordNodeAdoption(ctx, "cloud.node_adoption.refused", a, map[string]any{"claimed_cloud_id": claim.Scope.CloudID})
	}
	return st.CreateNodeAdoption(ctx, a)
}

// AdoptOnline is the hub's OnOnline hook, and its OnInventory hook too. A
// node whose adoption waits is adopted into its Cloud as the approver when
// preflight is clean, or left for review with the reason. Nothing that
// isn't waiting is touched, so a second connect never adopts again. A node
// whose Docker isn't answering yet keeps waiting, and the next connect or
// inventory change tries again. One attempt runs at a time per node; a
// second one finds the first holding the node and leaves it to that one.
func (s *Service) AdoptOnline(ctx context.Context, n *nodes.Node) error {
	if n == nil || n.ID == "" {
		return nil
	}
	st, ok := s.Store.(NodeAdoptionStore)
	if !ok {
		return nil
	}
	a, err := st.GetNodeAdoption(ctx, n.OrgID, n.ID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil || a.Status != NodeAdoptionWaiting {
		return err
	}
	err = s.Store.WithOperationLock(ctx, "node-adoption/"+n.ID, func() error {
		return s.adoptNode(ctx, st, n)
	})
	if errors.Is(err, ErrBusy) {
		return nil
	}
	return err
}

// adoptNode runs one attempt while holding the node's adoption lock.
func (s *Service) adoptNode(ctx context.Context, st NodeAdoptionStore, n *nodes.Node) error {
	a, err := st.GetNodeAdoption(ctx, n.OrgID, n.ID)
	if err != nil {
		return err
	}
	if a.Status != NodeAdoptionWaiting {
		return nil
	}
	if n.Status == nodes.StatusRevoked || n.RevokedAt != nil {
		return s.settleNodeAdoption(ctx, st, a, NodeAdoptionFailed, reasonNodeRevoked)
	}
	profile, err := s.nodeCloud(ctx, a.OrgID, a.CloudID)
	if errors.Is(err, errNotNodeCloud) {
		return s.settleNodeAdoption(ctx, st, a, NodeAdoptionFailed, reasonCloudGone)
	}
	if err != nil {
		return err
	}
	// Everything from here runs as the approver, whose live role and API
	// key are checked first and again by every call that follows.
	p := a.approver()
	if _, err = s.authorizeCapacityPrincipal(ctx, p, a.CloudID); err != nil {
		if lostAuthority(err) {
			return s.settleNodeAdoption(ctx, st, a, NodeAdoptionReview, reasonApproverLost)
		}
		return err
	}
	conn, err := s.nodeDocker(ctx, p, n)
	switch {
	case err == nil:
	case lostAuthority(err):
		return s.settleNodeAdoption(ctx, st, a, NodeAdoptionReview, reasonApproverLost)
	case errors.Is(err, nodes.ErrDockerNotAllowed):
		return s.keepWaiting(ctx, st, a, err.Error())
	case errors.Is(err, connections.ErrUnavailable):
		return s.keepWaiting(ctx, st, a, reasonDockerWaiting)
	case errors.Is(err, connections.ErrInvalid), errors.Is(err, connections.ErrExists):
		// The daemon is connected already, or the name is taken: an admin
		// decides.
		return s.settleNodeAdoption(ctx, st, a, NodeAdoptionReview, err.Error())
	default:
		return err
	}
	findings, vacant, err := s.nodePreflight(ctx, n, a, conn)
	if err != nil {
		return s.keepWaiting(ctx, st, a, err.Error())
	}
	in := ServerInput{Kind: "adopt", ConnectionID: conn.ID, Name: n.Name, CloudRevision: profile.Revision, ConfirmAdoption: true}
	if len(findings) > 0 {
		// A busy host can't even be previewed. Anything else leaves the
		// preview open in the Cloud's Changes tab for an admin to apply.
		if vacant {
			if o, previewErr := s.PreviewServer(ctx, p, a.CloudID, in); previewErr == nil {
				a.OperationID, a.ServerID = o.ID, o.ServerID
			} else {
				s.warn("Couldn't leave a node's adoption open for review", previewErr)
			}
		}
		return s.settleNodeAdoption(ctx, st, a, NodeAdoptionReview, strings.Join(findings, "; "))
	}
	o, err := s.PreviewServer(ctx, p, a.CloudID, in)
	if err != nil {
		if reason, finding := vacancyFinding(err); finding {
			return s.settleNodeAdoption(ctx, st, a, NodeAdoptionReview, reason)
		}
		if lostAuthority(err) {
			return s.settleNodeAdoption(ctx, st, a, NodeAdoptionReview, reasonApproverLost)
		}
		return s.keepWaiting(ctx, st, a, err.Error())
	}
	a.OperationID, a.ServerID = o.ID, o.ServerID
	if _, err = s.ApplyServerOperation(ctx, p, a.CloudID, o.ID, ApplyInput{Review: o.Review}); err != nil {
		// The preview is still open for an admin.
		reason := "Loomplane couldn't apply the adoption: " + err.Error()
		if lostAuthority(err) {
			reason = reasonApproverLost
		}
		return s.settleNodeAdoption(ctx, st, a, NodeAdoptionReview, reason)
	}
	return s.settleNodeAdoption(ctx, st, a, NodeAdoptionApplied, "")
}

// nodeDocker is the node's server connection: the one an earlier attempt
// wrote, or a new one written as p.
func (s *Service) nodeDocker(ctx context.Context, p security.Principal, n *nodes.Node) (*connections.Connection, error) {
	if s.Nodes == nil || s.NodeConnections == nil {
		return nil, fmt.Errorf("%w: node adoption isn't wired", ErrInvalid)
	}
	held, err := s.NodeConnections.NodeConnections(ctx, n.OrgID, n.ID)
	if err != nil {
		return nil, err
	}
	for _, c := range held {
		if c.Cloud == connections.Server && c.Status == connections.StatusReady {
			return c, nil
		}
	}
	return s.Nodes.AdoptDocker(ctx, p, n, nodeDockerSettings(n))
}

// nodeDockerSettings are the server settings a node's own report gives:
// the socket its runtime listens on.
func nodeDockerSettings(n *nodes.Node) map[string]string {
	settings := map[string]string{}
	switch {
	case n.Inventory.Docker != nil && n.Inventory.Docker.Socket != "":
		settings["docker_socket"] = n.Inventory.Docker.Socket
	case n.Inventory.Podman != nil && n.Inventory.Podman.Socket != "":
		settings["docker_socket"] = n.Inventory.Podman.Socket
	}
	return settings
}

// nodePreflight is what the adoption review would show: the host's
// vacancy, whether the Cloud's pools can use its architecture, and whether
// the server's name is free. vacant says the host itself is fine, so a
// preview can still be made. An error is one that may pass, such as the
// node going offline mid-check.
func (s *Service) nodePreflight(ctx context.Context, n *nodes.Node, a *NodeAdoption, conn *connections.Connection) (findings []string, vacant bool, err error) {
	vacant = true
	_, raw, err := s.Connections.Credential(ctx, a.OrgID, conn.ID)
	if err != nil {
		return nil, false, err
	}
	if _, inspectErr := s.Runtime.Inspect(ctx, raw, conn.Details, true); inspectErr != nil {
		reason, finding := vacancyFinding(inspectErr)
		if !finding {
			return nil, false, inspectErr
		}
		findings, vacant = append(findings, reason), false
	}
	servers, err := s.Store.ListServers(ctx, a.OrgID, a.CloudID)
	if err != nil {
		return nil, false, err
	}
	if finding, poolErr := s.poolArchitecture(ctx, a, servers, conn.Details["architecture"]); poolErr != nil {
		return nil, false, poolErr
	} else if finding != "" {
		findings = append(findings, finding)
	}
	for _, v := range servers {
		if v.Name == n.Name && v.Status != "deleted" && v.Status != "disconnected" {
			findings = append(findings, fmt.Sprintf("a server named %s exists", n.Name))
			break
		}
	}
	return findings, vacant, nil
}

// vacancyFinding turns CheckVacant's refusals into an admin's words.
func vacancyFinding(err error) (string, bool) {
	switch {
	case errors.Is(err, server.ErrIngressInUse):
		return reasonPortsInUse, true
	case errors.Is(err, server.ErrWorkloadsPresent):
		return reasonWorkloads, true
	}
	return "", false
}

// poolArchitecture says when none of the Cloud's pools can use arch. A
// pool's architectures are its members'; a pool whose members' are unknown
// could take anything. A Cloud without pools has nothing to refuse.
func (s *Service) poolArchitecture(ctx context.Context, a *NodeAdoption, servers []*Server, arch string) (string, error) {
	st, ok := s.Store.(PoolStore)
	if !ok || arch == "" {
		return "", nil
	}
	pools, err := st.ListPools(ctx, a.OrgID, a.CloudID)
	if err != nil {
		return "", err
	}
	byID := map[string]*Server{}
	for _, v := range servers {
		byID[v.ID] = v
	}
	slices.SortFunc(pools, func(x, y *Pool) int { return strings.Compare(x.Name, y.Name) })
	first := ""
	for _, pool := range pools {
		var archs []string
		for _, m := range pool.Members {
			if v := byID[m.ServerID]; v != nil && v.Health.Architecture != "" && !slices.Contains(archs, v.Health.Architecture) {
				archs = append(archs, v.Health.Architecture)
			}
		}
		if len(archs) == 0 || slices.Contains(archs, arch) {
			return "", nil
		}
		if first == "" {
			slices.Sort(archs)
			first = fmt.Sprintf("%s doesn't match pool %s (%s)", arch, pool.Name, strings.Join(archs, ", "))
		}
	}
	return first, nil
}

// lostAuthority says err means the approver may not do this any more, as
// opposed to a check that couldn't run.
func lostAuthority(err error) bool {
	return errors.Is(err, security.ErrForbidden) || errors.Is(err, security.ErrNotMember) || errors.Is(err, security.ErrUnauthenticated) || errors.Is(err, apikeys.ErrRejected)
}

// keepWaiting leaves the adoption waiting and says why, for the next
// attempt to pick up.
func (s *Service) keepWaiting(ctx context.Context, st NodeAdoptionStore, a *NodeAdoption, reason string) error {
	if a.Reason == reason {
		return nil
	}
	a.Reason, a.UpdatedAt = reason, s.now().UTC()
	return st.SaveNodeAdoption(ctx, a)
}

// settleNodeAdoption ends the waiting: applied, left for review, or failed.
func (s *Service) settleNodeAdoption(ctx context.Context, st NodeAdoptionStore, a *NodeAdoption, status, reason string) error {
	a.Status, a.Reason, a.UpdatedAt = status, reason, s.now().UTC()
	if err := st.SaveNodeAdoption(ctx, a); err != nil {
		return err
	}
	s.recordNodeAdoption(ctx, "cloud.node_adoption."+status, a, nil)
	return nil
}

func (s *Service) recordNodeAdoption(ctx context.Context, action string, a *NodeAdoption, extra map[string]any) {
	if s.Audit == nil {
		return
	}
	attributes := map[string]any{"node_id": a.NodeID, "cloud_id": a.CloudID, "approved_by": a.ApprovedBy, "approved_by_key_id": a.ApprovedByKeyID, "status": a.Status, "reason": a.Reason, "server_id": a.ServerID, "operation_id": a.OperationID}
	for k, v := range extra {
		attributes[k] = v
	}
	severity := "info"
	if a.Status != NodeAdoptionApplied {
		severity = "warning"
	}
	s.Audit.Record(ctx, projects.AuditEvent{Action: action, Severity: severity, Principal: security.SystemPrincipal(a.OrgID), Attributes: attributes})
}

var _ NodeAdoptionStore = (*PgStore)(nil)

const nodeAdoptionColumns = `node_id,org_id,cloud_id,approved_by,approved_by_key_id,status,reason,server_id,operation_id,created_at,updated_at`

// CreateNodeAdoption inserts a, leaving a node's existing row alone.
func (s *PgStore) CreateNodeAdoption(ctx context.Context, a *NodeAdoption) error {
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_node_adoptions(`+nodeAdoptionColumns+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (node_id) DO NOTHING`,
		a.NodeID, a.OrgID, a.CloudID, a.ApprovedBy, a.ApprovedByKeyID, a.Status, a.Reason, a.ServerID, a.OperationID, a.CreatedAt, a.UpdatedAt)
	return storeError(err)
}

// GetNodeAdoption reads the node's adoption in org.
func (s *PgStore) GetNodeAdoption(ctx context.Context, orgID, nodeID string) (*NodeAdoption, error) {
	var a NodeAdoption
	err := s.db.QueryRow(ctx, `SELECT `+nodeAdoptionColumns+` FROM loomplane_node_adoptions WHERE org_id=$1 AND node_id=$2`, orgID, nodeID).
		Scan(&a.NodeID, &a.OrgID, &a.CloudID, &a.ApprovedBy, &a.ApprovedByKeyID, &a.Status, &a.Reason, &a.ServerID, &a.OperationID, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, storeError(err)
	}
	a.CreatedAt, a.UpdatedAt = a.CreatedAt.UTC(), a.UpdatedAt.UTC()
	return &a, nil
}

// SaveNodeAdoption writes where the adoption stands. Who approved it, and
// into which Cloud, never change.
func (s *PgStore) SaveNodeAdoption(ctx context.Context, a *NodeAdoption) error {
	tag, err := s.db.Exec(ctx, `UPDATE loomplane_node_adoptions SET status=$3,reason=$4,server_id=$5,operation_id=$6,updated_at=$7 WHERE org_id=$1 AND node_id=$2`,
		a.OrgID, a.NodeID, a.Status, a.Reason, a.ServerID, a.OperationID, a.UpdatedAt)
	if err != nil {
		return storeError(err)
	}
	n, err := tag.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
