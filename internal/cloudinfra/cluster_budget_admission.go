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
	if saved == nil || current == nil || saved.PolicyRevision != p.Revision || saved.Digest != current.Digest || !freshPrice(current.Estimate.CheckedAt, time.Now()) {
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
	prices, priceErr := budgetPricesTx(ctx, tx, org, cloud)
	if priceErr != nil {
		return nil, priceErr
	}
	components := []MoneyComponent{}
	oldest := now
	stalePrice := false
	notePrice := func(checked time.Time) {
		if !freshPrice(checked, now) {
			stalePrice = true
		}
		if checked.Before(oldest) {
			oldest = checked
		}
	}

	addUnknown := func(source string, identity any) {
		key := budgetIdentity(org, source, identity)
		components = append(components, MoneyComponent{Category: "other", Name: source + ":" + key, Source: source, Identity: key, Quantity: 1})
	}
	addEstimate := func(source, id string, estimate *MoneyEstimate) {
		if estimate == nil {
			addUnknown(source, id)
			return
		}
		for _, c := range estimate.Components {
			checked := estimate.CheckedAt
			if c.PriceCheckedAt != nil {
				checked = *c.PriceCheckedAt
			}
			category := costCategory(c.Category, c.Name)
			if category == "nodes" {
				if fact, ok := prices[c.Identity]; ok && !c.priceProjected {
					checked = fact.CheckedAt
					c.HourlyMicroUSD = fact.HourlyMicroUSD
				}
				notePrice(checked)
			}
			c.PriceCheckedAt = &checked
			if !freshPrice(checked, now) {
				c.HourlyMicroUSD = nil
			}
			c.Category = costCategory(c.Category, c.Name)
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
			projectedReview, projectionErr := projectNodeMoney(review.Money, prices)
			if projectionErr != nil {
				return nil, projectionErr
			}
			price := projectedReview.HourlyMicroUSD
			checked := projectedReview.CheckedAt
			known := projectedReview.Known
			if prior != nil {
				projected, e := projectNodeMoney(*prior, prices)
				if e != nil {
					return nil, e
				}
				prior = &projected
				if prior.CheckedAt.Before(checked) {
					checked = prior.CheckedAt
				}
			}
			if prior != nil {
				known = known && prior.Known
				price = max(0, price-prior.HourlyMicroUSD)
			}
			var unitPrice *int64
			if known {
				unitPrice = number(price)
			}
			copyRequest.Components = append(copyRequest.Components, MoneyComponent{Category: "nodes", PriceCheckedAt: &checked, Name: "envelope:" + review.Key, Source: "reviewed_envelope", Identity: review.Money.PriceDigest, Quantity: 1, HourlyMicroUSD: unitPrice})
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
	physicalServers := map[string]bool{}
	seenServers := map[string]bool{}
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
		physical := budgetIdentity(server.Provider, server.AccountID, server.ResourceID)
		if server.ResourceID != "" && seenServers[physical] {
			addUnknown("node_membership", physical)
		}
		if server.ResourceID != "" {
			seenServers[physical] = true
		}
		key := budgetIdentity(org, "observed_server", server.Provider, server.AccountID, server.ResourceID, server.ID)
		c := MoneyComponent{Category: "nodes", Name: "observed:" + key, Source: "observed", Identity: key, Quantity: 1}
		checked := server.PriceCheckedAt
		if fact, ok := prices[serverPriceIdentity(&server)]; ok {
			checked = fact.CheckedAt
			if freshPrice(checked, now) {
				c.HourlyMicroUSD = fact.HourlyMicroUSD
			}
		}
		notePrice(checked)
		c.PriceCheckedAt = &checked
		if _, current := prices[serverPriceIdentity(&server)]; !current && server.HourlyPriceKnown && server.PricedSize == server.Size && freshPrice(server.PriceCheckedAt, now) {
			price, e := hourlyMicroUSD(server.HourlyUSD)
			if e != nil {
				rows.Close()
				return nil, e
			}
			c.HourlyMicroUSD = &price
		}
		components = append(components, c)
		if server.ResourceID != "" && c.HourlyMicroUSD != nil {
			physicalServers[physical] = true
		}
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
			var retainedPrice *int64
			if freshPrice(r.RetainedPriceCheckedAt, now) {
				retainedPrice = item.EstimatedHourlyMicroUSD
				notePrice(r.RetainedPriceCheckedAt)
			}
			components = append(components, MoneyComponent{Category: costCategory(item.Kind, ""), Name: "retained:" + key, Source: "retained", Identity: key, Quantity: 1, HourlyMicroUSD: retainedPrice, PriceCheckedAt: &r.RetainedPriceCheckedAt})
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
		if e.CloudID != cloud || e.State == "released" {
			continue
		}
		observed, verified := distinctEnvelopeNodes(e, physicalServers, now)
		if e.Consumed.Servers > 0 && !verified {
			addUnknown("node_membership", e.ID)
		}
		if observed > 0 {
			priced, priceErr := envelopeRemainderMoney(e, observed)
			if priceErr != nil {
				return nil, priceErr
			}
			addEstimate("observed_envelope", e.ID, &priced)
			if verified {
				known := true
				for _, c := range priced.Components {
					if fact, ok := prices[c.Identity]; ok {
						if fact.HourlyMicroUSD == nil || !freshPrice(fact.CheckedAt, now) {
							known = false
						}
					} else if c.HourlyMicroUSD == nil || !freshPrice(priced.CheckedAt, now) {
						known = false
					}
				}
				if known {
					for _, id := range e.NodeIDs {
						physicalServers[budgetIdentity(e.Provider, e.AccountID, id)] = true
					}
				}
			}
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
	if stalePrice {
		oldest = time.Time{}
	}
	estimate, err := moneyEstimate(components, policy.Budget.HorizonSeconds, oldest)
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
	prices, err := budgetPricesTx(ctx, tx, o.OrgID, o.CloudID)
	if err != nil {
		return nil, err
	}
	var oldPrice *int64
	oldChecked := server.PriceCheckedAt
	if fact, ok := prices[serverPriceIdentity(&server)]; ok {
		oldPrice = fact.HourlyMicroUSD
		oldChecked = fact.CheckedAt
	} else if server.HourlyPriceKnown && server.PricedSize == server.Size {
		value, e := hourlyMicroUSD(server.HourlyUSD)
		if e != nil {
			return nil, e
		}
		oldPrice = number(value)
	}
	estimate := *o.MoneyEstimate
	estimate.Components = append([]MoneyComponent(nil), estimate.Components...)
	for i, c := range estimate.Components {
		if c.Name != "server" {
			continue
		}
		checked := estimate.CheckedAt
		if fact, ok := prices[c.Identity]; ok {
			c.HourlyMicroUSD = fact.HourlyMicroUSD
			checked = fact.CheckedAt
		}
		if freshPrice(oldChecked, time.Now()) && oldPrice != nil && c.HourlyMicroUSD != nil {
			c.HourlyMicroUSD = number(max(0, *c.HourlyMicroUSD-*oldPrice))
			if oldChecked.Before(checked) {
				checked = oldChecked
			}
		}
		c.PriceCheckedAt = &checked
		c.priceProjected = true
		estimate.Components[i] = c
	}
	return &estimate, nil
}

// NodeIDs are written only after settlement verifies physical account membership.
func distinctEnvelopeNodes(e ClusterCapacityEnvelope, servers map[string]bool, now time.Time) (int64, bool) {
	count := e.Consumed.Servers
	if count == 0 {
		return 0, true
	}
	if e.ResourceID == "" || e.Ref.ID == "" || e.Provider == "" || e.AccountID == "" || !freshClusterEnvelopeEvidence(e.CheckedAt, now) || int64(len(e.NodeIDs)) != count {
		return count, false
	}
	seen := map[string]bool{}
	for _, id := range e.NodeIDs {
		if id == "" || seen[id] {
			return e.Consumed.Servers, false
		}
		seen[id] = true
		if servers[budgetIdentity(e.Provider, e.AccountID, id)] {
			count--
		}
	}
	return count, true
}
