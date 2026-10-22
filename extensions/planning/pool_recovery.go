package planning

import (
	"errors"
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/security"
)

func (e *Extension) poolRecoveryRoutes(r forge.Router, guard identity.Guard, svc *cloudinfra.Service) error {
	g := r.Group("/api/v1/clouds/:cloudId", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Cloud infrastructure"))
	for _, apply := range []bool{false, true} {
		name, path := "cloudPlacements.previewRecovery", "/placements/preview-recovery"
		if apply {
			name, path = "cloudPlacements.recover", "/placements/recover"
		}
		handler := func(c forge.Context) error {
			p, ok := security.PrincipalFrom(c.Request().Context())
			if !ok {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Sign in to continue"})
			}
			var in cloudinfra.RecoveryInput
			if err := c.BindJSON(&in); err != nil {
				return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid recovery request"})
			}
			var v *cloudinfra.ReservationRecovery
			var err error
			if apply {
				v, err = svc.RecoverReservation(c.Request().Context(), p, c.Param("cloudId"), in)
			} else {
				v, err = svc.PreviewReservationRecovery(c.Request().Context(), p, c.Param("cloudId"), in)
			}
			if err != nil {
				if errors.Is(err, cloudinfra.ErrConflict) {
					return forge.NewHTTPError(http.StatusConflict, err.Error())
				}
				if errors.Is(err, cloudinfra.ErrNotFound) {
					return forge.NewHTTPError(http.StatusNotFound, "Reservation not found")
				}
				return fail(err)
			}
			return c.JSON(http.StatusOK, v)
		}
		if err := g.POST(path, handler, forge.WithName(name), forge.WithRequestSchema(cloudinfra.RecoveryInput{}), forge.WithResponseSchema(http.StatusOK, "Recovery", cloudinfra.ReservationRecovery{})); err != nil {
			return err
		}
	}
	return nil
}
