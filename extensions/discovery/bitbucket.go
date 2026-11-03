package discovery

import (
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/repositories"
)

// AuthorizeBitbucketInput is the body of POST /api/v1/bitbucket/authorize.
type AuthorizeBitbucketInput struct {
	// Workspace is the slug of the workspace to connect, the acme in
	// bitbucket.org/acme.
	Workspace string `json:"workspace"`
}

// EnableBitbucketWebhookInput is the body of POST /api/v1/bitbucket/webhooks.
type EnableBitbucketWebhookInput struct {
	// Repository is the Bitbucket repository, such as
	// bitbucket.org/acme/api or its URL.
	Repository string `json:"repository"`
}

// BitbucketAuthorization is where to send someone to approve the OAuth
// consumer.
type BitbucketAuthorization struct {
	URL string `json:"url"`
}

func (e *Extension) bitbucketRoutes(r forge.Router, guard identity.Guard) error {
	g := r.Group("/api/v1/bitbucket", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Bitbucket"))
	if err := g.GET("", e.bitbucketStatus,
		forge.WithName("bitbucket.status"),
		forge.WithSummary("The organization's Bitbucket workspaces, and whether this Loomplane has an OAuth consumer"),
		forge.WithResponseSchema(http.StatusOK, "The connection", repositories.BitbucketStatus{}),
	); err != nil {
		return err
	}
	if err := g.POST("/authorize", e.bitbucketAuthorize,
		forge.WithName("bitbucket.authorize"),
		forge.WithSummary("The Bitbucket link that starts connecting a workspace"),
		forge.WithRequestSchema(AuthorizeBitbucketInput{}),
		forge.WithResponseSchema(http.StatusOK, "Where to approve the consumer", BitbucketAuthorization{}),
	); err != nil {
		return err
	}
	if err := g.POST("/workspaces", e.bitbucketConnect,
		forge.WithName("bitbucket.connect"),
		forge.WithSummary("Connect a Bitbucket workspace, from what Bitbucket's redirect carried back"),
		forge.WithRequestSchema(repositories.BitbucketConnectInput{}),
		forge.WithResponseSchema(http.StatusCreated, "The connected workspace", repositories.BitbucketWorkspaceView{}),
	); err != nil {
		return err
	}
	if err := g.DELETE("/workspaces/:id", e.bitbucketDisconnect,
		forge.WithName("bitbucket.disconnect"),
		forge.WithSummary("Disconnect a workspace. The consumer stays approved on Bitbucket."),
		forge.WithNoContentResponse(),
		forge.WithEntity(forge.EntityDef{Type: "BitbucketWorkspace", IDField: "id"}),
	); err != nil {
		return err
	}
	if err := g.GET("/repositories", e.bitbucketRepositories,
		forge.WithName("bitbucket.repositories"),
		forge.WithSummary("The repositories the organization's workspaces can read"),
		forge.WithResponseSchema(http.StatusOK, "Repositories", repositories.BitbucketRepositoryList{}),
	); err != nil {
		return err
	}
	return g.POST("/webhooks", e.bitbucketEnableWebhook,
		forge.WithName("bitbucket.webhook.enable"),
		forge.WithSummary("Try again to add the push webhook that re-analyzes a repository's sources"),
		forge.WithRequestSchema(EnableBitbucketWebhookInput{}),
		forge.WithResponseSchema(http.StatusOK, "Whether pushes re-analyze the repository now", discovery.PushState{}),
	)
}

func (e *Extension) bitbucketEnableWebhook(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in EnableBitbucketWebhookInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be JSON with a repository")
	}
	st, err := e.bitbucketHooks.Enable(c.Request().Context(), p, in.Repository)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, st)
}

func (e *Extension) bitbucketStatus(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	st, err := e.bitbucket.Status(c.Request().Context(), p)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, st)
}

func (e *Extension) bitbucketAuthorize(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in AuthorizeBitbucketInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be JSON with a workspace")
	}
	link, err := e.bitbucket.Authorize(c.Request().Context(), p, in.Workspace)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, BitbucketAuthorization{URL: link})
}

func (e *Extension) bitbucketConnect(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in repositories.BitbucketConnectInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be JSON with code and state")
	}
	ws, err := e.bitbucket.Connect(c.Request().Context(), p, in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusCreated, ws)
}

func (e *Extension) bitbucketDisconnect(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err := e.bitbucket.Disconnect(c.Request().Context(), p, c.Param("id")); err != nil {
		return fail(err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (e *Extension) bitbucketRepositories(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	repos, err := e.bitbucket.Repositories(c.Request().Context(), p)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, repos)
}
