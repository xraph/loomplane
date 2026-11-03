package nodes

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

const (
	// maxJoinBody bounds the one request that needs no session.
	maxJoinBody = 1 << 20
	// mintHint is what an operator does when a join token may be spent.
	mintHint = "mint a new join token and try again"
)

// JoinTokenMinter makes, lists and cancels node join tokens. The API key
// service does.
type JoinTokenMinter interface {
	CreateNodeJoinToken(ctx context.Context, p security.Principal, scope apikeys.NodeJoinScope, lifetime time.Duration) (*apikeys.NodeJoinToken, error)
	ListNodeJoinTokens(ctx context.Context, p security.Principal) ([]apikeys.PendingNodeJoinToken, error)
	CancelNodeJoinToken(ctx context.Context, p security.Principal, tokenID string) error
}

// JoinTokenList is the org's join tokens no machine has used yet. The
// secrets are never in it.
type JoinTokenList struct {
	Tokens []apikeys.PendingNodeJoinToken `json:"tokens"`
}

// NodeList is the org's nodes.
type NodeList struct {
	Nodes []nodes.Node `json:"nodes"`
}

// NodeUpdate changes a node. An absent field stays; an empty list or map is a
// real change (narrow to nothing, clear the labels). Endpoints can only be
// narrowed here: widening is done on the machine.
type NodeUpdate struct {
	Name        *string              `json:"name,omitempty"`
	Labels      map[string]string    `json:"labels,omitempty"`
	Endpoints   []nodeproto.Endpoint `json:"endpoints,omitempty"`
	IngressMode *string              `json:"ingress_mode,omitempty"`
}

// JoinTokenRequest scopes a join token to the machine it is for.
type JoinTokenRequest struct {
	Labels    map[string]string    `json:"labels,omitempty"`
	Endpoints []nodeproto.Endpoint `json:"endpoints,omitempty"`
	// Edge makes the machine an edge, which serves traffic for the org.
	Edge bool `json:"edge,omitempty"`
	// EdgeScope is "org" or "instance". Only a platform admin may mint an
	// instance edge.
	EdgeScope string `json:"edge_scope,omitempty"`
	CloudID   string `json:"cloud_id,omitempty"`
	// LifetimeMinutes is how long the token can be used. It defaults to, and
	// is capped at, an hour.
	LifetimeMinutes int `json:"lifetime_minutes,omitempty"`
}

// JoinToken is a new join token. The token is shown once.
type JoinToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	// Command is the line to run on the machine.
	Command string `json:"command"`
}

// NodeJoin is what a machine sends to join. The token authenticates it.
type NodeJoin struct {
	Token string `json:"token"`
	Name  string `json:"name,omitempty"`
	// PublicKey is the node's Ed25519 public key, base64.
	PublicKey string `json:"public_key"`
	// Signature is the node's Ed25519 signature, base64, over
	// "loomplane-node-join-v1\n" and the JSON {"token","name","public_key"},
	// with name exactly as sent. It proves the machine holds the key.
	Signature       string               `json:"signature"`
	Endpoints       []nodeproto.Endpoint `json:"endpoints,omitempty"`
	Labels          map[string]string    `json:"labels,omitempty"`
	Inventory       nodeproto.Inventory  `json:"inventory"`
	NodeVersion     string               `json:"node_version"`
	ProtocolVersion int                  `json:"protocol_version"`
}

// NodeJoined is what a machine learns on joining.
type NodeJoined struct {
	NodeID             string               `json:"node_id"`
	OrgID              string               `json:"org_id"`
	IdentityGeneration int64                `json:"identity_generation"`
	Endpoints          []nodeproto.Endpoint `json:"endpoints"`
	CloudID            string               `json:"cloud_id,omitempty"`
}

// routes mounts the nodes API. Everything but join sits behind the guard.
func (e *Extension) routes(r forge.Router, guard ...forge.Middleware) error {
	tag := forge.WithGroupTags("Nodes")
	g := r.Group("/api/v1/nodes", forge.WithGroupMiddleware(guard...), tag)
	if err := g.GET("", e.list,
		forge.WithName("nodes.list"),
		forge.WithSummary("The machines joined to this organization"),
		forge.WithResponseSchema(http.StatusOK, "NodeList", NodeList{}),
	); err != nil {
		return err
	}
	if err := g.POST("/join-tokens", e.createJoinToken,
		forge.WithName("nodeJoinTokens.create"),
		forge.WithSummary("Make a single-use token for one machine to join, and the command that uses it"),
		forge.WithRequestSchema(JoinTokenRequest{}),
		forge.WithResponseSchema(http.StatusCreated, "JoinToken", JoinToken{}),
	); err != nil {
		return err
	}
	if err := g.GET("/join-tokens", e.listJoinTokens,
		forge.WithName("nodeJoinTokens.list"),
		forge.WithSummary("The join tokens no machine has used yet: who made them, with which key, for what, and when they expire"),
		forge.WithResponseSchema(http.StatusOK, "JoinTokenList", JoinTokenList{}),
	); err != nil {
		return err
	}
	if err := g.DELETE("/join-tokens/:id", e.cancelJoinToken,
		forge.WithName("nodeJoinTokens.cancel"),
		forge.WithSummary("Cancel a join token no machine has used, so none can"),
		forge.WithNoContentResponse(),
	); err != nil {
		return err
	}
	if err := g.GET("/:id", e.get,
		forge.WithName("nodes.get"),
		forge.WithSummary("One node"),
		forge.WithResponseSchema(http.StatusOK, "Node", nodes.Node{}),
	); err != nil {
		return err
	}
	if err := g.PATCH("/:id", e.update,
		forge.WithName("nodes.update"),
		forge.WithSummary("Rename a node, relabel it, narrow what it serves or set its ingress mode"),
		forge.WithRequestSchema(NodeUpdate{}),
		forge.WithResponseSchema(http.StatusOK, "Node", nodes.Node{}),
	); err != nil {
		return err
	}
	if err := g.POST("/:id/revoke", e.revoke,
		forge.WithName("nodes.revoke"),
		forge.WithSummary("Stop trusting a node, at once. Its key can't connect again."),
		forge.WithResponseSchema(http.StatusOK, "Node", nodes.Node{}),
	); err != nil {
		return err
	}
	// The token in the body is the machine's only credential, so no guard.
	open := r.Group("/api/v1/nodes", tag)
	return open.POST("/join", e.join,
		forge.WithName("nodes.join"),
		forge.WithSummary("A machine joins with a join token. The token is spent."),
		forge.WithRequestSchema(NodeJoin{}),
		forge.WithResponseSchema(http.StatusCreated, "NodeJoined", NodeJoined{}),
	)
}

func principal(c forge.Context) (security.Principal, error) {
	p, ok := security.PrincipalFrom(c.Request().Context())
	if !ok {
		return p, forge.NewHTTPError(http.StatusUnauthorized, security.ErrUnauthenticated.Error())
	}
	return p, nil
}

func (e *Extension) list(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	out, err := e.svc.List(c.Request().Context(), p)
	if err != nil {
		return e.fail(c, err)
	}
	if out == nil {
		out = []nodes.Node{}
	}
	return c.JSON(http.StatusOK, NodeList{Nodes: out})
}

func (e *Extension) get(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	n, err := e.svc.Get(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return e.fail(c, err)
	}
	return c.JSON(http.StatusOK, n)
}

func (e *Extension) update(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in NodeUpdate
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be JSON: name, labels, endpoints or ingress_mode")
	}
	n, err := e.svc.Update(c.Request().Context(), p, c.Param("id"), nodes.Update{Name: in.Name, Labels: in.Labels, Endpoints: in.Endpoints, IngressMode: in.IngressMode})
	if err != nil {
		return e.fail(c, err)
	}
	return c.JSON(http.StatusOK, n)
}

func (e *Extension) revoke(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err = e.svc.Revoke(ctx, p, c.Param("id")); err != nil {
		return e.fail(c, err)
	}
	n, err := e.svc.Get(ctx, p, c.Param("id"))
	if err != nil {
		return e.fail(c, err)
	}
	return c.JSON(http.StatusOK, n)
}

func (e *Extension) createJoinToken(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in JoinTokenRequest
	if err = c.BindJSON(&in); err != nil && !errors.Is(err, io.EOF) {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be JSON, or empty")
	}
	scope := apikeys.NodeJoinScope{CloudID: in.CloudID, Labels: in.Labels, Endpoints: in.Endpoints, Edge: in.Edge, EdgeScope: in.EdgeScope}
	tok, err := e.minter.CreateNodeJoinToken(c.Request().Context(), p, scope, time.Duration(in.LifetimeMinutes)*time.Minute)
	if err != nil {
		return e.fail(c, err)
	}
	return c.JSON(http.StatusCreated, JoinToken{Token: tok.Secret, ExpiresAt: tok.ExpiresAt, Command: e.installCommand(tok.Secret, in.Endpoints)})
}

func (e *Extension) listJoinTokens(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	out, err := e.minter.ListNodeJoinTokens(c.Request().Context(), p)
	if err != nil {
		return e.fail(c, err)
	}
	if out == nil {
		out = []apikeys.PendingNodeJoinToken{}
	}
	return c.JSON(http.StatusOK, JoinTokenList{Tokens: out})
}

func (e *Extension) cancelJoinToken(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err = e.minter.CancelNodeJoinToken(c.Request().Context(), p, c.Param("id")); err != nil {
		return e.fail(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// installCommand is the line an operator runs on the machine. The token goes
// in LOOMPLANE_JOIN_TOKEN, so no other user on the machine can read it from
// a process's arguments. The pipe leaves node join no terminal to ask on, so
// the token's endpoints go on the line as --allow: what the panel chose is
// what the node allows, and the operator sees it before running it. The
// minter has checked every endpoint is one a node can allow.
func (e *Extension) installCommand(token string, allow []nodeproto.Endpoint) string {
	cmd := "curl -fsSL " + strings.TrimRight(e.cfg.PublicURL, "/") + "/install/node.sh | " + joinTokenEnv + "=" + token + " sh -s"
	var eps []string
	for _, ep := range allow {
		if slices.Contains(nodeproto.AllowableEndpoints(), ep) && !slices.Contains(eps, string(ep)) {
			eps = append(eps, string(ep))
		}
	}
	if len(eps) > 0 {
		cmd += " -- --allow " + strings.Join(eps, ",")
	}
	return cmd
}

// joinTokenEnv is where node join and node.sh look for a join token.
const joinTokenEnv = "LOOMPLANE_JOIN_TOKEN" //nolint:gosec // the name of a variable, not a credential

func (e *Extension) join(c forge.Context) error {
	// Counted before the body is read: the join is the one route anyone can
	// call, and each one can cost a token lookup.
	if e.limited(c.Response(), c.Request(), limitJoin, e.cfg.Nodes.JoinRateLimit) {
		return nil
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Response(), c.Request().Body, maxJoinBody))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		return forge.NewHTTPError(http.StatusRequestEntityTooLarge, "the join request is too large")
	case err != nil:
		return forge.NewHTTPError(http.StatusBadRequest, "the join request could not be read")
	}
	var in NodeJoin
	if err = json.Unmarshal(body, &in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be JSON: token, public_key, node_version and protocol_version")
	}
	key, err := base64.StdEncoding.DecodeString(in.PublicKey)
	if err != nil {
		return forge.NewHTTPError(http.StatusUnprocessableEntity, "public_key must be base64")
	}
	sig, err := base64.StdEncoding.DecodeString(in.Signature)
	if err != nil {
		return forge.NewHTTPError(http.StatusUnprocessableEntity, "signature must be base64")
	}
	res, err := e.svc.Join(c.Request().Context(), nodes.JoinRequest{
		Token: in.Token, Name: in.Name, PublicKey: key, Signature: sig, Endpoints: in.Endpoints, Labels: in.Labels,
		Inventory: in.Inventory, NodeVersion: in.NodeVersion, ProtocolVersion: in.ProtocolVersion,
	})
	if err != nil {
		return e.fail(c, err)
	}
	return c.JSON(http.StatusCreated, NodeJoined{
		NodeID: res.NodeID, OrgID: res.OrgID, IdentityGeneration: res.IdentityGeneration,
		Endpoints: res.Endpoints, CloudID: res.CloudID,
	})
}

// fail maps a service error to the status the caller should see. A failure
// after the join token was spent says to mint a new one, and keeps the cause
// out of the response.
func (e *Extension) fail(c forge.Context, err error) error {
	var conflict *nodes.JoinConflictError
	switch {
	case errors.As(err, &conflict):
		// The text says which name or key, and whether to mint a new token.
		return c.JSON(http.StatusConflict, map[string]string{"error": conflict.Error()})
	case errors.Is(err, nodes.ErrMintNewToken):
		e.Logger().Error("a node could not be created after its join token was spent", forge.F("error", err.Error()))
		// Forge hides the message of a 5xx, and this one is for the operator.
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": nodes.ErrMintNewToken.Error()})
	case errors.Is(err, apikeys.ErrUnavailable):
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": apikeys.ErrUnavailable.Error() + "; if it keeps failing the token may be spent, so " + mintHint})
	case errors.Is(err, apikeys.ErrRejected):
		return forge.NewHTTPError(http.StatusUnauthorized, "that join token isn't valid: it was used, expired or revoked; "+mintHint)
	case errors.Is(err, nodes.ErrUpdateNode), errors.Is(err, nodes.ErrNodeNewer):
		return forge.NewHTTPError(http.StatusUpgradeRequired, err.Error())
	case errors.Is(err, nodes.ErrNotFound):
		return forge.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, apikeys.ErrNotFound):
		return forge.NewHTTPError(http.StatusNotFound, "no unused join token has that id; it may have been used, cancelled or expired")
	case errors.Is(err, nodes.ErrNameTaken):
		return forge.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, nodes.ErrInvalid), errors.Is(err, nodes.ErrWidenRemotely), errors.Is(err, apikeys.ErrInvalid):
		return forge.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	}
	if status := security.StatusFor(err); status != http.StatusInternalServerError {
		return forge.NewHTTPError(status, err.Error())
	}
	return err
}
