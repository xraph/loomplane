package cloudinfra

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"

	"github.com/xraph/loomplane/internal/security"
)

// RouteRemovalRequest is the routes a reviewed delete of one service or
// component removes before its runtime goes, as the plan saw them.
type RouteRemovalRequest struct {
	Key, ProjectID, EnvironmentID, Service string
	Routes                                 []Route
}

// RouteRemovalID is the operation that removes one route for a reviewed
// delete. key is the plan's ID and the action's, as "plan/action", so a
// retry of the delete finds the same operation.
func RouteRemovalID(key, route string) string {
	sum := sha256.Sum256([]byte(key + "/remove/" + route))
	return "routeop_" + hex.EncodeToString(sum[:16])
}

// RemoveReviewedRoutes removes each route a reviewed delete names, through
// the route workers, which remove its ingress and then its DNS record. A
// route that changed since review isn't touched. A retry resumes the
// removal it started rather than starting another. It returns ErrPending
// until every removal has succeeded.
func (s *Service) RemoveReviewedRoutes(ctx context.Context, p security.Principal, in RouteRemovalRequest) error {
	st, err := s.domainStore()
	if err != nil {
		return err
	}
	for _, reviewed := range in.Routes {
		if reviewed.OrgID != p.OrgID || reviewed.ProjectID != in.ProjectID || reviewed.Input.EnvironmentID != in.EnvironmentID || reviewed.Input.Service != in.Service {
			return ErrInvalid
		}
		id := RouteRemovalID(in.Key, reviewed.ID)
		op, readErr := st.RouteOperation(ctx, p.OrgID, id)
		if errors.Is(readErr, ErrNotFound) {
			current, getErr := st.Route(ctx, p.OrgID, in.ProjectID, reviewed.ID)
			if getErr != nil {
				return getErr
			}
			if !reflect.DeepEqual(*current, reviewed) {
				return fmt.Errorf("%w: %s changed since the delete was reviewed; plan again", ErrConflict, reviewed.Input.Hostname)
			}
			op, err = s.removeRoute(ctx, p, in.ProjectID, reviewed.ID, reviewed.Revision, id)
		} else {
			err = readErr
		}
		if err != nil {
			return err
		}
		if op.Kind != "remove" || op.Route.ID != reviewed.ID {
			return ErrConflict
		}
		if reconcileErr := s.reconcileReleaseOperation(ctx, p, op); reconcileErr != nil {
			return reconcileErr
		}
	}
	return nil
}
