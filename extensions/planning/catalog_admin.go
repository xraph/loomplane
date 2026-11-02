package planning

import (
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/deployments"
)

// openCatalogAdmin opens a session on a composed resource's own admin.
// The session's URL is served by the catalog admin proxy, mounted outside
// the identity guard (see Register): its token is the credential.
func (e *Extension) openCatalogAdmin(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	session, err := e.catalogAdmin.Open(c.Request().Context(), p, deployments.ResourceRef{
		ProjectID: c.Param("projectId"), EnvironmentID: c.Param("environmentId"), Resource: c.Param("resource"),
	})
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusCreated, session)
}

// catalogAdminRoute is where the proxy serves sessions.
const catalogAdminRoute = "/catalog-admin/"
