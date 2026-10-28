package ai

import (
	"errors"
	"net/http"
	"strings"

	"github.com/xraph/loomplane/internal/aisettings"
	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/models"
	"github.com/xraph/loomplane/internal/models/hfhub"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/manifest"
)

// Codes of an AI route's refusal, beside the problem codes of a deploy
// (models.Problem*).
const (
	codeInvalid         = "invalid"
	codeInUse           = "in_use"
	codeGated           = "gated"
	codeRepinned        = "repinned"
	codeStale           = "stale"
	codeBusy            = "busy"
	codeNotFound        = "not_found"
	codeForbidden       = "forbidden"
	codeUnauthenticated = "unauthenticated"
	codeConflict        = "conflict"
	codeUnavailable     = "unavailable"
	codePlanFailed      = "plan_failed"
)

// Sentences for what Hugging Face itself answered. Its own text never
// reaches a response: the service turns the answers it expects into its
// own sentences, and these cover the rest.
const (
	hubUnavailable  = "Hugging Face didn't answer. Try again in a minute."
	hubUnauthorized = "Hugging Face didn't accept the workspace's Hugging Face token. Put a new one in AI settings."
	hubGated        = "This model is gated on Hugging Face, and the workspace's Hugging Face token hasn't been granted access to it. Accept its license on Hugging Face, then try again."
	hubNotFound     = "Hugging Face has no such model, or the workspace's Hugging Face token can't see it."
	hubTooLarge     = "A file Loomplane reads from this model on Hugging Face is over its size limit."
)

// apiError is an AI route's refusal: its status, and a body with a short
// code the page can act on and the service's sentence as the message, the
// shape of models.Problem.
type apiError struct {
	status int
	body   models.Problem
}

func (e *apiError) Error() string     { return e.body.Message }
func (e *apiError) StatusCode() int   { return e.status }
func (e *apiError) ResponseBody() any { return e.body }

func refuse(status int, code, message string) *apiError {
	return &apiError{status: status, body: models.Problem{Code: code, Message: message}}
}

// sentinelClass is a set of service errors that answer the same way.
type sentinelClass struct {
	status int
	code   string
	errs   []error
}

// sentinels map what the AI services, and the services they call, return
// to a status. The message is the service's sentence without the
// sentinel's own text in front.
var sentinels = []sentinelClass{
	{http.StatusUnprocessableEntity, codeInUse, []error{models.ErrInUse, aisettings.ErrInUse}},
	{http.StatusUnprocessableEntity, codeInvalid, []error{
		models.ErrInvalid, models.ErrInvalidQuery, aisettings.ErrInvalid, aisettings.ErrOverBudget, aisettings.ErrUnderAllocated,
		hfhub.ErrRefused, manifests.ErrInvalid, manifest.ErrInvalid, planning.ErrInvalid, planning.ErrNoManifest,
		cloudinfra.ErrInvalid, projects.ErrInvalid,
	}},
	{http.StatusConflict, codeStale, []error{
		models.ErrStale, aisettings.ErrStale, manifests.ErrConflict, planning.ErrStale, planning.ErrSuperseded, cloudinfra.ErrConflict,
	}},
	{http.StatusConflict, codeBusy, []error{planning.ErrConflict, cloudinfra.ErrBusy, cloudinfra.ErrPending}},
	{http.StatusConflict, codeConflict, []error{models.ErrConflict}},
	{http.StatusNotFound, codeNotFound, []error{
		models.ErrNotFound, models.ErrEntryNotFound, aisettings.ErrNotFound, projects.ErrNotFound, projects.ErrEnvironmentNotFound,
		manifests.ErrNotFound, planning.ErrNotFound, cloudinfra.ErrNotFound, cloudprofiles.ErrNotFound, deployments.ErrNotFound,
	}},
}

// fail maps what the AI services return to a refusal with the service's
// sentence, for the AI routes. Anything it doesn't know stays as it is,
// and Forge answers it with a bare 500, so no internal text reaches the
// response.
func fail(err error) error {
	if err == nil {
		return nil
	}
	if out := typed(err); out != nil {
		return out
	}
	var plan *models.PlanAfterEditError
	if errors.As(err, &plan) {
		return planFailed(plan)
	}
	if out := known(err); out != nil {
		return out
	}
	return err
}

// typed maps the services' error types, whose text is a sentence written
// for the page and holds nothing secret.
func typed(err error) *apiError {
	var (
		problem  *models.ProblemError
		gated    *models.GatedError
		access   *models.GatedAccessError
		base     *models.BaseGatedError
		repinned *models.RepinnedError
	)
	switch {
	case errors.As(err, &problem):
		return refuse(http.StatusUnprocessableEntity, problem.Problem.Code, problem.Problem.Message)
	case errors.As(err, &base):
		return refuse(http.StatusUnprocessableEntity, codeGated, base.Error())
	case errors.As(err, &gated):
		return refuse(http.StatusUnprocessableEntity, codeGated, gated.Error())
	case errors.As(err, &access):
		return refuse(http.StatusUnprocessableEntity, codeGated, access.Error())
	case errors.As(err, &repinned):
		return refuse(http.StatusUnprocessableEntity, codeRepinned, repinned.Error())
	}
	return nil
}

// known maps the sentinels, then what Hugging Face answered, then
// permissions. It returns nil for anything else.
func known(err error) *apiError {
	for _, class := range sentinels {
		for _, sentinel := range class.errs {
			if errors.Is(err, sentinel) {
				return refuse(class.status, class.code, sentence(err, sentinel))
			}
		}
	}
	switch {
	case errors.Is(err, hfhub.ErrUnavailable):
		return refuse(http.StatusBadGateway, codeUnavailable, hubUnavailable)
	case errors.Is(err, hfhub.ErrUnauthorized):
		return refuse(http.StatusUnprocessableEntity, codeInvalid, hubUnauthorized)
	case errors.Is(err, hfhub.ErrGated):
		return refuse(http.StatusUnprocessableEntity, codeGated, hubGated)
	case errors.Is(err, hfhub.ErrNotFound):
		return refuse(http.StatusNotFound, codeNotFound, hubNotFound)
	case errors.Is(err, hfhub.ErrTooLarge):
		return refuse(http.StatusUnprocessableEntity, codeInvalid, hubTooLarge)
	}
	switch status := security.StatusFor(err); status {
	case http.StatusUnauthorized:
		return refuse(status, codeUnauthenticated, err.Error())
	case http.StatusForbidden:
		return refuse(status, codeForbidden, err.Error())
	case http.StatusConflict:
		return refuse(status, codeConflict, err.Error())
	}
	return nil
}

// planFailed answers a change whose manifest edit went through and whose
// plan didn't.
func planFailed(plan *models.PlanAfterEditError) error {
	return planFailedBecause(plan.Error(), errors.Unwrap(plan))
}

// planFailedBecause answers msg, a PlanAfterEditError's sentence, which
// says to plan from the project and ends with ": " and inner's text. The
// inner text is always replaced: when the plan failed for a reason fail
// knows, by fail's own sentence for it, with its status, so what Hugging
// Face said never reaches the reply; otherwise it's left out, since it may
// be internal.
func planFailedBecause(msg string, inner error) error {
	if inner == nil {
		return refuse(http.StatusConflict, codePlanFailed, msg)
	}
	head := strings.TrimSuffix(msg, ": "+inner.Error())
	out := typed(inner)
	if out == nil {
		out = known(inner)
	}
	if out != nil {
		return refuse(out.status, codePlanFailed, head+": "+out.body.Message)
	}
	return refuse(http.StatusConflict, codePlanFailed, head+".")
}

// sentence is err's text without sentinel's in front: "invalid model
// deployment: a monthly budget is..." reads "a monthly budget is...".
func sentence(err, sentinel error) string {
	msg := err.Error()
	if rest, ok := strings.CutPrefix(msg, sentinel.Error()+": "); ok && rest != "" {
		return rest
	}
	return msg
}
