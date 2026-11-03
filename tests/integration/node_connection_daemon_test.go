//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

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
