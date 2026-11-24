package planning

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
)

// A workspace service is a registered external endpoint: a database,
// cache or bucket the workspace connects to by its connection string and
// shares with its projects. These routes register, describe, check,
// rotate and share them. Each one that dials the endpoint is checked
// from a target connection the person names, counts against the same
// per-person limit as the dependency check, and is audited without its
// connection string.

// ServiceCheckInput checks a connection string before it's stored.
type ServiceCheckInput struct {
	// Engine is postgres, mysql, redis, mongodb, or a search or bucket
	// engine.
	Engine string `json:"engine"`
	// DSN is checked and dropped. It's never stored or returned.
	DSN string `json:"dsn"`
	// Target is the connection the check dials from.
	Target string `json:"target"`
}

// RegisterServiceInput registers a workspace service from where it was
// checked.
type RegisterServiceInput struct {
	catalogconnect.RegisterInput
	// Target is the connection the check dials from.
	Target string `json:"target"`
}

// ReplaceCredentialInput swaps a workspace service's credentials.
type ReplaceCredentialInput struct {
	DSN    string `json:"dsn"`
	Target string `json:"target"`
	// Revision is the revision the person read; a newer one refuses.
	Revision int `json:"revision"`
}

// AdminCredentialInput keeps an admin credential for a workspace service.
type AdminCredentialInput struct {
	DSN    string `json:"dsn"`
	Target string `json:"target"`
}

// RecheckInput checks a stored workspace service again.
type RecheckInput struct {
	Target string `json:"target"`
}

// SharingInput sets who may use a workspace service.
type SharingInput struct {
	// Restricted limits it to Projects; unrestricted, every project in the
	// workspace may use it.
	Restricted bool     `json:"restricted"`
	Projects   []string `json:"projects"`
}

// ServiceUseList is the body of GET /catalog/endpoints/:id/uses.
type ServiceUseList struct {
	Uses []catalogconnect.Use `json:"uses"`
}

// ForgeEnvelope tells the generated client the list wraps Uses.
func (ServiceUseList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "uses"}
}

// OwnUserList is the body of GET /catalog/endpoints/:id/users.
type OwnUserList struct {
	Users []catalogconnect.OwnUser `json:"users"`
}

// ForgeEnvelope tells the generated client the list wraps Users.
func (OwnUserList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "users"}
}

// DropUserInput drops a user Loomplane made, and the database it made for
// it.
type DropUserInput struct {
	// Confirm repeats the user's name.
	Confirm string `json:"confirm"`
	// Target is the connection the drop dials from; empty uses the one the
	// workspace service was last checked from.
	Target string `json:"target"`
}

// errCheckFailed is a change refused because its check found problems.
var errCheckFailed = fmt.Errorf("%w: the check found problems", catalogconnect.ErrInvalid)

func (e *Extension) workspaceServiceRoutes() []route {
	return []route{
		{http.MethodGet, "/catalog/endpoints/:id", e.getCatalogEndpoint, []forge.RouteOption{
			forge.WithName("catalogEndpoints.get"),
			forge.WithSummary("One external endpoint or workspace service, without its credentials"),
			forge.WithResponseSchema(http.StatusOK, "The endpoint", catalogconnect.Endpoint{}),
		}},
		{http.MethodPost, "/catalog/endpoints/probes", e.probeWorkspaceService, []forge.RouteOption{
			forge.WithName("catalogEndpoints.probe"),
			forge.WithSummary("Check a connection string from a target, and what its credentials may do, before registering it; nothing is stored or returned"),
			forge.WithRequestSchema(ServiceCheckInput{}),
			forge.WithResponseSchema(http.StatusOK, "What the check found", catalogconnect.CheckResult{}),
		}},
		{http.MethodPost, "/catalog/endpoints/registrations", e.registerWorkspaceService, []forge.RouteOption{
			forge.WithName("catalogEndpoints.register"),
			forge.WithSummary("Register a workspace service by its connection string once it checks out, and share it with every project or only some; the connection string is kept in Vault and never returned"),
			forge.WithRequestSchema(RegisterServiceInput{}),
			forge.WithResponseSchema(http.StatusCreated, "The workspace service", catalogconnect.Endpoint{}),
		}},
		{http.MethodPatch, "/catalog/endpoints/:id", e.updateWorkspaceService, []forge.RouteOption{
			forge.WithName("catalogEndpoints.update"),
			forge.WithSummary("Rename a workspace service, or change its description or environment label"),
			forge.WithRequestSchema(catalogconnect.Details{}),
			forge.WithResponseSchema(http.StatusOK, "The workspace service", catalogconnect.Endpoint{}),
		}},
		{http.MethodPut, "/catalog/endpoints/:id/credential", e.replaceWorkspaceCredential, []forge.RouteOption{
			forge.WithName("catalogEndpoints.replaceCredential"),
			forge.WithSummary("Replace a workspace service's credentials once the new ones check out; services get them the next time they deploy"),
			forge.WithRequestSchema(ReplaceCredentialInput{}),
			forge.WithResponseSchema(http.StatusOK, "The workspace service", catalogconnect.Endpoint{}),
		}},
		{http.MethodPut, "/catalog/endpoints/:id/admin", e.setWorkspaceAdmin, []forge.RouteOption{
			forge.WithName("catalogEndpoints.setAdmin"),
			forge.WithSummary("Keep an admin credential for a workspace service, so Loomplane can create a database or a user for each service that connects"),
			forge.WithRequestSchema(AdminCredentialInput{}),
			forge.WithResponseSchema(http.StatusOK, "The workspace service", catalogconnect.Endpoint{}),
		}},
		{http.MethodDelete, "/catalog/endpoints/:id/admin", e.clearWorkspaceAdmin, []forge.RouteOption{
			forge.WithName("catalogEndpoints.clearAdmin"),
			forge.WithSummary("Forget a workspace service's admin credential"),
			forge.WithResponseSchema(http.StatusOK, "The workspace service", catalogconnect.Endpoint{}),
		}},
		{http.MethodPut, "/catalog/endpoints/:id/sharing", e.shareWorkspaceService, []forge.RouteOption{
			forge.WithName("catalogEndpoints.share"),
			forge.WithSummary("Share a workspace service with every project, or only with the ones listed"),
			forge.WithRequestSchema(SharingInput{}),
			forge.WithResponseSchema(http.StatusOK, "The workspace service", catalogconnect.Endpoint{}),
		}},
		{http.MethodPost, "/catalog/endpoints/:id/checks", e.recheckWorkspaceService, []forge.RouteOption{
			forge.WithName("catalogEndpoints.recheck"),
			forge.WithSummary("Check a workspace service again from a target, its admin credential too, and keep what the check found"),
			forge.WithRequestSchema(RecheckInput{}),
			forge.WithResponseSchema(http.StatusOK, "The workspace service", catalogconnect.Endpoint{}),
		}},
		{http.MethodGet, "/catalog/endpoints/:id/users", e.listWorkspaceServiceUsers, []forge.RouteOption{
			forge.WithName("catalogEndpoints.users"),
			forge.WithSummary("The users Loomplane made on a workspace service for services that get one of their own, with the database each was given; passwords are never returned"),
			forge.WithResponseSchema(http.StatusOK, "Users", OwnUserList{}),
		}},
		{http.MethodPost, "/catalog/endpoints/:id/users/:user/drops", e.dropWorkspaceServiceUser, []forge.RouteOption{
			forge.WithName("catalogEndpoints.dropUser"),
			forge.WithSummary("Drop a user Loomplane made that no service uses any more, and the database Loomplane made for it, with all its data"),
			forge.WithRequestSchema(DropUserInput{}),
			forge.WithResponseSchema(http.StatusOK, "The users left", OwnUserList{}),
		}},
		{http.MethodGet, "/catalog/endpoints/:id/uses", e.listWorkspaceServiceUses, []forge.RouteOption{
			forge.WithName("catalogEndpoints.uses"),
			forge.WithSummary("Which projects and environments connect to an endpoint"),
			forge.WithResponseSchema(http.StatusOK, "Uses", ServiceUseList{}),
		}},
	}
}

func (e *Extension) getCatalogEndpoint(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	endpoint, err := e.endpoints.Describe(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, endpoint)
}

// limited takes a check from p's allowance, or answers 429. The refusal
// is audited once per run of refusals.
func (e *Extension) limited(c forge.Context, p security.Principal, engine, dsn, target string) error {
	allowed, first := e.checks().allow(p)
	if allowed {
		return nil
	}
	if first {
		e.endpoints.RecordCheck(c.Request().Context(), p, catalogconnect.CheckInput{Engine: engine, DSN: dsn, Target: target}, catalogconnect.OutcomeRateLimited)
	}
	return forge.NewHTTPError(http.StatusTooManyRequests, fmt.Sprintf("Too many endpoint checks. You can check %d a minute; try again shortly.", checksPerMinute))
}

// probeDSN checks a connection string of engine from target, with what
// its credentials may do. A target that can't be reached is a problem in
// the result, never an error.
func (e *Extension) probeDSN(ctx context.Context, p security.Principal, engine, dsn, target string) (catalogconnect.CheckResult, error) {
	if strings.TrimSpace(target) == "" {
		return catalogconnect.CheckResult{}, fmt.Errorf("%w: choose the server or cluster the check runs from", catalogconnect.ErrInvalid)
	}
	dial, closer, err := e.endpointDialer.TargetDial(ctx, p.OrgID, target)
	if errors.Is(err, connections.ErrNotFound) {
		return catalogconnect.CheckResult{}, err
	}
	if err != nil {
		return catalogconnect.CheckResult{Problems: []string{"the target connection failed: " + err.Error()}}, nil
	}
	if closer != nil {
		defer func() { _ = closer.Close() }() // a check only reads
	}
	report, err := e.endpointChecks.CheckDSN(catalogconnect.WithCapabilities(ctx), dsn, recipes.Requirement{Engine: engine}, dial)
	if err != nil {
		return catalogconnect.CheckResult{}, err
	}
	result := report.Result()
	if cloud, cerr := e.endpointDialer.TargetCloud(ctx, p.OrgID, target); cerr == nil {
		if note := planning.ResourceCheckNote(cloud); note != "" {
			result.Notes = append(result.Notes, note)
		}
	}
	return result, nil
}

// lastCheck is what a passed check keeps.
func lastCheck(r catalogconnect.CheckResult, target string) *catalogconnect.LastCheck {
	return &catalogconnect.LastCheck{OK: r.OK, Version: r.Version, Problems: r.Problems, Notes: r.Notes, Target: target, Capabilities: r.Capabilities}
}

// auditedCheck runs probeDSN and audits it as a check.
func (e *Extension) auditedCheck(c forge.Context, p security.Principal, engine, dsn, target string) (catalogconnect.CheckResult, error) {
	ctx := c.Request().Context()
	if err := e.endpoints.AuthorizeCheck(ctx, p); err != nil {
		e.endpoints.RecordCheck(ctx, p, catalogconnect.CheckInput{Engine: engine, DSN: dsn, Target: target}, catalogconnect.OutcomeDenied)
		return catalogconnect.CheckResult{}, err
	}
	result, err := e.probeDSN(ctx, p, engine, dsn, target)
	outcome := catalogconnect.OutcomeSuccess
	switch {
	case errors.Is(err, catalogconnect.ErrInvalid):
		outcome = catalogconnect.OutcomeInvalid
	case err != nil || !result.OK:
		outcome = catalogconnect.OutcomeFailure
	}
	e.endpoints.RecordCheck(ctx, p, catalogconnect.CheckInput{Engine: engine, DSN: dsn, Target: target}, outcome)
	return result, err
}

// refusedCheck words a failed check as the reason a change didn't happen.
func refusedCheck(r catalogconnect.CheckResult) error {
	reasons := append(append([]string{}, r.Problems...), missingWords(r.Missing)...)
	if len(reasons) == 0 {
		return errCheckFailed
	}
	return fmt.Errorf("%w: %s", errCheckFailed, strings.Join(reasons, "; "))
}

func missingWords(missing []string) []string {
	out := make([]string, 0, len(missing))
	for _, m := range missing {
		out = append(out, "it lacks the "+m+" extension")
	}
	return out
}

func (e *Extension) probeWorkspaceService(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in ServiceCheckInput
	bindErr := c.BindJSON(&in)
	if limitErr := e.limited(c, p, in.Engine, in.DSN, in.Target); limitErr != nil {
		return limitErr
	}
	if bindErr != nil {
		e.endpoints.RecordCheck(c.Request().Context(), p, catalogconnect.CheckInput{}, catalogconnect.OutcomeInvalid)
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"engine": "postgres", "dsn": "postgres://...", "target": "..."}`)
	}
	result, err := e.auditedCheck(c, p, in.Engine, in.DSN, in.Target)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, result)
}

func (e *Extension) registerWorkspaceService(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in RegisterServiceInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"name": "...", "engine": "postgres", "dsn": "postgres://...", "target": "..."}`)
	}
	if limitErr := e.limited(c, p, in.Engine, in.DSN, in.Target); limitErr != nil {
		return limitErr
	}
	// The connection is checked again here, so what's stored is what
	// passed, whatever the dashboard showed.
	result, err := e.auditedCheck(c, p, in.Engine, in.DSN, in.Target)
	if err != nil {
		return fail(err)
	}
	if !result.OK {
		return fail(refusedCheck(result))
	}
	endpoint, err := e.endpoints.Register(c.Request().Context(), p, in.RegisterInput, lastCheck(result, in.Target))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusCreated, endpoint)
}

func (e *Extension) updateWorkspaceService(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in catalogconnect.Details
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"name": "...", "description": "...", "environment_label": "..."}`)
	}
	endpoint, err := e.endpoints.Update(c.Request().Context(), p, c.Param("id"), in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, endpoint)
}

func (e *Extension) replaceWorkspaceCredential(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in ReplaceCredentialInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"dsn": "...", "target": "...", "revision": 1}`)
	}
	ctx := c.Request().Context()
	current, err := e.endpoints.Describe(ctx, p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	if limitErr := e.limited(c, p, current.Engine, in.DSN, in.Target); limitErr != nil {
		return limitErr
	}
	result, err := e.auditedCheck(c, p, current.Engine, in.DSN, in.Target)
	if err != nil {
		return fail(err)
	}
	if !result.OK {
		return fail(refusedCheck(result))
	}
	endpoint, err := e.endpoints.Replace(ctx, p, current.ID, in.Revision, in.DSN, lastCheck(result, in.Target))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, endpoint)
}

func (e *Extension) setWorkspaceAdmin(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in AdminCredentialInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"dsn": "...", "target": "..."}`)
	}
	ctx := c.Request().Context()
	current, err := e.endpoints.Describe(ctx, p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	if limitErr := e.limited(c, p, current.Engine, in.DSN, in.Target); limitErr != nil {
		return limitErr
	}
	result, err := e.auditedCheck(c, p, current.Engine, in.DSN, in.Target)
	if err != nil {
		return fail(err)
	}
	if !result.OK {
		return fail(refusedCheck(result))
	}
	endpoint, err := e.endpoints.SetAdmin(ctx, p, current.ID, in.DSN, lastCheck(result, in.Target))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, endpoint)
}

func (e *Extension) clearWorkspaceAdmin(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	endpoint, err := e.endpoints.ClearAdmin(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, endpoint)
}

func (e *Extension) shareWorkspaceService(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in SharingInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"restricted": true, "projects": ["..."]}`)
	}
	endpoint, err := e.endpoints.Share(c.Request().Context(), p, c.Param("id"), in.Restricted, in.Projects)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, endpoint)
}

// recheckWorkspaceService checks the stored credentials again, and the
// admin credential when there is one, and keeps what it found, passed or
// not: a service that stopped answering should say so.
func (e *Extension) recheckWorkspaceService(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in RecheckInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"target": "..."}`)
	}
	ctx := c.Request().Context()
	current, err := e.endpoints.Describe(ctx, p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	if err = e.endpoints.AuthorizeCheck(ctx, p); err != nil {
		return fail(err)
	}
	if limitErr := e.limited(c, p, current.Engine, "", in.Target); limitErr != nil {
		return limitErr
	}
	_, dsn, err := e.endpoints.Resolve(ctx, p.OrgID, current.ID)
	if err != nil {
		return fail(err)
	}
	result, err := e.auditedCheck(c, p, current.Engine, dsn, in.Target)
	if err != nil {
		return fail(err)
	}
	check := *lastCheck(result, in.Target)
	if current.AdminSet {
		_, admin, aerr := e.endpoints.ResolveAdmin(ctx, p.OrgID, current.ID)
		if aerr != nil {
			return fail(aerr)
		}
		adminResult, aerr := e.probeDSN(ctx, p, current.Engine, admin, in.Target)
		if aerr != nil {
			return fail(aerr)
		}
		check.Admin = &catalogconnect.Capabilities{}
		if adminResult.OK && adminResult.Capabilities != nil {
			check.Admin = adminResult.Capabilities
		}
		if !adminResult.OK {
			check.OK = false
			for _, problem := range adminResult.Problems {
				check.Problems = append(check.Problems, "the admin credential: "+problem)
			}
		}
	}
	endpoint, err := e.endpoints.SaveCheck(ctx, p, current.ID, check)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, endpoint)
}

func (e *Extension) listWorkspaceServiceUses(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	uses, err := e.endpoints.Uses(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, ServiceUseList{Uses: uses})
}

// resourceEndpoints is the endpoint service as a provider resource uses
// it: the endpoints its Connect creates stay the resource's own, so the
// workspace doesn't edit, share or rotate them.
type resourceEndpoints struct{ *catalogconnect.Service }

func (r resourceEndpoints) Create(ctx context.Context, p security.Principal, in catalogconnect.CreateInput) (*catalogconnect.Endpoint, error) {
	return r.CreateForResource(ctx, p, in)
}

func (e *Extension) listWorkspaceServiceUsers(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	users, err := e.endpoints.ListOwnUsers(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, OwnUserList{Users: users})
}

// dropWorkspaceServiceUser drops a user Loomplane made, from the target the
// person names or the one the workspace service was last checked from.
func (e *Extension) dropWorkspaceServiceUser(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in DropUserInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"confirm": "<user>"}`)
	}
	ctx := c.Request().Context()
	current, err := e.endpoints.Describe(ctx, p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	target := strings.TrimSpace(in.Target)
	if target == "" && current.LastCheck != nil {
		target = current.LastCheck.Target
	}
	if target == "" {
		return fail(fmt.Errorf("%w: choose the server or cluster to reach %s from", catalogconnect.ErrInvalid, current.Name))
	}
	if e.endpointDialer == nil {
		return fail(errors.New("this Loomplane server can't reach workspace services"))
	}
	dial, closer, err := e.endpointDialer.TargetDial(ctx, p.OrgID, target)
	if err != nil {
		return fail(err)
	}
	if closer != nil {
		defer func() { _ = closer.Close() }() // the drop has finished with it
	}
	if err = e.endpoints.DropOwnUser(ctx, p, current.ID, c.Param("user"), strings.TrimSpace(in.Confirm), dial); err != nil {
		return fail(err)
	}
	users, err := e.endpoints.ListOwnUsers(ctx, p, current.ID)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, OwnUserList{Users: users})
}
