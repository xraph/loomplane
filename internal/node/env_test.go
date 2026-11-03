package node

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	extnodes "github.com/xraph/loomplane/extensions/nodes"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/nodes/nodestest"
)

// testEnv is a real hub behind the real node routes, over an in-memory store.
type testEnv struct {
	Store    *nodestest.Store
	Tokens   *nodestest.Tokens
	Svc      *nodes.Service
	audience string

	cur atomic.Pointer[hubAndRoutes]
}

type hubAndRoutes struct {
	hub    *nodes.Hub
	routes http.Handler
	cancel context.CancelFunc
}

func newTestEnv(t *testing.T, audience string) *testEnv {
	t.Helper()
	e := &testEnv{Store: nodestest.NewStore(), Tokens: &nodestest.Tokens{}, audience: audience}
	e.Svc = nodestest.NewService(e.Store, e.Tokens)
	e.Svc.Notify = func(id string, msg any) { e.Hub().Notify(id, msg) }
	e.RestartHub(t)
	t.Cleanup(func() { e.cur.Load().cancel() })
	return e
}

// Hub is the current hub.
func (e *testEnv) Hub() *nodes.Hub { return e.cur.Load().hub }

// Handler serves whichever hub is current.
func (e *testEnv) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { e.cur.Load().routes.ServeHTTP(w, r) })
}

// RestartHub ends every session the hub holds and swaps in a fresh hub and
// routes on the same store, as a restarted control plane would.
func (e *testEnv) RestartHub(t *testing.T) {
	t.Helper()
	hub := nodestest.NewHub(t, e.audience, e.Store, e.Svc)
	ctx, cancel := context.WithCancel(context.Background())
	routes, err := extnodes.NodeHandler(ctx, hub, e.Svc, e.audience)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if old := e.cur.Swap(&hubAndRoutes{hub: hub, routes: routes, cancel: cancel}); old != nil {
		old.cancel()
	}
}
