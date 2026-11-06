package aiagent

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/agentchat"
)

// platformFlagsBase is where the platform owner reads the answers people
// reported.
const platformFlagsBase = "/api/v1/platform/agent/flags"

// platformFlagsAPI is what the platform owner's flag routes ask of
// agentchat. *agentchat.Service is the real one.
type platformFlagsAPI interface {
	Flags(ctx context.Context, status string) ([]agentchat.Flag, error)
	DismissFlag(ctx context.Context, id, by string) (*agentchat.Flag, error)
}

var _ platformFlagsAPI = (*agentchat.Service)(nil)

// FlagList is the reported answers of every workspace, the newest first.
type FlagList struct {
	Flags []agentchat.Flag `json:"flags"`
}

// FlagQuery picks the reported answers listed.
type FlagQuery struct {
	// Status is open (the default), dismissed or all.
	Status string `json:"status,omitempty" query:"status,omitempty"`
}

// mountPlatformFlagRoutes serves the answers people reported to the
// platform owner, and only to them: identity.OwnerOnly gates every route,
// so anyone else, a workspace admin or the owner's own API key or agent
// included, gets the 404 an unknown route would. They aren't behind the
// scope binder: they span every workspace, whether its agent is on or
// not. Like the owner's billing routes, none of them is in the OpenAPI
// spec.
func mountPlatformFlagRoutes(r forge.Router, guard identity.Guard, isOwner identity.PlatformOwner, flags platformFlagsAPI) error {
	if isOwner == nil || flags == nil {
		return errors.New("the platform flag routes need identity's platform-owner check and the conversations")
	}
	h := platformFlagHandlers{flags: flags}
	g := r.Group(platformFlagsBase, forge.WithGroupMiddleware(identity.OwnerOnly(guard, isOwner)...), forge.WithGroupSchemaExclude())
	return errors.Join(
		g.GET("", h.list,
			forge.WithName("platformAgentFlags.list"),
			forge.WithSummary("The agent answers people reported, in every workspace, the newest first"),
			forge.WithDescription("Open reports by default; status=dismissed or status=all for the others. At most 500."),
			forge.WithQuerySchema(FlagQuery{}),
			forge.WithResponseSchema(http.StatusOK, "The reports", FlagList{}),
		),
		g.POST("/:id/dismiss", h.dismiss,
			forge.WithName("platformAgentFlags.dismiss"),
			forge.WithSummary("Dismiss a report once you've looked at it"),
			forge.WithDescription("Dismissing it again keeps the first dismissal."),
			forge.WithResponseSchema(http.StatusOK, "The report", agentchat.Flag{}),
		),
	)
}

type platformFlagHandlers struct {
	flags platformFlagsAPI
}

func (h platformFlagHandlers) list(c forge.Context) error {
	flags, err := h.flags.Flags(c.Request().Context(), c.Query("status"))
	if err != nil {
		return flagFail(c.Request().Context(), err)
	}
	return c.JSON(http.StatusOK, FlagList{Flags: flags})
}

func (h platformFlagHandlers) dismiss(c forge.Context) error {
	f, err := h.flags.DismissFlag(c.Request().Context(), c.Param("id"), identity.OwnerID(c))
	if err != nil {
		return flagFail(c.Request().Context(), err)
	}
	return c.JSON(http.StatusOK, f)
}

// msgFlagsFailed is what the owner reads when the reports can't be read
// or changed for a reason of the server's own.
const msgFlagsFailed = "Loomplane couldn't do that right now. Try again in a minute."

// flagFail maps the platform flag routes' errors: a status that isn't one
// 422 with what's wrong, an unknown flag 404. Anything else is logged and
// never sent.
func flagFail(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, agentchat.ErrBadFlag):
		return forge.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, agentchat.ErrFlagNotFound):
		return forge.NewHTTPError(http.StatusNotFound, agentchat.ErrFlagNotFound.Error())
	}
	slog.ErrorContext(ctx, "platform agent flags: a request failed", "error", err)
	return forge.NewHTTPError(http.StatusInternalServerError, msgFlagsFailed)
}
