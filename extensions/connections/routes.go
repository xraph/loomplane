package connections

import (
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider/sdk"
)

// ConnectionList is the body of GET /api/v1/connections.
type ConnectionList struct {
	Connections []connections.Connection `json:"connections"`
}

// ForgeEnvelope tells the generated client the list wraps Connections.
func (ConnectionList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "connections"}
}

// CloudForm is a cloud you can connect, and what connecting it asks for.
// Built-in clouds have forms of their own in the dashboard; a provider
// plugin's cloud is described by the plugin.
type CloudForm struct {
	Cloud      string        `json:"cloud"`
	Title      string        `json:"title"`
	Builtin    bool          `json:"builtin"`
	Credential string        `json:"credential,omitempty"`
	Help       string        `json:"help,omitempty"`
	Settings   []sdk.Setting `json:"settings,omitempty"`
}

// CloudList is the body of GET /api/v1/connection-clouds.
type CloudList struct {
	Clouds []CloudForm `json:"clouds"`
}

func (e *Extension) clouds() (CloudList, error) {
	out := CloudList{Clouds: []CloudForm{
		{Cloud: connections.GCP, Title: "Google Cloud", Builtin: true},
		{Cloud: connections.Vercel, Title: "Vercel", Builtin: true},
		{Cloud: connections.AWS, Title: "AWS", Builtin: true},
		{Cloud: connections.Server, Title: "SSH server", Credential: "SSH private key", //nolint:gosec // Form label, not a credential.
			Help:     "Use a PEM private key, or JSON with private_key and passphrase. Your SSH user needs access to an existing Linux Docker daemon. Copy the server's public host key from a trusted source.",
			Settings: []sdk.Setting{{Key: "host", Label: "SSH host", Required: true}, {Key: "port", Label: "SSH port (default 22)"}, {Key: "user", Label: "SSH user", Required: true}, {Key: "host_key", Label: "Public host key", Required: true}, {Key: "docker_socket", Label: "Docker socket (default /var/run/docker.sock)"}, {Key: "advertise_host", Label: "Published address (optional)"}}},
		{Cloud: connections.Kubernetes, Title: "Kubernetes", Credential: "Self-contained kubeconfig",
			Help:     "Use an HTTPS API with an embedded CA and token or client certificate. The namespace must already exist. File references and executable authentication plugins are refused. For private images, select an existing namespace pull Secret that you manage. Persistent services need a retained storage class and an explicit amd64 or arm64 architecture.",
			Settings: []sdk.Setting{{Key: "context", Label: "Context (optional)"}, {Key: "namespace", Label: "Namespace (defaults to kubeconfig)"}, {Key: "image_pull_secret", Label: "Existing image pull Secret (optional)"}, {Key: "storage_class", Label: "Retained storage class (optional)"}, {Key: "architecture", Label: "Persistent service architecture (amd64 or arm64)"}}},
		{Cloud: connections.Registry, Title: "Image registry", Credential: "Credential JSON", //nolint:gosec // Form label, not a credential.
			Help:     "Use username/password or token JSON, with optional ca PEM. Use {} for anonymous access. HTTPS is required outside loopback. A separate token service needs an approved auth endpoint. Docker publishing and server pulls use bearer tokens; Basic-only registries need a token service. Kubernetes pulls use your existing namespace Secret.",
			Settings: []sdk.Setting{{Key: "endpoint", Label: "Registry endpoint", Required: true}, {Key: "repository", Label: "Repository prefix", Required: true}, {Key: "auth_endpoint", Label: "Token service endpoint (optional)"}}},
		// Render's form is the plain one plugins get: a key and a setting.
		{
			Cloud: connections.Render, Title: "Render", Credential: "API key",
			Help:     "Create one in Render under Account settings, API keys. Render builds from your repositories through its own GitHub connection, so give it access to them there too. The workspace ID is only needed when the key reaches more than one.",
			Settings: []sdk.Setting{{Key: "owner_id", Label: "Workspace ID"}},
		},
	}}
	plugs, err := e.plugins.Plugins()
	if err != nil {
		return out, err
	}
	seen := map[string]bool{}
	for _, form := range out.Clouds {
		seen[form.Cloud] = true
	}
	for _, p := range plugs {
		if seen[p.Cloud()] {
			continue
		}
		seen[p.Cloud()] = true
		f := p.Description.Connection
		out.Clouds = append(out.Clouds, CloudForm{
			Cloud: p.Cloud(), Title: f.Title, Credential: f.Credential, Help: f.Help, Settings: f.Settings,
		})
	}
	return out, nil
}

func (e *Extension) routes(r forge.Router, guard identity.Guard) error {
	g := r.Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Cloud connections"))
	if err := g.GET("/connection-clouds", func(c forge.Context) error {
		list, err := e.clouds()
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, list)
	},
		forge.WithName("connectionClouds.list"),
		forge.WithSummary("The clouds you can connect, built in or from a provider plugin, and what each asks for"),
		forge.WithResponseSchema(http.StatusOK, "Clouds", CloudList{}),
	); err != nil {
		return err
	}
	if err := g.GET("/connections", e.list,
		forge.WithName("connections.list"),
		forge.WithSummary("The organization's cloud connections, never their credentials"),
		forge.WithResponseSchema(http.StatusOK, "Connections", ConnectionList{}),
	); err != nil {
		return err
	}
	if err := g.POST("/connections", e.create,
		forge.WithName("connections.create"),
		forge.WithSummary("Connect a Google Cloud project or a Vercel account, checking the credential with the cloud first"),
		forge.WithRequestSchema(connections.CreateInput{}),
		forge.WithResponseSchema(http.StatusCreated, "The connection", connections.Connection{}),
	); err != nil {
		return err
	}
	if err := g.GET("/connections/:id", e.get,
		forge.WithName("connections.get"),
		forge.WithSummary("One cloud connection"),
		forge.WithResponseSchema(http.StatusOK, "The connection", connections.Connection{}),
	); err != nil {
		return err
	}
	if err := g.POST("/connections/:id/check", e.recheck,
		forge.WithName("connections.check"),
		forge.WithSummary("Ask the cloud again what the connection's credential may do"),
		forge.WithResponseSchema(http.StatusOK, "The connection", connections.Connection{}),
	); err != nil {
		return err
	}
	if err := g.GET("/connections/:id/storage", e.storage,
		forge.WithName("connectionStorage.list"),
		forge.WithSummary("Owned persistent volumes and current attachment state"),
		forge.WithResponseSchema(http.StatusOK, "Persistent storage", StorageVolumeList{}),
	); err != nil {
		return err
	}
	if err := g.PUT("/connections/:id/credential", e.replace,
		forge.WithName("connections.replace"),
		forge.WithSummary("Replace the connection's credential, such as with a rotated key"),
		forge.WithRequestSchema(connections.ReplaceInput{}),
		forge.WithResponseSchema(http.StatusOK, "The connection", connections.Connection{}),
	); err != nil {
		return err
	}
	return g.DELETE("/connections/:id", e.delete,
		forge.WithName("connections.delete"),
		forge.WithSummary("Remove a cloud connection and its credential"),
		forge.WithNoContentResponse(),
		forge.WithEntity(forge.EntityDef{Type: "Connection", IDField: "id"}),
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
	list, err := e.svc.List(c.Request().Context(), p)
	if err != nil {
		return fail(err)
	}
	out := ConnectionList{Connections: make([]connections.Connection, 0, len(list))}
	for _, conn := range list {
		out.Connections = append(out.Connections, *conn)
	}
	return c.JSON(http.StatusOK, out)
}

func (e *Extension) create(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in connections.CreateInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"cloud": "gcp", "name": "...", "credential": "..."}`)
	}
	conn, err := e.svc.Create(c.Request().Context(), p, in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusCreated, conn)
}

func (e *Extension) get(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	conn, err := e.svc.Get(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, conn)
}

func (e *Extension) recheck(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	conn, err := e.svc.Recheck(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, conn)
}

func (e *Extension) replace(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in connections.ReplaceInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"credential": "..."}`)
	}
	conn, err := e.svc.Replace(c.Request().Context(), p, c.Param("id"), in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, conn)
}

func (e *Extension) delete(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err = e.svc.Delete(c.Request().Context(), p, c.Param("id")); err != nil {
		return fail(err)
	}
	return c.NoContent(http.StatusNoContent)
}
