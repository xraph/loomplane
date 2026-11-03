package nodes

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/security"
)

// ServerDrains reviews and applies draining a Cloud server. Each call
// checks its own permission. *cloudinfra.Service is one.
type ServerDrains interface {
	PreviewDrain(ctx context.Context, p security.Principal, cloud, id string) (*cloudinfra.DrainReview, error)
	ApplyDrain(ctx context.Context, p security.Principal, cloud, id string, in cloudinfra.ApplyInput) (*cloudinfra.Server, error)
}

// NodeWorkloadList is what runs on a node.
type NodeWorkloadList struct {
	Workloads []nodes.NodeWorkload `json:"workloads"`
}

// ForgeEnvelope tells the generated client where the list's items are.
func (NodeWorkloadList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "workloads"}
}

// serveNodeServers lets the nodes service find a node's Cloud server and
// what runs there, and the drain routes drain it through cloud
// infrastructure.
func (e *Extension) serveNodeServers(infra *cloudinfra.Service, placed nodes.WorkloadReader) {
	e.drains = infra
	e.svc.Placed = placed
	adoptions, ok := infra.Store.(nodeAdoptionReader)
	if !ok || infra.NodeConnections == nil {
		e.Logger().Warn("Cloud infrastructure keeps no node adoptions here, so nodes have no servers to drain")
		return
	}
	e.svc.Servers = nodeServers{adoptions: adoptions, servers: infra.Store, conns: infra.NodeConnections}
}

type nodeAdoptionReader interface {
	GetNodeAdoption(ctx context.Context, orgID, nodeID string) (*cloudinfra.NodeAdoption, error)
}

type serverReader interface {
	GetServer(ctx context.Context, org, cloud, id string) (*cloudinfra.Server, error)
}

// nodeServers finds a node's Cloud server through its adoption, and checks
// that the server still reaches the node through one of its connections.
type nodeServers struct {
	adoptions nodeAdoptionReader
	servers   serverReader
	conns     cloudinfra.NodeConnectionSource
}

func (s nodeServers) Server(ctx context.Context, orgID, nodeID string) (*nodes.NodeServer, error) {
	a, err := s.adoptions.GetNodeAdoption(ctx, orgID, nodeID)
	if errors.Is(err, cloudinfra.ErrNotFound) {
		return nil, nodes.ErrNoServer
	}
	if err != nil {
		return nil, err
	}
	if a.CloudID == "" || a.ServerID == "" {
		return nil, nodes.ErrNoServer
	}
	srv, err := s.servers.GetServer(ctx, orgID, a.CloudID, a.ServerID)
	if errors.Is(err, cloudinfra.ErrNotFound) {
		return nil, nodes.ErrNoServer
	}
	if err != nil {
		return nil, err
	}
	if srv.Status == "deleted" || srv.Status == "disconnected" {
		return nil, nodes.ErrNoServer
	}
	ids, err := s.Connections(ctx, orgID, nodeID)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(ids, srv.ConnectionID) {
		return nil, nodes.ErrNoServer
	}
	return &nodes.NodeServer{CloudID: a.CloudID, ServerID: a.ServerID, Draining: srv.Draining}, nil
}

func (s nodeServers) Connections(ctx context.Context, orgID, nodeID string) ([]string, error) {
	held, err := s.conns.NodeConnections(ctx, orgID, nodeID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(held))
	for _, c := range held {
		ids = append(ids, c.ID)
	}
	return ids, nil
}

// drainRoutes mounts the drain review, its apply and the workloads list on
// the guarded nodes group.
func (e *Extension) drainRoutes(g forge.Router) error {
	if err := g.POST("/:id/drain", e.drain,
		forge.WithName("nodes.drain"),
		forge.WithSummary("Review draining the Cloud server a node was adopted as: what it holds now"),
		forge.WithResponseSchema(http.StatusOK, "DrainReview", cloudinfra.DrainReview{}),
	); err != nil {
		return err
	}
	if err := g.POST("/:id/drain/apply", e.drainApply,
		forge.WithName("nodes.drainApply"),
		forge.WithSummary("Drain a node's server as reviewed, so nothing new is placed on it"),
		forge.WithRequestSchema(cloudinfra.ApplyInput{}),
		forge.WithResponseSchema(http.StatusOK, "Node", nodes.Node{}),
	); err != nil {
		return err
	}
	return g.GET("/:id/workloads", e.workloads,
		forge.WithName("nodes.workloads"),
		forge.WithSummary("What Loomplane runs on a node, and which databases have no backups"),
		forge.WithResponseSchema(http.StatusOK, "NodeWorkloadList", NodeWorkloadList{}),
	)
}

func (e *Extension) drain(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	_, srv, err := e.svc.DrainTarget(ctx, p, c.Param("id"))
	if err != nil {
		return e.drainFail(c, err)
	}
	if e.drains == nil {
		return e.drainFail(c, nodes.ErrNoServer)
	}
	review, err := e.drains.PreviewDrain(ctx, p, srv.CloudID, srv.ServerID)
	if err != nil {
		return e.drainFail(c, err)
	}
	return c.JSON(http.StatusOK, review)
}

func (e *Extension) drainApply(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in cloudinfra.ApplyInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be JSON with the review to apply")
	}
	ctx := c.Request().Context()
	_, srv, err := e.svc.DrainTarget(ctx, p, c.Param("id"))
	if err != nil {
		return e.drainFail(c, err)
	}
	if e.drains == nil {
		return e.drainFail(c, nodes.ErrNoServer)
	}
	if _, err = e.drains.ApplyDrain(ctx, p, srv.CloudID, srv.ServerID, in); err != nil {
		return e.drainFail(c, err)
	}
	n, err := e.svc.Get(ctx, p, c.Param("id"))
	if err != nil {
		return e.drainFail(c, err)
	}
	return c.JSON(http.StatusOK, n)
}

func (e *Extension) workloads(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	out, err := e.svc.Workloads(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return e.fail(c, err)
	}
	return c.JSON(http.StatusOK, NodeWorkloadList{Workloads: out})
}

// drainFail maps what draining a node's server can fail with. A node that
// isn't a server, and a review the server has moved on from, are
// conflicts the caller can act on; the rest is the nodes API's mapping.
func (e *Extension) drainFail(c forge.Context, err error) error {
	switch {
	case errors.Is(err, nodes.ErrNoServer), errors.Is(err, cloudinfra.ErrConflict), errors.Is(err, cloudinfra.ErrBusy):
		return forge.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, cloudinfra.ErrNotFound), errors.Is(err, cloudprofiles.ErrNotFound):
		return forge.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, cloudinfra.ErrInvalid):
		return forge.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	}
	return e.fail(c, err)
}
