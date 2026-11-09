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
	servers, ok := infra.Store.(serverReader)
	if !ok || infra.NodeConnections == nil {
		e.Logger().Warn("Cloud infrastructure can't list servers by connection here, so nodes have no servers to drain")
		return
	}
	// The adoption is only a hint; a store that keeps none still finds the
	// server through the node's connections.
	found := nodeServers{servers: servers, conns: infra.NodeConnections}
	if adoptions, keeps := infra.Store.(nodeAdoptionReader); keeps {
		found.adoptions = adoptions
	}
	e.svc.Servers = found
}

type nodeAdoptionReader interface {
	GetNodeAdoption(ctx context.Context, orgID, nodeID string) (*cloudinfra.NodeAdoption, error)
}

// serverReader lists an organization's servers that run on a connection.
// *cloudinfra.PgStore is one.
type serverReader interface {
	RuntimeServers(ctx context.Context, org string) ([]*cloudinfra.Server, error)
}

// nodeServers finds a node's Cloud server: the one that runs on one of the
// node's connections. A node adopted by review, by hand or by its join
// token is found the same way. The adoption, when there is one, only says
// which to prefer if several do.
type nodeServers struct {
	adoptions nodeAdoptionReader
	servers   serverReader
	conns     cloudinfra.NodeConnectionSource
}

func (s nodeServers) Server(ctx context.Context, orgID, nodeID string) (*nodes.NodeServer, error) {
	ids, err := s.Connections(ctx, orgID, nodeID)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nodes.ErrNoServer
	}
	all, err := s.servers.RuntimeServers(ctx, orgID)
	if err != nil {
		return nil, err
	}
	var on []*cloudinfra.Server
	for _, v := range all {
		if v.OrgID != orgID || v.Status == "deleted" || v.Status == "disconnected" {
			continue
		}
		if slices.Contains(ids, v.RuntimeConnectionID) || slices.Contains(ids, v.ConnectionID) {
			on = append(on, v)
		}
	}
	if len(on) == 0 {
		return nil, nodes.ErrNoServer
	}
	hint := ""
	if s.adoptions != nil {
		a, aerr := s.adoptions.GetNodeAdoption(ctx, orgID, nodeID)
		if aerr != nil && !errors.Is(aerr, cloudinfra.ErrNotFound) {
			return nil, aerr
		}
		if aerr == nil {
			hint = a.ServerID
		}
	}
	srv := on[0]
	for _, v := range on {
		if v.ID == hint {
			srv = v
			break
		}
		if v.Status == "ready" && srv.Status != "ready" {
			srv = v
		}
	}
	return &nodes.NodeServer{CloudID: srv.CloudID, ServerID: srv.ID, Draining: srv.Draining}, nil
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
