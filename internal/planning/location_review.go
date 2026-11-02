package planning

import (
	"context"
	"time"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/cloudprofiles"
)

func validateLocationActionsTx(ctx context.Context, tx driver.Tx, org, project string, actions []Action, now time.Time) error {
	for _, a := range actions {
		if a.Service == nil || a.Op == Delete || a.Op == Keep && !a.AdoptCloudPlacement {
			continue
		}
		var review *cloudprofiles.LocationReview
		if a.Service.CloudPlacement != nil {
			review = a.Service.CloudPlacement.LocationReview
		}
		if a.Service.CapacityIntent != nil {
			review = a.Service.CapacityIntent.Demand.LocationReview
		}
		if err := cloudprofiles.ValidateLocationReviewTx(ctx, tx, org, project, review, now); err != nil {
			return err
		}
	}
	return nil
}
