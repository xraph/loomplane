package planning

import (
	"errors"
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/recovery"
)

type NativeRecoveryCopies struct {
	Copies     []recovery.NativeRestore `json:"copies"`
	NextBefore string                   `json:"next_before,omitempty"`
}

func (NativeRecoveryCopies) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "copies"}
}

func (e *Extension) nativeCopyRoutes(group forge.Router) error {
	if err := group.POST("/native-recovery-sources/:id/points/:pointId/copy-review", e.reviewNativeCopy, forge.WithName("nativeRecoveryCopies.review"), forge.WithRequestSchema(recovery.NativeCopyReviewInput{}), forge.WithResponseSchema(http.StatusOK, "Checked temporary native copy review", recovery.NativeCopyReview{})); err != nil {
		return err
	}
	if err := group.POST("/native-recovery-copies", e.createNativeCopy, forge.WithName("nativeRecoveryCopies.create"), forge.WithRequestSchema(recovery.NativeCopyCreateInput{}), forge.WithResponseSchema(http.StatusAccepted, "Admitted temporary native copy", recovery.NativeRestore{})); err != nil {
		return err
	}
	if err := group.GET("/projects/:id/native-recovery-copies", e.listNativeCopies, forge.WithName("nativeRecoveryCopies.list"), forge.WithQuerySchema(recovery.NativePageInput{}), forge.WithResponseSchema(http.StatusOK, "Retained native recovery copies", NativeRecoveryCopies{})); err != nil {
		return err
	}
	if err := group.GET("/native-recovery-copies/:id", e.getNativeCopy, forge.WithName("nativeRecoveryCopies.get"), forge.WithResponseSchema(http.StatusOK, "Native copy and cleanup status", recovery.NativeRestore{})); err != nil {
		return err
	}
	return group.DELETE("/native-recovery-copies/:id", e.cleanupNativeCopy, forge.WithName("nativeRecoveryCopies.cleanup"), forge.WithResponseSchema(http.StatusAccepted, "Requested temporary copy cleanup", recovery.NativeRestore{}))
}

func nativeCopyError(err error) error {
	if errors.Is(err, recovery.ErrNativeUnavailable) {
		return forge.NewHTTPError(http.StatusServiceUnavailable, err.Error())
	}
	return fail(err)
}

func (e *Extension) reviewNativeCopy(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in recovery.NativeCopyReviewInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "provide request_id, cloud_id, target_id and ttl_hours")
	}
	review, err := e.nativeBackups.ReviewCopy(c.Request().Context(), p, c.Param("id"), c.Param("pointId"), in)
	if err != nil {
		return nativeCopyError(err)
	}
	return c.JSON(http.StatusOK, review)
}

func (e *Extension) createNativeCopy(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in recovery.NativeCopyCreateInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "provide request_id, review_token and acknowledge_charges")
	}
	r, err := e.nativeBackups.CreateCopy(c.Request().Context(), p, in)
	if err != nil {
		return nativeCopyError(err)
	}
	return c.JSON(http.StatusAccepted, r)
}

func (e *Extension) getNativeCopy(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	r, err := e.nativeBackups.GetCopy(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, r)
}

func (e *Extension) listNativeCopies(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	page, err := nativePage(c)
	if err != nil {
		return err
	}
	copies, err := e.nativeBackups.ListCopies(c.Request().Context(), p, c.Param("id"), page)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, NativeRecoveryCopies{Copies: copies.Copies, NextBefore: copies.NextBefore})
}

func (e *Extension) cleanupNativeCopy(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	r, err := e.nativeBackups.RequestCopyCleanup(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusAccepted, r)
}
