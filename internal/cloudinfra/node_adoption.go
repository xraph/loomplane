package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

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
	reasonKeyRevoked    = "the API key that approved this was revoked or expired"
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
	NodeID string `json:"node_id"`
	// NodeName is the node's name, filled in by listings.
	NodeName   string `json:"node_name"`
	OrgID      string `json:"org_id"`
	CloudID    string `json:"cloud_id"`
	ApprovedBy string `json:"approved_by"`
	// ApprovedByKeyID is the API key the token was minted with, if any. It
	// is never shown.
	ApprovedByKeyID string `json:"-"`
	// BuildWorkers is the join scope's build_workers setting, copied into
	// the node's server connection.
	BuildWorkers string    `json:"-"`
	Status       string    `json:"status"`
	Reason       string    `json:"reason"`
	ServerID     string    `json:"server_id"`
	OperationID  string    `json:"operation_id"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
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
	// ListNodeAdoptions lists the adoptions into a Cloud, with node names.
	ListNodeAdoptions(ctx context.Context, orgID, cloudID string) ([]*NodeAdoption, error)
}

// ListNodeAdoptions lists the nodes joined into a Cloud by their join
// tokens, and where each one stands, for whoever may read the Cloud's
// servers. An adoption refused for naming another org's Cloud belongs to
// no Cloud and isn't listed.
func (s *Service) ListNodeAdoptions(ctx context.Context, p security.Principal, cloud string) ([]*NodeAdoption, error) {
	if _, err := s.authorize(ctx, p, cloud); err != nil {
		return nil, err
	}
	st, err := s.nodeAdoptions()
	if err != nil {
		return nil, err
	}
	return st.ListNodeAdoptions(ctx, p.OrgID, cloud)
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
// that isn't one of the node's own org's is refused: the refusal is
// recorded and audited, nothing is ever applied, and the call returns
// nodes.ErrAdoptionRefused so the node joins without a Cloud. Any other
// error fails the join.
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
	// node join --builder dedicated mints build_workers into the scope. The
	// minter's scope decides it, never the joining machine's own labels.
	workers := claim.Scope.Labels["build_workers"]
	if workers != "" && workers != "dedicated" {
		return fmt.Errorf("%w: a join token's build_workers can only be dedicated", ErrInvalid)
	}
	now := s.now().UTC()
	a := &NodeAdoption{NodeID: n.ID, OrgID: n.OrgID, CloudID: claim.Scope.CloudID, ApprovedBy: claim.MintedBy, ApprovedByKeyID: claim.MintedByKeyID, BuildWorkers: workers, Status: NodeAdoptionWaiting, CreatedAt: now, UpdatedAt: now}
	if _, cloudErr := s.nodeCloud(ctx, n.OrgID, a.CloudID); cloudErr != nil {
		if !errors.Is(cloudErr, errNotNodeCloud) {
			return cloudErr
		}
		// The other org's Cloud ID is kept out of this org's row; the audit
		// event says what the token named.
		a.CloudID, a.Status, a.Reason = "", NodeAdoptionFailed, reasonForeignCloud
		s.warn("A node's join token named a Cloud outside its organization; the node joined without one", fmt.Errorf("node %s of org %s named Cloud %q", n.ID, n.OrgID, claim.Scope.CloudID))
		s.recordNodeAdoption(ctx, "cloud.node_adoption.refused", a, map[string]any{"claimed_cloud_id": claim.Scope.CloudID})
		if createErr := st.CreateNodeAdoption(ctx, a); createErr != nil {
			return createErr
		}
		return nodes.ErrAdoptionRefused
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
	if err != nil {
		return err
	}
	// The adoption this call applied goes on to check the node's address.
	// What that finds, or fails to, never changes the adoption.
	if applied, getErr := st.GetNodeAdoption(ctx, n.OrgID, n.ID); getErr == nil && applied.Status == NodeAdoptionApplied && s.NodeIngress != nil {
		s.confirmAdoptedNode(ctx, n, applied)
	}
	return nil
}

// confirmAdoptedNode checks the address of a node it just adopted, once
// the adopt operation has made its server ready: the operation checks
// that ports 80 and 443 are free, so the proxy can't start before it. It
// runs on its own deadline, past the hook's, under the node's address
// lock, so no other check or follow of the node runs beside it. A failure
// is reported, and the node keeps ingress mode none until a later check:
// a reconcile, the node coming back, or its owner asking for direct.
func (s *Service) confirmAdoptedNode(ctx context.Context, n *nodes.Node, a *NodeAdoption) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), adoptedNodeConfirmWait)
	defer cancel()
	_, err := s.waitServerReady(ctx, a)
	if err == nil {
		err = s.followNode(ctx, n, followMode{wait: true, asked: true})
	}
	if err != nil {
		s.warn("Couldn't check an adopted node's public address; it runs without direct ingress for now", fmt.Errorf("node %s: %w", n.ID, err))
	}
}

// adoptAccepted follows an accepted adopt operation onto a node's
// connection that the node's own adoption didn't apply: an admin's, from
// the Cloud's Changes tab or the servers page, or a retry of one that
// failed. Once the server is ready, the node's adoption, if it names the
// operation's Cloud, is marked applied with the real server and
// operation, and the node's address is checked. The adoption's own
// attempt, which saved the operation on its waiting row before applying
// it, settles and checks for itself. It all runs in the background, on its
// own deadline, so the apply doesn't wait for the server.
func (s *Service) adoptAccepted(ctx context.Context, o *Operation) {
	if o == nil || o.Input.Kind != "adopt" || s.Connections == nil {
		return
	}
	connectionID := o.ConnectionID
	if connectionID == "" {
		connectionID = o.Input.ConnectionID
	}
	conn, _, err := s.Connections.Credential(ctx, o.OrgID, connectionID)
	if err != nil || conn.Method != connections.MethodNode || conn.Details["node_id"] == "" {
		return
	}
	nodeID := conn.Details["node_id"]
	st, keeps := s.Store.(NodeAdoptionStore)
	if keeps {
		if a, getErr := st.GetNodeAdoption(ctx, o.OrgID, nodeID); getErr == nil && a.Status == NodeAdoptionWaiting && a.OperationID == o.ID {
			return
		}
	}
	base := context.WithoutCancel(ctx)
	s.background(func() {
		ctx, cancel := context.WithTimeout(base, adoptedNodeConfirmWait)
		defer cancel()
		v, err := s.waitServerReady(ctx, &NodeAdoption{OrgID: o.OrgID, CloudID: o.CloudID, ServerID: o.ServerID, OperationID: o.ID})
		if err != nil {
			return
		}
		// The connection names its node; the node must be the operation's
		// organization's before anything of it is settled or checked.
		var n *nodes.Node
		if s.NodeRecords != nil {
			if n, err = s.NodeRecords.GetByID(ctx, nodeID); err != nil {
				s.warn("Couldn't read an adopted node; its adoption and address wait for the next check", fmt.Errorf("node %s: %w", nodeID, err))
				return
			}
			if n.OrgID != o.OrgID {
				s.warn("An adopt operation's connection names a node of another organization; nothing was settled", fmt.Errorf("node %s, operation %s", nodeID, o.ID))
				return
			}
		}
		if keeps {
			if settleErr := s.settleManualAdoption(ctx, st, nodeID, o, v); settleErr != nil {
				s.warn("Couldn't mark a node's adoption applied; its server is ready", fmt.Errorf("node %s: %w", nodeID, settleErr))
			}
		}
		if s.NodeIngress == nil || n == nil {
			return
		}
		if err = s.followNode(ctx, n, followMode{wait: true, asked: true}); err != nil {
			s.warn("Couldn't check an adopted node's public address; it runs without direct ingress for now", fmt.Errorf("node %s: %w", nodeID, err))
		}
	})
}

// settleManualAdoption marks the node's adoption applied by o, which made
// server v, unless it already is or it names another Cloud. A node with
// no adoption has nothing to settle.
func (s *Service) settleManualAdoption(ctx context.Context, st NodeAdoptionStore, nodeID string, o *Operation, v *Server) error {
	err := s.Store.WithOperationLock(ctx, "node-adoption/"+nodeID, func() error {
		a, err := st.GetNodeAdoption(ctx, o.OrgID, nodeID)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if a.CloudID != o.CloudID || a.Status == NodeAdoptionApplied && a.ServerID == v.ID && a.OperationID == o.ID {
			return nil
		}
		a.ServerID, a.OperationID = v.ID, o.ID
		return s.settleNodeAdoption(ctx, st, a, NodeAdoptionApplied, "")
	})
	// An attempt holding the adoption settles it itself.
	if errors.Is(err, ErrBusy) {
		return nil
	}
	return err
}

// waitServerReady waits for the adoption's server to be ready. An adopt
// operation that failed or was cancelled ends the wait.
func (s *Service) waitServerReady(ctx context.Context, a *NodeAdoption) (*Server, error) {
	for {
		v, err := s.Store.GetServer(ctx, a.OrgID, a.CloudID, a.ServerID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		if v != nil && v.Status == "ready" {
			return v, nil
		}
		if o, opErr := s.Store.GetOperation(ctx, a.OrgID, a.CloudID, a.OperationID); opErr == nil && (o.Status == "failed" || o.Status == "cancelled") {
			return nil, fmt.Errorf("%w: the adopt operation %s", ErrConflict, o.Status)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: the adopted server wasn't ready in time", ErrPending)
		case <-time.After(s.serverReadyPoll):
		}
	}
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
	// An earlier attempt saved its operation before applying it. If the
	// apply went through, that attempt just didn't live to record it.
	pending, err := s.savedAdoptOperation(ctx, st, a)
	if err != nil {
		return err
	}
	if pending != nil && pending.Status != "preview" {
		status, reason := savedOperationOutcome(pending)
		return s.settleNodeAdoption(ctx, st, a, status, reason)
	}
	// Everything from here runs as the approver, whose live role and API
	// key are checked first and again by every call that follows.
	p := a.approver()
	if _, err = s.authorizeCapacityPrincipal(ctx, p, a.CloudID); err != nil {
		if reason, lost := authorityLost(err); lost {
			return s.settleNodeAdoption(ctx, st, a, NodeAdoptionReview, reason)
		}
		return err
	}
	if pending != nil {
		// Its preflight was clean when it was previewed.
		return s.applyNodeAdoption(ctx, st, a, p, pending)
	}
	conn, err := s.nodeDocker(ctx, p, n, a)
	if reason, lost := authorityLost(err); lost {
		return s.settleNodeAdoption(ctx, st, a, NodeAdoptionReview, reason)
	}
	switch {
	case err == nil:
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
		if reason, lost := authorityLost(err); lost {
			return s.settleNodeAdoption(ctx, st, a, NodeAdoptionReview, reason)
		}
		return s.keepWaiting(ctx, st, a, err.Error())
	}
	// The operation goes on the row before it's applied, so an attempt cut
	// off after the apply commits leaves the next one something to read.
	a.OperationID, a.ServerID, a.UpdatedAt = o.ID, o.ServerID, s.now().UTC()
	if saveErr := st.SaveNodeAdoption(context.WithoutCancel(ctx), a); saveErr != nil {
		return saveErr
	}
	return s.applyNodeAdoption(ctx, st, a, p, o)
}

// savedAdoptOperation is the operation an earlier attempt saved on the
// row, or nil. A preview that expired, or one that's gone, is forgotten,
// and the attempt starts over.
func (s *Service) savedAdoptOperation(ctx context.Context, st NodeAdoptionStore, a *NodeAdoption) (*Operation, error) {
	if a.OperationID == "" {
		return nil, nil
	}
	o, err := s.Store.GetOperation(ctx, a.OrgID, a.CloudID, a.OperationID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if err == nil && (o.Status != "preview" || s.now().Before(o.ExpiresAt)) {
		return o, nil
	}
	a.OperationID, a.ServerID, a.UpdatedAt = "", "", s.now().UTC()
	return nil, st.SaveNodeAdoption(context.WithoutCancel(ctx), a)
}

// savedOperationOutcome is where an adoption stands once its saved
// operation was accepted. Accepted work that is queued, running, waiting on
// the host or done means the adoption took. One that failed or was
// cancelled goes to an admin with the operation kept on the row: a failed
// one can be retried from the Cloud, and a cancelled one was a person's
// call, which starting over would undo. A status this code doesn't know is
// never taken for success.
func savedOperationOutcome(o *Operation) (status, reason string) {
	switch o.Status {
	case "pending", "running", "waiting", "succeeded":
		return NodeAdoptionApplied, ""
	case "failed":
		reason = "the adopt operation failed"
		if o.Message != "" {
			reason += ": " + o.Message
		}
		return NodeAdoptionReview, reason
	case "cancelled":
		return NodeAdoptionReview, "the adopt operation was cancelled"
	}
	return NodeAdoptionReview, fmt.Sprintf("the adopt operation is %q, which Loomplane doesn't recognize", o.Status)
}

// applyNodeAdoption applies the adoption's preview as its approver.
func (s *Service) applyNodeAdoption(ctx context.Context, st NodeAdoptionStore, a *NodeAdoption, p security.Principal, o *Operation) error {
	if _, err := s.ApplyServerOperation(ctx, p, a.CloudID, o.ID, ApplyInput{Review: o.Review}); err != nil {
		// The preview is still open for an admin.
		reason, lost := authorityLost(err)
		if !lost {
			reason = "Loomplane couldn't apply the adoption: " + err.Error()
		}
		return s.settleNodeAdoption(ctx, st, a, NodeAdoptionReview, reason)
	}
	return s.settleNodeAdoption(ctx, st, a, NodeAdoptionApplied, "")
}

// nodeDocker is the node's server connection: the one an earlier attempt
// wrote, or a new one written as p.
func (s *Service) nodeDocker(ctx context.Context, p security.Principal, n *nodes.Node, a *NodeAdoption) (*connections.Connection, error) {
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
	return s.Nodes.AdoptDocker(ctx, p, n, nodeDockerSettings(n, a))
}

// nodeDockerSettings are the server settings for a node's Docker: the
// socket its own report names, and how the join scope said it builds.
func nodeDockerSettings(n *nodes.Node, a *NodeAdoption) map[string]string {
	settings := map[string]string{}
	if a != nil && a.BuildWorkers != "" {
		settings["build_workers"] = a.BuildWorkers
	}
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

// authorityLost says err means the approver may not do this any more, as
// opposed to a check that couldn't run, and why.
func authorityLost(err error) (string, bool) {
	switch {
	case err == nil:
		return "", false
	case errors.Is(err, apikeys.ErrRejected):
		return reasonKeyRevoked, true
	case errors.Is(err, security.ErrForbidden), errors.Is(err, security.ErrNotMember), errors.Is(err, security.ErrUnauthenticated):
		return reasonApproverLost, true
	}
	return "", false
}

// keepWaiting leaves the adoption waiting and says why, for the next
// attempt to pick up.
func (s *Service) keepWaiting(ctx context.Context, st NodeAdoptionStore, a *NodeAdoption, reason string) error {
	if a.Reason == reason {
		return nil
	}
	a.Reason, a.UpdatedAt = reason, s.now().UTC()
	return st.SaveNodeAdoption(context.WithoutCancel(ctx), a)
}

// settleNodeAdoption ends the waiting: applied, left for review, or failed.
func (s *Service) settleNodeAdoption(ctx context.Context, st NodeAdoptionStore, a *NodeAdoption, status, reason string) error {
	a.Status, a.Reason, a.UpdatedAt = status, reason, s.now().UTC()
	// What already happened is recorded even when the hook's time is up.
	ctx = context.WithoutCancel(ctx)
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
	// The effects ran by themselves, on the authority of whoever minted the
	// join token.
	actor := security.SystemPrincipal(a.OrgID)
	if a.ApprovedBy != "" {
		actor = security.ActorPrincipal(a.OrgID, a.ApprovedBy)
	}
	attributes["automatic"], attributes["via"] = true, "node_join_token"
	s.Audit.Record(ctx, projects.AuditEvent{Action: action, Severity: severity, Principal: actor, Attributes: attributes})
}

var _ NodeAdoptionStore = (*PgStore)(nil)

const nodeAdoptionColumns = `node_id,org_id,cloud_id,approved_by,approved_by_key_id,build_workers,status,reason,server_id,operation_id,created_at,updated_at`

// CreateNodeAdoption inserts a, leaving a node's existing row alone.
func (s *PgStore) CreateNodeAdoption(ctx context.Context, a *NodeAdoption) error {
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_node_adoptions(`+nodeAdoptionColumns+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT (node_id) DO NOTHING`,
		a.NodeID, a.OrgID, a.CloudID, a.ApprovedBy, a.ApprovedByKeyID, a.BuildWorkers, a.Status, a.Reason, a.ServerID, a.OperationID, a.CreatedAt, a.UpdatedAt)
	return storeError(err)
}

// GetNodeAdoption reads the node's adoption in org.
func (s *PgStore) GetNodeAdoption(ctx context.Context, orgID, nodeID string) (*NodeAdoption, error) {
	a, err := scanNodeAdoption(s.db.QueryRow(ctx, `SELECT `+nodeAdoptionColumns+`,'' FROM loomplane_node_adoptions WHERE org_id=$1 AND node_id=$2`, orgID, nodeID))
	if err != nil {
		return nil, storeError(err)
	}
	return a, nil
}

// ListNodeAdoptions lists the adoptions into a Cloud with their nodes'
// names, newest first.
func (s *PgStore) ListNodeAdoptions(ctx context.Context, orgID, cloudID string) ([]*NodeAdoption, error) {
	rows, err := s.db.Query(ctx, `SELECT a.node_id,a.org_id,a.cloud_id,a.approved_by,a.approved_by_key_id,a.build_workers,a.status,a.reason,a.server_id,a.operation_id,a.created_at,a.updated_at,n.name
FROM loomplane_node_adoptions a JOIN loomplane_nodes n ON n.id=a.node_id AND n.org_id=a.org_id
WHERE a.org_id=$1 AND a.cloud_id=$2 ORDER BY a.created_at DESC, a.node_id`, orgID, cloudID)
	if err != nil {
		return nil, storeError(err)
	}
	defer rows.Close()
	out := []*NodeAdoption{}
	for rows.Next() {
		a, scanErr := scanNodeAdoption(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func scanNodeAdoption(row pgx.Row) (*NodeAdoption, error) {
	var a NodeAdoption
	if err := row.Scan(&a.NodeID, &a.OrgID, &a.CloudID, &a.ApprovedBy, &a.ApprovedByKeyID, &a.BuildWorkers, &a.Status, &a.Reason, &a.ServerID, &a.OperationID, &a.CreatedAt, &a.UpdatedAt, &a.NodeName); err != nil {
		return nil, err
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
