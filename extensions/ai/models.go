package ai

import (
	"context"
	"net/http"
	"strconv"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/aisettings"
	"github.com/xraph/loomplane/internal/models"
	"github.com/xraph/loomplane/internal/security"
)

// modelsAPI is what the model routes ask of the models service.
// *models.Service is the real one; every method checks the principal's
// permissions and organization itself. Endpoints, which hands out model
// keys, is not here: it's for the gateway inside Loomplane only, and no
// route serves it.
type modelsAPI interface {
	Registry(ctx context.Context, p security.Principal, q models.RegistryQuery) (*models.RegistryList, error)
	Entry(ctx context.Context, p security.Principal, id string) (*models.EntryDetail, error)
	Card(ctx context.Context, p security.Principal, id string) (*models.Card, error)
	Inspect(ctx context.Context, p security.Principal, repo, revision string) (*models.EntryDetail, error)
	List(ctx context.Context, p security.Principal) ([]*models.Status, error)
	Preview(ctx context.Context, p security.Principal, projectID, environmentID string, in models.DeployInput) (*models.DeployPreview, error)
	Deploy(ctx context.Context, p security.Principal, projectID, environmentID string, in models.DeployInput) (*models.DeployResult, error)
	Status(ctx context.Context, p security.Principal, projectID, environmentID, resource string) (*models.Status, error)
	Update(ctx context.Context, p security.Principal, projectID, environmentID, resource string, in models.UpdateInput) (*models.Status, error)
	MovePreview(ctx context.Context, p security.Principal, projectID, environmentID, resource string, in models.MoveInput) (*models.DeployPreview, error)
	Move(ctx context.Context, p security.Principal, projectID, environmentID, resource string, in models.MoveInput) (*models.DeployResult, error)
	Retry(ctx context.Context, p security.Principal, projectID, environmentID, resource string) (*models.Status, error)
	CancelWaiting(ctx context.Context, p security.Principal, projectID, environmentID, resource string) (*models.CancelResult, error)
	Remove(ctx context.Context, p security.Principal, projectID, environmentID, resource string) (*models.DeployResult, error)
}

// settingsAPI is what the AI settings routes ask of the settings service.
// *aisettings.Service is the real one.
type settingsAPI interface {
	Get(ctx context.Context, p security.Principal) (*aisettings.View, error)
	PutHuggingFaceToken(ctx context.Context, p security.Principal, token string) (*aisettings.HuggingFaceToken, error)
	DeleteHuggingFaceToken(ctx context.Context, p security.Principal) error
}

var (
	_ modelsAPI   = (*models.Service)(nil)
	_ settingsAPI = (*aisettings.Service)(nil)
)

// DeployInput is the body of a deploy and its preview: models.DeployInput
// without Moving, which only a move sets and no request may.
type DeployInput struct {
	// EntryID is a registry entry, or a model inspected from Hugging Face
	// ("hf:<repo>@<first 12 of the revision>").
	EntryID string `json:"entry_id"`
	// Quantization is one of the entry's; empty picks the best that fits
	// the target.
	Quantization string              `json:"quantization,omitempty"`
	Target       models.TargetChoice `json:"target"`
	// Resource is the manifest resource's name; empty is the entry's
	// default name.
	Resource string `json:"resource,omitempty"`
	// MaxModelLen is the context to serve; 0 is the largest that fits, at
	// most 32768.
	MaxModelLen      int    `json:"max_model_len,omitempty"`
	MonthlyBudgetUSD string `json:"monthly_budget_usd,omitempty"`
	// ReviewServer asks a preview to review the GPU server a server size
	// target creates. Only an admin may, and it records the review in
	// Infrastructure, so send it from an explicit "Review server" step.
	ReviewServer bool `json:"review_server,omitempty"`
	// Server approves the reviewed GPU server: the cloud_id, id and review
	// of the preview's server.
	Server *models.ServerApproval `json:"server,omitempty"`
}

// input is the service's DeployInput, with Moving never set.
func (in DeployInput) input() models.DeployInput {
	return models.DeployInput{
		EntryID: in.EntryID, Quantization: in.Quantization, Target: in.Target, Resource: in.Resource,
		MaxModelLen: in.MaxModelLen, MonthlyBudgetUSD: in.MonthlyBudgetUSD, ReviewServer: in.ReviewServer, Server: in.Server,
	}
}

// InspectInput names a Hugging Face model to inspect.
type InspectInput struct {
	// Repo is the model's ID on Hugging Face, like "Qwen/Qwen2.5-7B-Instruct".
	Repo string `json:"repo"`
	// Revision is a branch, a tag or a commit; empty is main. The model is
	// pinned to the commit it names.
	Revision string `json:"revision,omitempty"`
}

// EntryQuery names one registry entry. Entry IDs hold a "/", so they go in
// the query rather than the path.
type EntryQuery struct {
	ID string `query:"id"`
}

// HuggingFaceTokenInput is the workspace's new Hugging Face token. It's
// written to Vault and never sent back.
type HuggingFaceTokenInput struct {
	Token string `json:"token"`
}

// ModelDeploymentList is every model the caller can read.
type ModelDeploymentList struct {
	Models []*models.Status `json:"models"`
}

// ForgeEnvelope tells the generated client the list wraps Models.
func (ModelDeploymentList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "models"}
}

// Descriptions of the deploy and move routes and their previews, which
// say what a server size target needs.
const (
	previewDescription = "Writes nothing. A server size target is priced from its list price and comes back with " +
		"server_review_needed and can_deploy false. Send the same body with review_server true to review the GPU " +
		"server: only an admin may, and it records the review in Infrastructure and returns it as server, priced by " +
		"the provider. Deploy then sends server set from it within five minutes."
	moveDescription = "Writes nothing, except the review of a GPU server when review_server is true, which only an " +
		"admin may ask for. The rules are a deploy preview's."
	deployDescription = "For a server size target, send server with the cloud_id, operation_id and review of the GPU " +
		"server a preview with review_server true returned, within five minutes of that review. The deployment then " +
		"waits for the server, and Loomplane plans the model onto it once it's up."
)

// mountModels serves the model registry, model deployments and the AI
// settings the models need, under /api/v1 behind guard.
func mountModels(r forge.Router, guard identity.Guard, m modelsAPI, s settingsAPI) error {
	h := modelHandlers{models: m, settings: s}
	const env = "/projects/:projectId/environments/:environmentId/ai/models"
	type route struct {
		method, path string
		handler      forge.Handler
		opts         []forge.RouteOption
	}
	modelRoutes := []route{
		{http.MethodGet, "/ai/models/registry", h.registry, []forge.RouteOption{
			forge.WithName("aiModels.registry"),
			forge.WithSummary("The models Loomplane can deploy, filtered by task, size, license, fit and whether they were tested"),
			forge.WithQuerySchema(models.RegistryQuery{}),
			forge.WithResponseSchema(http.StatusOK, "The entries and their counts by task", models.RegistryList{}),
		}},
		{http.MethodGet, "/ai/models/registry/entry", h.entry, []forge.RouteOption{
			forge.WithName("aiModels.entry"),
			forge.WithSummary("One model with its quantizations and where each fits"),
			forge.WithQuerySchema(EntryQuery{}),
			forge.WithResponseSchema(http.StatusOK, "The model", models.EntryDetail{}),
		}},
		{http.MethodGet, "/ai/models/registry/card", h.card, []forge.RouteOption{
			forge.WithName("aiModels.card"),
			forge.WithSummary("A model's card from Hugging Face, as Markdown"),
			forge.WithQuerySchema(EntryQuery{}),
			forge.WithResponseSchema(http.StatusOK, "The card", models.Card{}),
		}},
		{http.MethodPost, "/ai/models/inspect", h.inspect, []forge.RouteOption{
			forge.WithName("aiModels.inspect"),
			forge.WithSummary("Read any Hugging Face model, pin it to a commit and work out where it fits"),
			forge.WithRequestSchema(InspectInput{}),
			forge.WithResponseSchema(http.StatusOK, "The model as Loomplane would deploy it", models.EntryDetail{}),
		}},
		{http.MethodGet, "/ai/models/deployments", h.list, []forge.RouteOption{
			forge.WithName("aiModelDeployments.list"),
			forge.WithSummary("Every model deployed in the projects you can read, with its state and cost"),
			forge.WithResponseSchema(http.StatusOK, "The models", ModelDeploymentList{}),
		}},
		{http.MethodPost, env + "/preview", h.preview, []forge.RouteOption{
			forge.WithName("aiModels.preview"),
			forge.WithSummary("What deploying a model in this environment would do, what it costs and what stops it"),
			forge.WithDescription(previewDescription),
			forge.WithRequestSchema(DeployInput{}),
			forge.WithResponseSchema(http.StatusOK, "The preview", models.DeployPreview{}),
		}},
		{http.MethodPost, env, h.deploy, []forge.RouteOption{
			forge.WithName("aiModels.deploy"),
			forge.WithSummary("Deploy a model in this environment: add it to the manifest and plan the environment, or create its GPU server first"),
			forge.WithDescription(deployDescription),
			forge.WithRequestSchema(DeployInput{}),
			forge.WithResponseSchema(http.StatusCreated, "The deployment and its plan", models.DeployResult{}),
			forge.WithResponseSchema(http.StatusUnprocessableEntity, "Why it can't be deployed", models.Problem{}),
		}},
		{http.MethodGet, env + "/:resource", h.get, []forge.RouteOption{
			forge.WithName("aiModelDeployments.get"),
			forge.WithSummary("A model's state, cost, budget and the services that link to it"),
			forge.WithResponseSchema(http.StatusOK, "The model", models.Status{}),
		}},
		{http.MethodPatch, env + "/:resource", h.update, []forge.RouteOption{
			forge.WithName("aiModelDeployments.update"),
			forge.WithSummary("Change a model's monthly budget or its hourly price, without changing what runs"),
			forge.WithRequestSchema(models.UpdateInput{}),
			forge.WithResponseSchema(http.StatusOK, "The model", models.Status{}),
		}},
		{http.MethodPost, env + "/:resource/move-preview", h.movePreview, []forge.RouteOption{
			forge.WithName("aiModelDeployments.movePreview"),
			forge.WithSummary("What moving a model to another server, cluster or new GPU server would do"),
			forge.WithDescription(moveDescription),
			forge.WithRequestSchema(models.MoveInput{}),
			forge.WithResponseSchema(http.StatusOK, "The preview", models.DeployPreview{}),
		}},
		{http.MethodPost, env + "/:resource/move", h.move, []forge.RouteOption{
			forge.WithName("aiModelDeployments.move"),
			forge.WithSummary("Move a model to another server, cluster or new GPU server and plan its environment"),
			forge.WithDescription(deployDescription),
			forge.WithRequestSchema(models.MoveInput{}),
			forge.WithResponseSchema(http.StatusOK, "The deployment and its plan", models.DeployResult{}),
			forge.WithResponseSchema(http.StatusUnprocessableEntity, "Why it can't be moved", models.Problem{}),
		}},
		{http.MethodPost, env + "/:resource/retry", h.retry, []forge.RouteOption{
			forge.WithName("aiModelDeployments.retry"),
			forge.WithSummary("Try a model that failed to start, or whose GPU server needs attention, again"),
			forge.WithResponseSchema(http.StatusOK, "The model", models.Status{}),
		}},
		{http.MethodDelete, env + "/:resource/waiting", h.cancel, []forge.RouteOption{
			forge.WithName("aiModelDeployments.cancel"),
			forge.WithSummary("Stop a model that's waiting for its GPU server; a server already approved stays in Infrastructure"),
			forge.WithResponseSchema(http.StatusOK, "What's left behind", models.CancelResult{}),
		}},
		{http.MethodDelete, env + "/:resource", h.remove, []forge.RouteOption{
			forge.WithName("aiModelDeployments.remove"),
			forge.WithSummary("Take a model out of the manifest and plan its environment; its weights stay on the server"),
			forge.WithResponseSchema(http.StatusOK, "The plan, and what stays behind", models.DeployResult{}),
		}},
	}
	settingRoutes := []route{
		{http.MethodGet, "/ai/settings", h.getSettings, []forge.RouteOption{
			forge.WithName("aiSettings.get"),
			forge.WithSummary("The workspace's AI settings, without any secret, and whether you can change them"),
			forge.WithResponseSchema(http.StatusOK, "The settings", aisettings.View{}),
		}},
		{http.MethodPut, "/ai/settings/huggingface-token", h.putToken, []forge.RouteOption{
			forge.WithName("aiSettings.putHuggingFaceToken"),
			forge.WithSummary("Check a Hugging Face token with Hugging Face and save it as the workspace's; it's never shown again"),
			forge.WithRequestSchema(HuggingFaceTokenInput{}),
			forge.WithResponseSchema(http.StatusOK, "The token's hint and account", aisettings.HuggingFaceToken{}),
		}},
		{http.MethodDelete, "/ai/settings/huggingface-token", h.deleteToken, []forge.RouteOption{
			forge.WithName("aiSettings.deleteHuggingFaceToken"),
			forge.WithSummary("Delete the workspace's Hugging Face token, unless a deployed gated model downloads with it"),
			forge.WithNoContentResponse(),
		}},
	}
	for _, grp := range []struct {
		tag    string
		routes []route
	}{{"AI models", modelRoutes}, {"AI", settingRoutes}} {
		g := r.Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags(grp.tag))
		for _, rt := range grp.routes {
			var err error
			switch rt.method {
			case http.MethodGet:
				err = g.GET(rt.path, rt.handler, rt.opts...)
			case http.MethodPost:
				err = g.POST(rt.path, rt.handler, rt.opts...)
			case http.MethodPut:
				err = g.PUT(rt.path, rt.handler, rt.opts...)
			case http.MethodPatch:
				err = g.PATCH(rt.path, rt.handler, rt.opts...)
			case http.MethodDelete:
				err = g.DELETE(rt.path, rt.handler, rt.opts...)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// modelHandlers serve the model and AI settings routes.
type modelHandlers struct {
	models   modelsAPI
	settings settingsAPI
}

func principal(c forge.Context) (security.Principal, error) {
	p, ok := security.PrincipalFrom(c.Request().Context())
	if !ok {
		return p, refuse(http.StatusUnauthorized, codeUnauthenticated, security.ErrUnauthenticated.Error())
	}
	return p, nil
}

// bind reads a JSON body into v. Its error never repeats the body, which
// may hold a token.
func bind(c forge.Context, v any, shape string) error {
	if err := c.BindJSON(v); err != nil {
		return refuse(http.StatusBadRequest, codeInvalid, "The body must be JSON: "+shape+".")
	}
	return nil
}

// at is a route's project, environment and resource.
type at struct{ project, environment, resource string }

func where(c forge.Context) at {
	return at{c.Param("projectId"), c.Param("environmentId"), c.Param("resource")}
}

// answer writes out with status, or the mapped error.
func answer(c forge.Context, status int, out any, err error) error {
	if err != nil {
		return fail(err)
	}
	return c.JSON(status, out)
}

func (h modelHandlers) registry(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	q, err := registryQuery(c)
	if err != nil {
		return err
	}
	out, err := h.models.Registry(c.Request().Context(), p, q)
	return answer(c, http.StatusOK, out, err)
}

// registryQuery reads the registry's filters. A flag that isn't true or
// false is a bad request.
func registryQuery(c forge.Context) (models.RegistryQuery, error) {
	v := c.Request().URL.Query()
	q := models.RegistryQuery{Q: v.Get("q"), Task: v.Get("task"), Size: v.Get("size"), License: v.Get("license")}
	for name, into := range map[string]*bool{"commercial": &q.Commercial, "fits": &q.Fits, "tested": &q.Tested} {
		raw := v.Get(name)
		if raw == "" {
			continue
		}
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return q, refuse(http.StatusUnprocessableEntity, codeInvalid, name+" is true or false")
		}
		*into = b
	}
	return q, nil
}

// entryID is the id query parameter, which a registry entry needs.
func entryID(c forge.Context) (string, error) {
	id := c.Request().URL.Query().Get("id")
	if id == "" {
		return "", refuse(http.StatusUnprocessableEntity, codeInvalid, "Name the model with ?id=, like ?id=Qwen/Qwen2.5-7B-Instruct")
	}
	return id, nil
}

func (h modelHandlers) entry(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	id, err := entryID(c)
	if err != nil {
		return err
	}
	out, err := h.models.Entry(c.Request().Context(), p, id)
	return answer(c, http.StatusOK, out, err)
}

func (h modelHandlers) card(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	id, err := entryID(c)
	if err != nil {
		return err
	}
	out, err := h.models.Card(c.Request().Context(), p, id)
	return answer(c, http.StatusOK, out, err)
}

func (h modelHandlers) inspect(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in InspectInput
	if berr := bind(c, &in, `{"repo": "owner/name", "revision": "main"}`); berr != nil {
		return berr
	}
	out, err := h.models.Inspect(c.Request().Context(), p, in.Repo, in.Revision)
	return answer(c, http.StatusOK, out, err)
}

func (h modelHandlers) list(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	rows, err := h.models.List(c.Request().Context(), p)
	if rows == nil {
		rows = []*models.Status{}
	}
	return answer(c, http.StatusOK, ModelDeploymentList{Models: rows}, err)
}

// deployInput reads a deploy's body. Moving is never read from it.
func deployInput(c forge.Context) (models.DeployInput, error) {
	var in DeployInput
	if err := bind(c, &in, `{"entry_id": "...", "target": {...}}`); err != nil {
		return models.DeployInput{}, err
	}
	return in.input(), nil
}

func (h modelHandlers) preview(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	in, err := deployInput(c)
	if err != nil {
		return err
	}
	w := where(c)
	out, err := h.models.Preview(c.Request().Context(), p, w.project, w.environment, in)
	return answer(c, http.StatusOK, out, err)
}

func (h modelHandlers) deploy(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	in, err := deployInput(c)
	if err != nil {
		return err
	}
	w := where(c)
	out, err := h.models.Deploy(c.Request().Context(), p, w.project, w.environment, in)
	return answer(c, http.StatusCreated, out, err)
}

func (h modelHandlers) get(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	w := where(c)
	out, err := h.models.Status(c.Request().Context(), p, w.project, w.environment, w.resource)
	return answer(c, http.StatusOK, out, err)
}

func (h modelHandlers) update(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in models.UpdateInput
	if berr := bind(c, &in, `{"monthly_budget_usd": "250.00", "revision": 3}`); berr != nil {
		return berr
	}
	w := where(c)
	out, err := h.models.Update(c.Request().Context(), p, w.project, w.environment, w.resource, in)
	return answer(c, http.StatusOK, out, err)
}

func (h modelHandlers) movePreview(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in models.MoveInput
	if berr := bind(c, &in, `{"target": {...}}`); berr != nil {
		return berr
	}
	w := where(c)
	out, err := h.models.MovePreview(c.Request().Context(), p, w.project, w.environment, w.resource, in)
	return answer(c, http.StatusOK, out, err)
}

func (h modelHandlers) move(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in models.MoveInput
	if berr := bind(c, &in, `{"target": {...}}`); berr != nil {
		return berr
	}
	w := where(c)
	out, err := h.models.Move(c.Request().Context(), p, w.project, w.environment, w.resource, in)
	return answer(c, http.StatusOK, out, err)
}

func (h modelHandlers) retry(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	w := where(c)
	out, err := h.models.Retry(c.Request().Context(), p, w.project, w.environment, w.resource)
	return answer(c, http.StatusOK, out, err)
}

func (h modelHandlers) cancel(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	w := where(c)
	out, err := h.models.CancelWaiting(c.Request().Context(), p, w.project, w.environment, w.resource)
	return answer(c, http.StatusOK, out, err)
}

func (h modelHandlers) remove(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	w := where(c)
	out, err := h.models.Remove(c.Request().Context(), p, w.project, w.environment, w.resource)
	return answer(c, http.StatusOK, out, err)
}

func (h modelHandlers) getSettings(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	out, err := h.settings.Get(c.Request().Context(), p)
	return answer(c, http.StatusOK, out, err)
}

func (h modelHandlers) putToken(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in HuggingFaceTokenInput
	if berr := bind(c, &in, `{"token": "hf_..."}`); berr != nil {
		return berr
	}
	out, err := h.settings.PutHuggingFaceToken(c.Request().Context(), p, in.Token)
	return answer(c, http.StatusOK, out, err)
}

func (h modelHandlers) deleteToken(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err = h.settings.DeleteHuggingFaceToken(c.Request().Context(), p); err != nil {
		return fail(err)
	}
	return c.NoContent(http.StatusNoContent)
}
