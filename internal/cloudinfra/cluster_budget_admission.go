package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/xraph/loomplane/pkg/provider"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
)

// BudgetReview pins the partial estimate shown before approval. Source identities
// are opaque hashes so shared account evidence does not expose other Clouds.
type BudgetReview struct {
	AccountExposure *CapacityAccountSnapshot `json:"account_exposure,omitempty"`
	CloudID         string                   `json:"cloud_id"`
	AccountScope    string                   `json:"account_scope"`
	PolicyRevision  int64                    `json:"policy_revision"`
	Estimate        MoneyEstimate            `json:"estimate"`
	Digest          string                   `json:"digest"`
}

func checkBudgetReview(p *CapacityPolicy, saved, current *BudgetReview, acknowledge bool) error {
	if p.Budget == nil {
		return nil
	}
	if saved == nil || current == nil || saved.PolicyRevision != p.Revision || saved.Digest != current.Digest || !freshCapacity(current.Estimate.CheckedAt, time.Now()) {
		return ErrConflict
	}
	if !current.Estimate.Known && !acknowledge {
		return ErrConflict
	}
	if current.Estimate.HourlyMicroUSD > p.Budget.MaxHourlyMicroUSD || current.Estimate.HorizonMicroUSD > p.Budget.MaxHorizonMicroUSD {
		return &CapacityAdmissionError{Block: CapacityBlock{Reason: "hard_limit", Resource: "budget", CloudID: p.CloudID, PolicyRevision: p.Revision}}
	}
	return nil
}

func budgetIdentity(parts ...any) string { return maintenanceHash(parts) }

func scopedBudgetReviewTx(ctx context.Context, tx driver.Tx, org, cloud, providerName, account, exclude string, request *MoneyEstimate, policy *CapacityPolicy, planned *ClusterEnvelopeReview) (*BudgetReview, error) {
	if policy.Budget == nil {
		return nil, nil
	}
	now := time.Now().UTC()
	components := []MoneyComponent{}
	addUnknown := func(source string, identity any) {
		key := budgetIdentity(org, source, identity)
		components = append(components, MoneyComponent{Name: source + ":" + key, Source: source, Identity: key, Quantity: 1})
	}
	addEstimate := func(source, id string, estimate *MoneyEstimate) {
		if estimate == nil {
			addUnknown(source, id)
			return
		}
		for _, c := range estimate.Components {
			key := budgetIdentity(org, source, id, c.Name)
			c.Name = source + ":" + key
			c.Source = source
			c.Identity = budgetIdentity(org, id, c.Identity)
			components = append(components, c)
		}
		if len(estimate.Components) == 0 {
			addUnknown(source, id)
		}
	}
	list, err := clusterEnvelopesTx(ctx, tx, org)
	if err != nil {
		return nil, err
	}
	if planned != nil && request != nil {
		copyRequest := *request
		copyRequest.Components = append([]MoneyComponent(nil), request.Components...)
		for _, review := range planned.Pools {
			kept := copyRequest.Components[:0]
			for _, c := range copyRequest.Components {
				if c.Name != "nodes:"+review.Key {
					kept = append(kept, c)
				}
			}
			copyRequest.Components = kept
			var prior *MoneyEstimate
			for _, e := range list {
				if e.ID == review.EnvelopeID {
					prior = &e.Money
					if e.OperationID == exclude && e.PriorMoney != nil {
						prior = e.PriorMoney
					}
					break
				}
			}
			price := review.Money.HourlyMicroUSD
			known := review.Money.Known
			if prior != nil {
				known = known && prior.Known
				price = max(0, price-prior.HourlyMicroUSD)
			}
			var unitPrice *int64
			if known {
				unitPrice = number(price)
			}
			copyRequest.Components = append(copyRequest.Components, MoneyComponent{Name: "envelope:" + review.Key, Source: "reviewed_envelope", Identity: review.Money.PriceDigest, Quantity: 1, HourlyMicroUSD: unitPrice})
		}
		request = &copyRequest
	}
	addEstimate("request", exclude, request)
	// External account consumption is quota context, not a charge to this Cloud.
	var raw []byte
	var exposure *CapacityAccountSnapshot
	err = tx.QueryRow(ctx, `SELECT body FROM loomplane_cloud_capacity_accounts WHERE org_id=$1 AND provider=$2 AND account_id=$3`, org, providerName, account).Scan(&raw)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return nil, err
	default:
		var a CapacityAccountSnapshot
		if checkErr := json.Unmarshal(raw, &a); checkErr != nil {
			return nil, checkErr
		}
		exposure = &a
	}
	// Charges follow Cloud attribution across every provider. Retained associated
	// charges survive a cluster's tombstone.
	rows, err := tx.Query(ctx, `SELECT body FROM loomplane_cloud_servers WHERE org_id=$1 AND cloud_id=$2 AND status NOT IN ('deleted','disconnected') ORDER BY id`, org, cloud)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var body []byte
		if checkErr := rows.Scan(&body); checkErr != nil {
			rows.Close()
			return nil, checkErr
		}
		var server Server
		if checkErr := json.Unmarshal(body, &server); checkErr != nil {
			rows.Close()
			return nil, checkErr
		}
		key := budgetIdentity(org, "observed_server", server.Provider, server.AccountID, server.ResourceID, server.ID)
		c := MoneyComponent{Name: "observed:" + key, Source: "observed", Identity: key, Quantity: 1}
		if server.HourlyPriceKnown && server.PricedSize == server.Size {
			price, e := hourlyMicroUSD(server.HourlyUSD)
			if e != nil {
				rows.Close()
				return nil, e
			}
			c.HourlyMicroUSD = &price
		}
		components = append(components, c)
	}
	rows.Close()
	if checkErr := rows.Err(); checkErr != nil {
		return nil, checkErr
	}
	retained := map[string]bool{}
	rows, err = tx.Query(ctx, `SELECT body FROM loomplane_managed_resources WHERE org_id=$1 AND cloud_id=$2 ORDER BY id`, org, cloud)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var body []byte
		if checkErr := rows.Scan(&body); checkErr != nil {
			rows.Close()
			return nil, checkErr
		}
		var r ManagedResource
		if checkErr := json.Unmarshal(body, &r); checkErr != nil {
			rows.Close()
			return nil, checkErr
		}
		if r.DeletedAt == nil {
			var pools []provider.NodePoolSpec
			if r.Observation != nil {
				pools = append(pools, r.Observation.Pools...)
			}
			sort.Slice(pools, func(i, j int) bool { return pools[i].ID < pools[j].ID })
			addUnknown("observed_cluster", []any{r.ID, retirementResourceFingerprint(&r), pools})
		}
		for _, item := range r.RetainedResources {
			key := budgetIdentity(org, r.Provider, r.AccountID, item.Kind, item.ID, item.Incarnation)
			if retained[key] {
				continue
			}
			retained[key] = true
			components = append(components, MoneyComponent{Name: "retained:" + key, Source: "retained", Identity: key, Quantity: 1, HourlyMicroUSD: item.EstimatedHourlyMicroUSD})
		}
	}
	rows.Close()
	if checkErr := rows.Err(); checkErr != nil {
		return nil, checkErr
	}
	reservations, err := capacityReservationsTx(ctx, tx, org)
	if err != nil {
		return nil, err
	}
	for _, scoped := range reservations {
		r := scoped.reservation
		if r.OperationID == exclude || scoped.cloud != cloud {
			continue
		}
		var estimate *MoneyEstimate
		if r.ClusterOperationID != "" {
			o, e := clusterOperationTx(ctx, tx, org, scoped.cloud, r.OperationID)
			if e != nil {
				return nil, e
			}
			estimate = o.MoneyEstimate
			if estimate != nil && o.Input.EnvelopeReview != nil {
				copyEstimate := *estimate
				copyEstimate.Components = append([]MoneyComponent(nil), estimate.Components...)
				list, e := clusterEnvelopesTx(ctx, tx, org)
				if e != nil {
					return nil, e
				}
				for _, envelope := range list {
					if envelope.OperationID != o.ID || envelope.State != "active" || envelope.Pending != (ResourceVector{}) {
						continue
					}
					kept := copyEstimate.Components[:0]
					for _, c := range copyEstimate.Components {
						if c.Name != "nodes:"+envelope.Review.Key {
							kept = append(kept, c)
						}
					}
					copyEstimate.Components = kept
				}
				estimate = &copyEstimate
			}
		} else {
			o, e := scanOperation(tx.QueryRow(ctx, `SELECT body,principal FROM loomplane_infrastructure_operations WHERE org_id=$1 AND id=$2`, org, r.OperationID))
			if e != nil {
				return nil, e
			}
			estimate, err = serverBudgetMoney(ctx, tx, o)
			if err != nil {
				return nil, err
			}
		}
		addEstimate("pending", r.OperationID, estimate)
	}
	envelopes, err := clusterEnvelopesTx(ctx, tx, org)
	if err != nil {
		return nil, err
	}
	for _, e := range envelopes {
		if e.OperationID == exclude {
			if e.PriorMaximum == nil || e.PriorMoney == nil {
				continue
			}
			e.Maximum = *e.PriorMaximum
			e.Money = *e.PriorMoney
			e.Pending = ResourceVector{}
		}
		if e.CloudID != cloud {
			continue
		}
		if e.Consumed.Servers > 0 {
			priced, priceErr := envelopeRemainderMoney(e, e.Consumed.Servers)
			if priceErr != nil {
				return nil, priceErr
			}
			addEstimate("observed_envelope", e.ID, &priced)
		}
		remainder := envelopeRemainder(e)
		if remainder.Servers == 0 {
			continue
		}
		estimate, estimateErr := envelopeRemainderMoney(e, remainder.Servers)
		if estimateErr != nil {
			return nil, estimateErr
		}
		addEstimate("envelope", e.ID, &estimate)
	}
	estimate, err := moneyEstimate(components, policy.Budget.HorizonSeconds, now)
	if err != nil {
		return nil, err
	}
	review := &BudgetReview{AccountExposure: exposure, CloudID: cloud, AccountScope: budgetIdentity(org, providerName, account), PolicyRevision: policy.Revision, Estimate: estimate}
	review.Digest = maintenanceHash([]any{review.CloudID, review.AccountScope, review.PolicyRevision, estimate.PriceDigest, estimate.HorizonSeconds})
	return review, nil
}

func serverBudgetMoney(ctx context.Context, tx driver.Tx, o *Operation) (*MoneyEstimate, error) {
	if o.Input.Kind != "resize" || o.MoneyEstimate == nil {
		return o.MoneyEstimate, nil
	}
	var raw []byte
	if checkErr := tx.QueryRow(ctx, `SELECT body FROM loomplane_cloud_servers WHERE org_id=$1 AND cloud_id=$2 AND id=$3`, o.OrgID, o.CloudID, o.ServerID).Scan(&raw); checkErr != nil {
		return nil, checkErr
	}
	var server Server
	if checkErr := json.Unmarshal(raw, &server); checkErr != nil {
		return nil, checkErr
	}
	if !server.HourlyPriceKnown || server.PricedSize != server.Size {
		return o.MoneyEstimate, nil
	}
	oldPrice, err := hourlyMicroUSD(server.HourlyUSD)
	if err != nil {
		return nil, err
	}
	estimate := *o.MoneyEstimate
	estimate.Components = append([]MoneyComponent(nil), estimate.Components...)
	for i, c := range estimate.Components {
		if c.Name == "server" && c.HourlyMicroUSD != nil {
			estimate.Components[i].HourlyMicroUSD = number(max(0, *c.HourlyMicroUSD-oldPrice))
		}
	}
	return &estimate, nil
}
