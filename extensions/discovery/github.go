package discovery

import (
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/repositories"
)

// ConnectedRepositoryList is the body of GET /api/v1/github/repositories.
type ConnectedRepositoryList struct {
	Repositories []repositories.ConnectedRepository `json:"repositories"`
}

func (e *Extension) githubRoutes(r forge.Router, guard identity.Guard) error {
	g := r.Group("/api/v1/github", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("GitHub"))
	if err := g.GET("", e.githubStatus,
		forge.WithName("github.status"),
		forge.WithSummary("The organization's GitHub connection, and a link to connect another account"),
		forge.WithResponseSchema(http.StatusOK, "The connection", repositories.GitHubStatus{}),
	); err != nil {
		return err
	}
	if err := g.POST("/installations", e.githubConnect,
		forge.WithName("github.connect"),
		forge.WithSummary("Connect a GitHub App installation, from what GitHub's install redirect carried back"),
		forge.WithRequestSchema(repositories.ConnectGitHubInput{}),
		forge.WithResponseSchema(http.StatusCreated, "The connected installation", repositories.GitHubInstallation{}),
	); err != nil {
		return err
	}
	if err := g.DELETE("/installations/:id", e.githubDisconnect,
		forge.WithName("github.disconnect"),
		forge.WithSummary("Disconnect an installation. The App stays installed on GitHub."),
		forge.WithNoContentResponse(),
		forge.WithEntity(forge.EntityDef{Type: "GitHubInstallation", IDField: "id"}),
	); err != nil {
		return err
	}
	return g.GET("/repositories", e.githubRepositories,
		forge.WithName("github.repositories"),
		forge.WithSummary("The repositories the organization's installations can read"),
		forge.WithResponseSchema(http.StatusOK, "Repositories", ConnectedRepositoryList{}),
	)
}

func (e *Extension) githubStatus(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	st, err := e.github.Status(c.Request().Context(), p)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, st)
}

func (e *Extension) githubConnect(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in repositories.ConnectGitHubInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be JSON with installation_id, code and state")
	}
	inst, err := e.github.Connect(c.Request().Context(), p, in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusCreated, inst)
}

func (e *Extension) githubDisconnect(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err := e.github.Disconnect(c.Request().Context(), p, c.Param("id")); err != nil {
		return fail(err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (e *Extension) githubRepositories(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	repos, err := e.github.Repositories(c.Request().Context(), p)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, ConnectedRepositoryList{Repositories: repos})
}
