package cloudinfra

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type budgetPriceFact struct {
	Identity       string    `json:"identity"`
	HourlyMicroUSD *int64    `json:"hourly_micro_usd"`
	CheckedAt      time.Time `json:"checked_at"`
}

type budgetPriceSource struct {
	Identity, Provider, Account, Connection, Region, Size string
	Cluster                                               bool
}

func budgetPricesTx(ctx context.Context, tx driver.Tx, org, cloud string) (map[string]budgetPriceFact, error) {
	rows, err := tx.Query(ctx, `SELECT body FROM loomplane_cloud_budget_prices WHERE org_id=$1 AND cloud_id=$2`, org, cloud)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	facts := map[string]budgetPriceFact{}
	for rows.Next() {
		var raw []byte
		var fact budgetPriceFact
		if checkErr := rows.Scan(&raw); checkErr != nil {
			return nil, checkErr
		}
		if checkErr := json.Unmarshal(raw, &fact); checkErr != nil {
			return nil, checkErr
		}
		facts[fact.Identity] = fact
	}
	return facts, rows.Err()
}

func serverPriceIdentity(v *Server) string { return v.Provider + "/" + v.AccountID + "/" + v.Size }

func (s *PgStore) budgetPriceSources(ctx context.Context, org, cloud string) ([]budgetPriceSource, error) {
	rows, err := s.db.Query(ctx, `SELECT 'server',body FROM loomplane_cloud_servers WHERE org_id=$1 AND cloud_id=$2 AND status NOT IN ('deleted','disconnected')
 UNION ALL SELECT 'operation',o.body FROM loomplane_infrastructure_operations o JOIN loomplane_cloud_capacity_reservations r ON r.org_id=o.org_id AND r.cloud_id=o.cloud_id AND r.id=o.id WHERE o.org_id=$1 AND o.cloud_id=$2 AND r.status IN ('reserved','committed')
 UNION ALL SELECT 'cluster',o.body FROM loomplane_cluster_operations o WHERE o.org_id=$1 AND o.cloud_id=$2 AND (EXISTS(SELECT 1 FROM loomplane_cloud_capacity_reservations r WHERE r.org_id=o.org_id AND r.cloud_id=o.cloud_id AND r.id=o.id AND r.status IN ('reserved','committed')) OR EXISTS(SELECT 1 FROM loomplane_cluster_capacity_envelopes e WHERE e.org_id=o.org_id AND e.cloud_id=o.cloud_id AND e.body->>'operation_id'=o.id AND e.body->>'state'<>'released'))
 UNION ALL SELECT 'envelope',body FROM loomplane_cluster_capacity_envelopes WHERE org_id=$1 AND cloud_id=$2 AND (body->>'state'<>'released' OR body->'schema_version' IS DISTINCT FROM '1'::jsonb)`, org, cloud)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sources []budgetPriceSource
	var envelopes []ClusterCapacityEnvelope
	operations := map[string]ClusterOperation{}
	add := func(source budgetPriceSource, estimate *MoneyEstimate) {
		if estimate == nil {
			return
		}
		for _, c := range estimate.Components {
			if costCategory(c.Category, c.Name) != "nodes" {
				continue
			}
			copySource := source
			copySource.Identity = c.Identity
			if copySource.Size == "" {
				parts := strings.Split(c.Identity, "/")
				copySource.Size = parts[len(parts)-1]
			}
			sources = append(sources, copySource)
		}
	}
	for rows.Next() {
		var kind string
		var raw []byte
		if checkErr := rows.Scan(&kind, &raw); checkErr != nil {
			return nil, checkErr
		}
		switch kind {
		case "server":
			var v Server
			if checkErr := json.Unmarshal(raw, &v); checkErr != nil {
				return nil, checkErr
			}
			if v.Provider != "server" {
				sources = append(sources, budgetPriceSource{Identity: serverPriceIdentity(&v), Provider: v.Provider, Account: v.AccountID, Connection: v.ConnectionID, Region: v.Region, Size: v.Size})
			}
		case "operation":
			var o Operation
			if checkErr := json.Unmarshal(raw, &o); checkErr != nil {
				return nil, checkErr
			}
			add(budgetPriceSource{Provider: o.Provider, Account: o.AccountID, Connection: o.ConnectionID, Region: o.Input.Region, Size: o.Input.Size}, o.MoneyEstimate)
		case "cluster":
			var o ClusterOperation
			if checkErr := json.Unmarshal(raw, &o); checkErr != nil {
				return nil, checkErr
			}
			operations[o.ID] = o
			add(budgetPriceSource{Provider: o.Input.Provider, Account: o.Input.AccountID, Connection: o.Input.ConnectionID, Region: o.Input.Spec.Region, Cluster: true}, o.MoneyEstimate)
		case "envelope":
			var e ClusterCapacityEnvelope
			if checkErr := json.Unmarshal(raw, &e); checkErr != nil {
				return nil, checkErr
			}
			envelopes = append(envelopes, e)
		}
	}
	for _, e := range envelopes {
		o, ok := operations[e.OperationID]
		if !ok {
			continue
		}
		source := budgetPriceSource{Provider: e.Provider, Account: e.AccountID, Connection: o.Input.ConnectionID, Region: o.Input.Spec.Region, Cluster: true}
		add(source, &e.Money)
		add(source, e.PriorMoney)
	}
	return sources, rows.Err()
}

// refreshBudgetPrices stores current catalog facts separately from approved reviews.
func (s *Service) refreshBudgetPrices(ctx context.Context, p security.Principal, cloud string) error {
	st, ok := s.Store.(*PgStore)
	if !ok {
		return nil
	}
	policy, err := st.GetCapacityPolicy(ctx, p.OrgID, cloud)
	if err != nil || policy.Budget == nil {
		return err
	}
	if checkErr := s.Authz.Require(ctx, p, security.OrganizationCloudManage, p.OrgID); checkErr != nil {
		return checkErr
	}
	sources, err := st.budgetPriceSources(ctx, p.OrgID, cloud)
	if err != nil {
		return err
	}
	catalogs := map[string]map[string]*int64{}
	checks := map[string]time.Time{}
	facts := map[string]budgetPriceFact{}
	for _, source := range sources {
		key := maintenanceHash([]any{source.Provider, source.Account, source.Connection, source.Region, source.Cluster})
		prices, found := catalogs[key]
		if !found {
			prices = map[string]*int64{}
			checked := s.now().UTC()
			if source.Cluster {
				reader, access, e := s.clusterAccount(ctx, p, source.Connection, provider.ClusterDiscovery)
				if e != nil {
					return e
				}
				if access.Provider != source.Provider || access.AccountID != source.Account {
					return provider.ErrInfrastructureIdentity
				}
				options, e := reader.Options(ctx, access, source.Region)
				if e != nil {
					return e
				}
				checked = options.CheckedAt
				for _, size := range options.NodeSizeFacts {
					if size.HourlyUSD != nil {
						value, e := hourlyMicroUSD(*size.HourlyUSD)
						if e != nil {
							return e
						}
						prices[size.ID] = number(value)
					}
				}
			} else {
				conn, raw, e := s.Connections.Credential(ctx, p.OrgID, source.Connection)
				if e != nil {
					return e
				}
				credential, e := Credential(conn, raw)
				if e != nil {
					return e
				}
				if credential.Provider != source.Provider || credential.AccountID != source.Account {
					return provider.ErrInfrastructureIdentity
				}
				adapter, e := s.Adapters.Server(source.Provider, provider.ServerDiscovery)
				if e != nil {
					return e
				}
				reader, ok := adapter.(provider.ServerOptionsAdapter)
				if !ok {
					return provider.ErrInfrastructureUnsupported
				}
				options, e := reader.ServerOptions(ctx, credential)
				if e != nil {
					return e
				}
				for _, size := range options.Sizes {
					if size.HourlyPriceKnown {
						value, e := hourlyMicroUSD(size.HourlyUSD)
						if e != nil {
							return e
						}
						prices[size.ID] = number(value)
					}
				}
			}
			if !freshPrice(checked, s.now()) {
				return ErrConflict
			}
			catalogs[key] = prices
			checks[key] = checked
		}
		facts[source.Identity] = budgetPriceFact{Identity: source.Identity, HourlyMicroUSD: prices[source.Size], CheckedAt: checks[key]}
	}
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if checkErr := lockCapacityCloud(ctx, tx, p.OrgID, cloud); checkErr != nil {
		return checkErr
	}
	for _, fact := range facts {
		raw, e := json.Marshal(fact)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO loomplane_cloud_budget_prices(org_id,cloud_id,identity,checked_at,body) VALUES($1,$2,$3,$4,$5) ON CONFLICT(org_id,cloud_id,identity) DO UPDATE SET checked_at=excluded.checked_at,body=excluded.body WHERE loomplane_cloud_budget_prices.checked_at<=excluded.checked_at`, p.OrgID, cloud, fact.Identity, fact.CheckedAt, raw)
		if e != nil {
			return e
		}
	}
	return tx.Commit()
}

func projectNodeMoney(estimate MoneyEstimate, prices map[string]budgetPriceFact) (MoneyEstimate, error) {
	components := append([]MoneyComponent(nil), estimate.Components...)
	checked := time.Time{}
	for i, c := range components {
		sourceChecked := estimate.CheckedAt
		if costCategory(c.Category, c.Name) == "nodes" {
			if fact, ok := prices[c.Identity]; ok {
				c.HourlyMicroUSD = fact.HourlyMicroUSD
				sourceChecked = fact.CheckedAt
			}
		}
		if i == 0 || sourceChecked.Before(checked) {
			checked = sourceChecked
		}
		if !freshPrice(sourceChecked, time.Now()) {
			c.HourlyMicroUSD = nil
		}
		c.PriceCheckedAt = &sourceChecked
		components[i] = c
	}
	return moneyEstimate(components, estimate.HorizonSeconds, checked)
}
