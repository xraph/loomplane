package cloudprofiles

import (
	"context"
	"encoding/json"
	"time"

	"github.com/xraph/grove/driver"
)

// ValidatePlanLocationReviewsTx checks saved placement authority before the
// first remote effect. Accepted attempts retain their existing recovery rules.
func ValidatePlanLocationReviewsTx(ctx context.Context, tx driver.Tx, org, plan string, now time.Time) error {
	var raw []byte
	var project string
	if err := tx.QueryRow(ctx, `SELECT project_id,result FROM loomplane_plans WHERE org_id=$1 AND id=$2 FOR SHARE`, org, plan).Scan(&project, &raw); err != nil {
		return storeError(err)
	}
	if len(raw) == 0 {
		return nil
	}
	var result struct {
		Actions []struct {
			Op      string `json:"op"`
			Service *struct {
				Placement *struct {
					Review *LocationReview `json:"location_review"`
				} `json:"cloud_placement"`
				Intent *struct {
					Demand struct {
						Review *LocationReview `json:"location_review"`
					} `json:"demand"`
				} `json:"capacity_intent"`
			} `json:"service"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	for _, a := range result.Actions {
		if a.Op == "keep" || a.Op == "delete" || a.Service == nil {
			continue
		}
		var review *LocationReview
		if a.Service.Placement != nil {
			review = a.Service.Placement.Review
		}
		if a.Service.Intent != nil {
			review = a.Service.Intent.Demand.Review
		}
		if err := ValidateLocationReviewTx(ctx, tx, org, project, review, now); err != nil {
			return err
		}
	}
	return nil
}
