package nodes

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xraph/loomplane/internal/security"
)

// ErrNoServer is a node that isn't a server in any Cloud yet, so it has
// nothing to drain.
var ErrNoServer = errors.New("this node isn't a server in a Cloud, so there is nothing to drain; it becomes one when it's adopted into a Cloud")

// NodeServer is the Cloud server a node was adopted as.
type NodeServer struct {
	CloudID  string
	ServerID string
	Draining bool
}

// NodeServers says what a node is to the rest of Loomplane. The nodes
// extension fills it from the node's adoption and its connections.
type NodeServers interface {
	// Server is the Cloud server node nodeID of orgID was adopted as, or
	// ErrNoServer.
	Server(ctx context.Context, orgID, nodeID string) (*NodeServer, error)
	// Connections lists the IDs of the connections that reach the node.
	Connections(ctx context.Context, orgID, nodeID string) ([]string, error)
}

// PlacedBinding is a binding deployed on a node, as the store reads it.
type PlacedBinding struct {
	ID            string
	ProjectID     string
	EnvironmentID string
	// Environment is the environment's name.
	Environment string
	Subject     string
	Name        string
	// Config is the binding's applied config. Only its kind is read.
	Config json.RawMessage
}

// BackupPolicyRef names an enabled backup policy by its environment and
// the resource it backs up.
type BackupPolicyRef struct {
	EnvironmentID string
	Resource      string
}

// WorkloadReader reads what is deployed on a node and which of it is
// backed up. *PgStore is one.
type WorkloadReader interface {
	// BindingsOn lists the org's bindings deployed through one of
	// connectionIDs, or placed on the Cloud server serverID.
	BindingsOn(ctx context.Context, orgID string, connectionIDs []string, serverID string) ([]PlacedBinding, error)
	// BackupPolicies lists the enabled backup policies in environmentIDs.
	BackupPolicies(ctx context.Context, orgID string, environmentIDs []string) ([]BackupPolicyRef, error)
}

// The kinds of workload a node runs.
const (
	WorkloadService   = "service"
	WorkloadJob       = "job"
	WorkloadDatabase  = "database"
	WorkloadComponent = "component"
)

// NodeWorkload is something Loomplane runs on a node. A database without
// a backup policy has HasBackupPolicy false: if the machine is lost, so is
// its data, and the dashboard says so.
type NodeWorkload struct {
	BindingID   string `json:"binding_id"`
	ProjectID   string `json:"project_id"`
	Environment string `json:"environment"`
	// Service is the binding's name: the service's, or the resource's.
	Service string `json:"service"`
	// Kind is service, job, database or component.
	Kind            string `json:"kind"`
	HasBackupPolicy bool   `json:"has_backup_policy"`
}

// Workloads lists what Loomplane runs on a node of the org: what was
// deployed through its connections or placed on its Cloud server.
func (s *Service) Workloads(ctx context.Context, p security.Principal, nodeID string) ([]NodeWorkload, error) {
	if err := s.allow(ctx, p, security.NodeRead, false); err != nil {
		return nil, err
	}
	if _, err := s.Store.Get(ctx, p.OrgID, nodeID); err != nil {
		return nil, err
	}
	out := []NodeWorkload{}
	if s.Servers == nil || s.Placed == nil {
		return out, nil
	}
	conns, err := s.Servers.Connections(ctx, p.OrgID, nodeID)
	if err != nil {
		return nil, err
	}
	serverID := ""
	switch srv, serr := s.Servers.Server(ctx, p.OrgID, nodeID); {
	case serr == nil:
		serverID = srv.ServerID
	case !errors.Is(serr, ErrNoServer):
		return nil, serr
	}
	if len(conns) == 0 && serverID == "" {
		return out, nil
	}
	placed, err := s.Placed.BindingsOn(ctx, p.OrgID, conns, serverID)
	if err != nil || len(placed) == 0 {
		return out, err
	}
	var envs []string
	for _, b := range placed {
		envs = append(envs, b.EnvironmentID)
	}
	policies, err := s.Placed.BackupPolicies(ctx, p.OrgID, envs)
	if err != nil {
		return nil, err
	}
	backed := map[BackupPolicyRef]bool{}
	for _, pol := range policies {
		backed[pol] = true
	}
	for _, b := range placed {
		kind, resource, ok := workloadKind(b)
		if !ok {
			continue
		}
		out = append(out, NodeWorkload{
			BindingID: b.ID, ProjectID: b.ProjectID, Environment: b.Environment, Service: b.Name, Kind: kind,
			HasBackupPolicy: backed[BackupPolicyRef{EnvironmentID: b.EnvironmentID, Resource: resource}],
		})
	}
	return out, nil
}

// workloadKind is what a binding runs as, and the resource name a backup
// policy for it carries. A composed resource runs nothing itself, only
// its components do, so it isn't a workload (ok is false).
func workloadKind(b PlacedBinding) (kind, resource string, ok bool) {
	if b.Subject == "service" {
		return WorkloadService, b.Name, true
	}
	var cfg struct {
		Composition json.RawMessage `json:"composition"`
		Component   *struct {
			Resource   string `json:"resource"`
			Protection string `json:"protection"`
			Component  struct {
				Role string `json:"role"`
			} `json:"component"`
		} `json:"component"`
	}
	_ = json.Unmarshal(b.Config, &cfg) //nolint:errcheck // a config that doesn't parse is a plain resource
	switch {
	case cfg.Component != nil && cfg.Component.Component.Role == "job":
		return WorkloadJob, cfg.Component.Resource, true
	case cfg.Component != nil && cfg.Component.Protection == "native":
		// A database engine inside a composed resource, backed up under
		// the composed resource's name.
		return WorkloadDatabase, cfg.Component.Resource, true
	case cfg.Component != nil:
		return WorkloadComponent, cfg.Component.Resource, true
	case len(cfg.Composition) > 0 && string(cfg.Composition) != "null":
		return "", "", false
	}
	// A resource of its own is a database engine.
	return WorkloadDatabase, b.Name, true
}

// DrainTarget is the Cloud server to drain for a node of the org, for
// someone who may manage nodes. Draining it is cloud infrastructure's
// call, which checks its own permission too. A node that isn't a server in
// a Cloud is ErrNoServer.
func (s *Service) DrainTarget(ctx context.Context, p security.Principal, nodeID string) (*Node, *NodeServer, error) {
	if err := s.allow(ctx, p, security.NodeManage, true); err != nil {
		return nil, nil, err
	}
	n, err := s.Store.Get(ctx, p.OrgID, nodeID)
	if err != nil {
		return nil, nil, err
	}
	if n.Status == StatusRevoked {
		return nil, nil, ErrNotFound
	}
	if s.Servers == nil {
		return nil, nil, ErrNoServer
	}
	srv, err := s.Servers.Server(ctx, p.OrgID, nodeID)
	if err != nil {
		return nil, nil, err
	}
	return n, srv, nil
}

// drainingStatus shows an online node whose Cloud server is draining as
// draining. It's worked out on read: the stored status stays the
// session's online or offline, and an offline node stays offline, which
// matters more. A lookup that fails leaves the status as it is.
func (s *Service) drainingStatus(ctx context.Context, n *Node) {
	if s.Servers == nil || n.Status != StatusOnline {
		return
	}
	srv, err := s.Servers.Server(ctx, n.OrgID, n.ID)
	switch {
	case err == nil && srv.Draining:
		n.Status = StatusDraining
	case err != nil && !errors.Is(err, ErrNoServer):
		s.log().Warn("nodes: reading whether a node's server is draining", "node", n.ID, "error", err)
	}
}
