//go:build integration

package integration

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
)

// oneDaemonChecker answers for every node with one Docker daemon, and
// holds each check until both racing creates have been checked, so both
// reach the daemon rule together.
type oneDaemonChecker struct{ checked *sync.WaitGroup }

func (oneDaemonChecker) Parse(_ []byte, settings map[string]string) (connections.Check, error) {
	return connections.Check{Account: "node/" + settings["node_id"], Details: map[string]string{
		"node_id": settings["node_id"], "identity_generation": settings["identity_generation"], "docker_socket": "/var/run/docker.sock", "docker_id": "daemon-shared", "architecture": "amd64",
	}}, nil
}

func (c oneDaemonChecker) Check(_ context.Context, raw []byte, settings map[string]string) (connections.Check, error) {
	c.checked.Done()
	c.checked.Wait()
	return c.Parse(raw, settings)
}

type noVault struct{}

func (noVault) Put(context.Context, string, string, []byte) error {
	return errors.New("a node connection wrote to Vault")
}
func (noVault) Reveal(context.Context, string, string) ([]byte, error) {
	return nil, connections.ErrNotFound
}
func (noVault) Delete(context.Context, string, string) error { return nil }

type allowAll struct{}

func (allowAll) Require(context.Context, security.Principal, security.Permission, string) error {
	return nil
}

// Two nodes on one Docker daemon connecting at once: the daemon lock lets
// exactly one through, so the daemon never gets a second fence journal.
func TestNodeConnectionsRaceForOneDaemon(t *testing.T) {
	store, _ := signInStore(t)
	var checked sync.WaitGroup
	checked.Add(2)
	svc := connections.NewService(store, noVault{}, map[string]connections.Checker{connections.Server: oneDaemonChecker{checked: &checked}}, allowAll{}, nil, nil)
	p := security.Principal{OrgID: "org_1", UserID: "usr_1"}
	nodes := []string{"node_6f1c1b8e-3f7a-4c2a-9d7e-1a2b3c4d5e6f", "node_0d9e8f7a-6b5c-4d3e-8f1a-2b3c4d5e6f70"}
	errs := make([]error, len(nodes))
	var done sync.WaitGroup
	for i, node := range nodes {
		done.Go(func() {
			_, errs[i] = svc.CreateNodeConnection(t.Context(), p, connections.NodeConnectionInput{Cloud: connections.Server, NodeID: node, Name: "web-" + node[5:9], IdentityGeneration: 1})
		})
	}
	done.Wait()
	var won, refused int
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, connections.ErrInvalid) && strings.Contains(err.Error(), "this Docker daemon is already connected as node web-"):
			refused++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if won != 1 || refused != 1 {
		t.Fatalf("%d connected and %d were refused, want one of each", won, refused)
	}
	all, err := store.ListConnections(t.Context(), "org_1")
	if err != nil || len(all) != 1 || all[0].Method != connections.MethodNode {
		t.Fatalf("stored %v, %v", all, err)
	}
}

// A check that read a node connection before its node rejoined can't put
// the old identity generation back: the rejoin moves the connection's
// generation, so the stale write is refused.
func TestNodeRejoinBeatsAStaleCheck(t *testing.T) {
	store, _ := signInStore(t)
	var checked sync.WaitGroup
	checked.Add(1)
	svc := connections.NewService(store, noVault{}, map[string]connections.Checker{connections.Server: oneDaemonChecker{checked: &checked}}, allowAll{}, nil, nil)
	p := security.Principal{OrgID: "org_1", UserID: "usr_1"}
	node := "node_6f1c1b8e-3f7a-4c2a-9d7e-1a2b3c4d5e6f"
	c, err := svc.CreateNodeConnection(t.Context(), p, connections.NodeConnectionInput{Cloud: connections.Server, NodeID: node, Name: "web-1", IdentityGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := store.GetConnection(t.Context(), "org_1", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.RefreshNodeGeneration(t.Context(), "org_1", node, 2); err != nil {
		t.Fatal(err)
	}
	if err = store.UpdateConnection(t.Context(), stale); !errors.Is(err, connections.ErrChanged) {
		t.Fatalf("a stale check's write: %v", err)
	}
	got, err := store.GetConnection(t.Context(), "org_1", c.ID)
	if err != nil || got.Details["identity_generation"] != "2" || got.Generation <= c.Generation {
		t.Fatalf("after the rejoin: %+v, %v", got, err)
	}
}

// sharedDaemonChecker answers for every node and every SSH key with one
// Docker daemon, so connections in two organizations meet on it.
type sharedDaemonChecker struct{}

func (sharedDaemonChecker) Parse(_ []byte, settings map[string]string) (connections.Check, error) {
	if settings["node_id"] == "" {
		return connections.Check{Account: "deploy@web.example.com:22", Details: map[string]string{
			"host": "web.example.com", "port": "22", "user": "deploy", "host_key": "ssh-ed25519 AAAA", "docker_socket": "/var/run/docker.sock", "docker_id": "daemon-shared", "architecture": "amd64",
		}}, nil
	}
	return oneDaemonChecker{}.Parse(nil, settings)
}

func (c sharedDaemonChecker) Check(_ context.Context, raw []byte, settings map[string]string) (connections.Check, error) {
	return c.Parse(raw, settings)
}

// keptVault keeps SSH keys in memory.
type keptVault struct {
	mu   sync.Mutex
	kept map[string][]byte
}

func (v *keptVault) Put(_ context.Context, org, id string, cred []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.kept[org+"/"+id] = cred
	return nil
}

func (v *keptVault) Reveal(_ context.Context, org, id string) ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	cred, ok := v.kept[org+"/"+id]
	if !ok {
		return nil, connections.ErrNotFound
	}
	return cred, nil
}

func (v *keptVault) Delete(context.Context, string, string) error { return nil }

// One daemon is one daemon whichever organization connects it: a node
// connection next to anything on it is refused. A daemon ID is what the
// host reports, though, so a check of a connection that exists never
// fails it over another organization's: only creating one is refused.
func TestOneDaemonAcrossOrganizations(t *testing.T) {
	const other = ": this Docker daemon is already connected in another organization"
	store, _ := signInStore(t)
	vault := &keptVault{kept: map[string][]byte{}}
	svc := connections.NewService(store, vault, map[string]connections.Checker{connections.Server: sharedDaemonChecker{}}, allowAll{}, nil, nil)
	ours, theirs := security.Principal{OrgID: "org_1", UserID: "usr_1"}, security.Principal{OrgID: "org_2", UserID: "usr_2"}
	node := "node_6f1c1b8e-3f7a-4c2a-9d7e-1a2b3c4d5e6f"
	ssh := connections.CreateInput{Cloud: connections.Server, Name: "over ssh", Credential: "-----BEGIN OPENSSH PRIVATE KEY-----"}

	// Their SSH first, then our node.
	theirSSH, err := svc.Create(t.Context(), theirs, ssh)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.CreateNodeConnection(t.Context(), ours, connections.NodeConnectionInput{Cloud: connections.Server, NodeID: node, Name: "web-1", IdentityGeneration: 1})
	if !errors.Is(err, connections.ErrInvalid) || !strings.HasSuffix(err.Error(), other) {
		t.Fatalf("our node next to their SSH: %v", err)
	}
	// SSH next to SSH stays allowed across organizations.
	ourSSH, err := svc.Create(t.Context(), ours, ssh)
	if err != nil {
		t.Fatalf("our SSH next to their SSH: %v", err)
	}
	if err = svc.Delete(t.Context(), theirs, theirSSH.ID); err != nil {
		t.Fatal(err)
	}
	if err = svc.Delete(t.Context(), ours, ourSSH.ID); err != nil {
		t.Fatal(err)
	}

	// Our node first, then their SSH.
	if _, err = svc.CreateNodeConnection(t.Context(), ours, connections.NodeConnectionInput{Cloud: connections.Server, NodeID: node, Name: "web-1", IdentityGeneration: 1}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Create(t.Context(), theirs, ssh)
	if !errors.Is(err, connections.ErrInvalid) || !strings.HasSuffix(err.Error(), other) {
		t.Fatalf("their SSH next to our node: %v", err)
	}

	// Their SSH connection from before the node, failed since, rechecks
	// itself ready next to our node: our node's claim on the daemon can't
	// keep their working connection down.
	now := time.Now().UTC()
	stale := &connections.Connection{ID: "conn_stale", OrgID: "org_2", Cloud: connections.Server, Name: "stale ssh", Method: connections.MethodToken, Account: "deploy@web.example.com:22", Status: connections.StatusFailed,
		Details: map[string]string{"host": "web.example.com", "port": "22", "user": "deploy", "host_key": "ssh-ed25519 AAAA", "docker_socket": "/var/run/docker.sock", "docker_id": "daemon-shared", "architecture": "amd64"}, CreatedAt: now, UpdatedAt: now}
	if err = store.CreateConnection(t.Context(), stale); err != nil {
		t.Fatal(err)
	}
	if err = vault.Put(t.Context(), "org_2", stale.ID, []byte(ssh.Credential)); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Recheck(t.Context(), theirs, stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != connections.StatusReady {
		t.Fatalf("their recheck next to our node: %s %q", got.Status, got.Message)
	}
	if kept, _ := store.GetConnection(t.Context(), "org_2", stale.ID); kept.Status != got.Status {
		t.Fatalf("stored %s", kept.Status)
	}
}

// Two organizations' nodes on one daemon connecting at once: the lock is
// the daemon's, not an organization's, so only one gets through.
func TestNodeConnectionsRaceForOneDaemonAcrossOrganizations(t *testing.T) {
	store, _ := signInStore(t)
	var checked sync.WaitGroup
	checked.Add(2)
	svc := connections.NewService(store, noVault{}, map[string]connections.Checker{connections.Server: oneDaemonChecker{checked: &checked}}, allowAll{}, nil, nil)
	nodes := []string{"node_6f1c1b8e-3f7a-4c2a-9d7e-1a2b3c4d5e6f", "node_0d9e8f7a-6b5c-4d3e-8f1a-2b3c4d5e6f70"}
	errs := make([]error, len(nodes))
	var done sync.WaitGroup
	for i, node := range nodes {
		done.Go(func() {
			p := security.Principal{OrgID: "org_" + strconv.Itoa(i+1), UserID: "usr_1"}
			_, errs[i] = svc.CreateNodeConnection(t.Context(), p, connections.NodeConnectionInput{Cloud: connections.Server, NodeID: node, Name: "web-1", IdentityGeneration: 1})
		})
	}
	done.Wait()
	var won, refused int
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, connections.ErrInvalid) && strings.HasSuffix(err.Error(), ": this Docker daemon is already connected in another organization"):
			refused++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if won != 1 || refused != 1 {
		t.Fatalf("%d connected and %d were refused, want one of each", won, refused)
	}
}
