package connections

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/security"
)

// NodeConnectionInput connects what runs on a joined node. There's no
// credential: Loomplane reaches the node through its own streams, so the
// node and the cloud's settings are all it takes.
type NodeConnectionInput struct {
	// Cloud is server. Kubernetes and Firecracker come in later phases.
	Cloud string
	// NodeID is the node's ID, node_ and a UUID.
	NodeID string
	Name   string
	// IdentityGeneration is the node's identity generation, which
	// reviewed placements pin. Callers pass the stored
	// nodes.Node.IdentityGeneration, never a value from a request.
	IdentityGeneration int64
	// Settings are the cloud's options. For server: docker_socket,
	// build_workers and advertise_host.
	Settings map[string]string
}

// nodeSettings are the settings a node connection takes from its input,
// by cloud. The node's own ID and generation come from the input itself.
var nodeSettings = map[string][]string{
	Server: {"docker_socket", "build_workers", "advertise_host"},
}

// CreateNodeConnection is the one writer of node-backed connections, of
// any cloud. It checks what runs on the node through the node's streams
// and stores the connection with method node, keeping nothing in Vault.
// Only server is connected this way for now; any other cloud is
// ErrInvalid.
func (s *Service) CreateNodeConnection(ctx context.Context, p security.Principal, in NodeConnectionInput) (*Connection, error) {
	if err := s.authz.Require(ctx, p, security.CloudAccountManage, ""); err != nil {
		return nil, err
	}
	cloud := strings.TrimSpace(in.Cloud)
	keys, ok := nodeSettings[cloud]
	if !ok {
		return nil, fmt.Errorf("%w: a node connects its Docker as a server, not %q", ErrInvalid, in.Cloud)
	}
	if in.IdentityGeneration < 1 {
		return nil, fmt.Errorf("%w: provide the node's identity generation", ErrInvalid)
	}
	settings := map[string]string{"node_id": in.NodeID, "identity_generation": strconv.FormatInt(in.IdentityGeneration, 10)}
	for _, key := range keys {
		if value := strings.TrimSpace(in.Settings[key]); value != "" {
			settings[key] = value
		}
	}
	return s.create(ctx, p, "", cloud, in.Name, nil, server.NodeCredential(p.OrgID, in.NodeID), settings, MethodNode, "")
}

// NodeConnections returns the organization's connections that reach node
// nodeID. It checks no role, like All.
func (s *Service) NodeConnections(ctx context.Context, orgID, nodeID string) ([]*Connection, error) {
	all, err := s.store.ListConnections(ctx, orgID)
	if err != nil {
		return nil, err
	}
	var out []*Connection
	for _, c := range all {
		if c.Method == MethodNode && c.Details["node_id"] == nodeID {
			out = append(out, c)
		}
	}
	return out, nil
}

// refreshTries bounds how often RefreshNodeGeneration rereads a
// connection that changed under it.
const refreshTries = 3

// RefreshNodeGeneration records that node nodeID rejoined with a new key,
// as identity generation generation, on every connection that reaches
// it. The generation is part of the connection's identity and a reviewed
// setting, so the connection's own generation moves and placements
// reviewed before then refuse until someone reviews them again. A
// generation no newer than the one recorded changes nothing, so a late
// call never rolls one back.
func (s *Service) RefreshNodeGeneration(ctx context.Context, orgID, nodeID string, generation int64) error {
	if generation < 1 {
		return fmt.Errorf("%w: a node's identity generation starts at 1", ErrInvalid)
	}
	held, err := s.NodeConnections(ctx, orgID, nodeID)
	if err != nil {
		return err
	}
	for _, c := range held {
		if err := s.refreshGeneration(ctx, c, generation); err != nil {
			return err
		}
	}
	return nil
}

// refreshGeneration moves c to identity generation generation, reading
// it again when a check or another refresh changed it first. The store
// refuses a write from a stale read, so a check that started before the
// rejoin can't put the old generation back.
func (s *Service) refreshGeneration(ctx context.Context, c *Connection, generation int64) error {
	for try := 1; ; try++ {
		if recorded, parseErr := strconv.ParseInt(c.Details["identity_generation"], 10, 64); parseErr == nil && recorded >= generation {
			return nil
		}
		c.Details = maps.Clone(c.Details)
		c.Details["identity_generation"] = strconv.FormatInt(generation, 10)
		c.UpdatedAt = s.now().UTC()
		err := s.store.UpdateConnection(ctx, c)
		if err == nil {
			s.record(ctx, "cloud_connection.node_rejoined", security.SystemPrincipal(c.OrgID), c)
			return nil
		}
		if !errors.Is(err, ErrChanged) || try == refreshTries {
			return err
		}
		if c, err = s.store.GetConnection(ctx, c.OrgID, c.ID); err != nil {
			return err
		}
	}
}

// SetNodeAdvertiseHost writes address, a node's public IP address that
// answered its address check, as the advertise_host of node connection
// id. It isn't part of the connection's identity, so the generation and
// the reviews that pin it stay. A connection that isn't a node's is
// ErrInvalid. A write that raced a check is read again and retried.
func (s *Service) SetNodeAdvertiseHost(ctx context.Context, orgID, id, address string) error {
	if net.ParseIP(address) == nil {
		return fmt.Errorf("%w: a node's advertised address is the IP address that answered", ErrInvalid)
	}
	c, err := s.store.GetConnection(ctx, orgID, id)
	if err != nil {
		return err
	}
	for try := 1; ; try++ {
		if c.Method != MethodNode || c.Cloud != Server || c.Details["node_id"] == "" {
			return fmt.Errorf("%w: %s isn't a node's server connection", ErrInvalid, c.Name)
		}
		if c.Details["advertise_host"] == address {
			return nil
		}
		c.Details = maps.Clone(c.Details)
		c.Details["advertise_host"] = address
		c.UpdatedAt = s.now().UTC()
		err = s.store.UpdateConnection(ctx, c)
		if err == nil {
			s.record(ctx, "cloud_connection.node_address", security.SystemPrincipal(c.OrgID), c)
			return nil
		}
		if !errors.Is(err, ErrChanged) || try == refreshTries {
			return err
		}
		if c, err = s.store.GetConnection(ctx, orgID, id); err != nil {
			return err
		}
	}
}

// nodeCredential is the marker a node connection's checks and
// deployments get in place of a credential. Loomplane makes it on read.
func nodeCredential(c *Connection) ([]byte, error) {
	if c.Cloud != Server || c.Details["node_id"] == "" {
		return nil, fmt.Errorf("%w: %s reaches its node in a way this Loomplane doesn't know", ErrNotReady, c.Name)
	}
	return server.NodeCredential(c.OrgID, c.Details["node_id"]), nil
}

// nodeStreams are the streams the server checker reaches nodes through,
// or nil when it has none.
func (s *Service) nodeStreams() server.NodeStreams {
	switch checker := s.checkers[Server].(type) {
	case ServerChecker:
		return checker.Streams
	case *ServerChecker:
		if checker != nil {
			return checker.Streams
		}
	}
	return nil
}

// connectionLocker is a store that holds a lock across every Loomplane
// process. *PgStore is one.
type connectionLocker interface {
	WithConnectionLock(ctx context.Context, orgID, id string, fn func() error) error
}

// daemonStore is a store that finds the ready server connections on a
// Docker daemon in every organization. *PgStore is one.
type daemonStore interface {
	ReadyServersOnDaemon(ctx context.Context, daemon string) ([]*Connection, error)
}

var (
	_ connectionLocker = (*PgStore)(nil)
	_ daemonStore      = (*PgStore)(nil)
)

// withDaemonLock runs fn holding the lock on Docker daemon daemon, one
// lock for every organization, so two connections to one daemon can't
// both pass daemonFree. A store that can't lock, such as a test's memory
// store, runs fn as is; the Postgres store always locks.
func (s *Service) withDaemonLock(ctx context.Context, daemon string, fn func() error) error {
	locker, ok := s.store.(connectionLocker)
	if !ok {
		return fn()
	}
	// No organization has an empty ID, and connection IDs start with
	// conn_, so this never names a connection's own lock.
	return locker.WithConnectionLock(ctx, "", "docker/"+daemon, fn)
}

// readyOnDaemon are the ready server connections on daemon, in every
// organization the store can search. A store that can't search across
// organizations gives the organization's own.
func (s *Service) readyOnDaemon(ctx context.Context, orgID, daemon string) ([]*Connection, error) {
	if store, ok := s.store.(daemonStore); ok {
		return store.ReadyServersOnDaemon(ctx, daemon)
	}
	all, err := s.store.ListConnections(ctx, orgID)
	if err != nil {
		return nil, err
	}
	var out []*Connection
	for _, c := range all {
		if c.Cloud == Server && c.Status == StatusReady && c.Details["docker_id"] == daemon {
			out = append(out, c)
		}
	}
	return out, nil
}

// daemonFree refuses server connection self, made by method in
// organization orgID, to a Docker daemon that another ready server
// connection, in any organization, attaches in a way that would keep a
// second fence journal for it. SSH connections fence through the one
// journal on the host that the daemon ID names, while each node keeps a
// journal of its own, so any pair with a node connection in it is
// refused, and SSH next to SSH is not. Another organization's connection
// is never named, and counts only when crossOrg is set: a daemon ID is
// what the host says it is, so a node could claim another organization's
// daemon. Creating a connection checks every organization; a check or a
// replacement of one that exists checks only its own, so a forged ID can
// stop its own organization's connection, never another's working one.
func (s *Service) daemonFree(ctx context.Context, orgID, self, daemon, method string, crossOrg bool) error {
	held, err := s.readyOnDaemon(ctx, orgID, daemon)
	if err != nil {
		return err
	}
	for _, c := range held {
		if c.ID == self || c.Cloud != Server || c.Status != StatusReady || c.Details["docker_id"] != daemon {
			continue
		}
		if c.OrgID != orgID && !crossOrg {
			continue
		}
		switch {
		case c.Method != MethodNode && method != MethodNode:
			continue
		case c.OrgID != orgID:
			return fmt.Errorf("%w: this Docker daemon is already connected in another organization", ErrInvalid)
		case c.Method == MethodNode:
			return fmt.Errorf("%w: this Docker daemon is already connected as node %s; remove it from the node first", ErrInvalid, c.Name)
		default:
			return fmt.Errorf("%w: this Docker daemon is already connected as %s over SSH; disconnect it first", ErrInvalid, c.Name)
		}
	}
	return nil
}

// holdDaemon runs save for c, a connection a check or a replacement is
// about to store. When c is a server connection that would be ready on a
// daemon the one-daemon rule gives to another connection of its own
// organization, c is stored failed with the rule's reason instead.
// Another organization's connection on the daemon never fails it (see
// daemonFree). The daemon stays locked throughout, so the rule and the
// write are one step.
func (s *Service) holdDaemon(ctx context.Context, c *Connection, save func() error) error {
	daemon := c.Details["docker_id"]
	if c.Cloud != Server || c.Status != StatusReady || daemon == "" {
		return save()
	}
	return s.withDaemonLock(ctx, daemon, func() error {
		if err := s.daemonFree(ctx, c.OrgID, c.ID, daemon, c.Method, false); err != nil {
			if !errors.Is(err, ErrInvalid) {
				return err
			}
			c.Status, c.Message = StatusFailed, strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": ")
		}
		return save()
	})
}
