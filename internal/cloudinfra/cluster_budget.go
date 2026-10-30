package cloudinfra

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	mathbig "math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

const DefaultBudgetHorizonSeconds int64 = 30 * 24 * 60 * 60

// MoneyEstimate is an estimated run rate, not a provider invoice.
type MoneyEstimate struct {
	HourlyMicroUSD    int64            `json:"hourly_micro_usd"`
	HorizonSeconds    int64            `json:"horizon_seconds"`
	HorizonMicroUSD   int64            `json:"horizon_micro_usd"`
	Known             bool             `json:"known"`
	MissingComponents []string         `json:"missing_components"`
	PriceDigest       string           `json:"price_digest"`
	CheckedAt         time.Time        `json:"checked_at"`
	Components        []MoneyComponent `json:"components"`
}

// MoneyComponent retains the source identity and units behind each charge.
type MoneyComponent struct {
	priceProjected bool
	Category       string     `json:"category,omitempty"`
	PriceCheckedAt *time.Time `json:"price_checked_at,omitempty"`
	Name           string     `json:"name"`
	Source         string     `json:"source"`
	Identity       string     `json:"identity"`
	Quantity       int64      `json:"quantity"`
	HourlyMicroUSD *int64     `json:"hourly_micro_usd"`
}

type CapacityBudget struct {
	MaxHourlyMicroUSD  int64 `json:"max_hourly_micro_usd"`
	MaxHorizonMicroUSD int64 `json:"max_horizon_micro_usd"`
	HorizonSeconds     int64 `json:"horizon_seconds"`
}

func checkedMoneyProduct(a, b int64) (int64, error) {
	if a < 0 || b < 0 || b != 0 && a > math.MaxInt64/b {
		return 0, ErrInvalid
	}
	return a * b, nil
}

// Convert the provider's decimal display value before multiplying any units.
func hourlyMicroUSD(value float64) (int64, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, ErrInvalid
	}
	r, ok := new(mathbig.Rat).SetString(strconv.FormatFloat(value, 'f', -1, 64))
	if !ok {
		return 0, ErrInvalid
	}
	r.Mul(r, mathbig.NewRat(1_000_000, 1))
	n, remainder := new(mathbig.Int), new(mathbig.Int)
	n.QuoRem(r.Num(), r.Denom(), remainder)
	if remainder.Sign() != 0 {
		n.Add(n, mathbig.NewInt(1))
	}
	if !n.IsInt64() {
		return 0, ErrInvalid
	}
	return n.Int64(), nil
}

func moneyEstimate(components []MoneyComponent, horizon int64, checked time.Time) (MoneyEstimate, error) {
	if horizon == 0 {
		horizon = DefaultBudgetHorizonSeconds
	}
	out := MoneyEstimate{HorizonSeconds: horizon, CheckedAt: checked, Known: true, MissingComponents: []string{}, Components: append([]MoneyComponent(nil), components...)}
	if horizon < 0 {
		return out, ErrInvalid
	}
	sort.Slice(out.Components, func(i, j int) bool { return out.Components[i].Name < out.Components[j].Name })
	previous := ""
	for _, c := range out.Components {
		if c.Name == "" || c.Name == previous || c.Source == "" || c.Identity == "" || c.Quantity <= 0 {
			return out, ErrInvalid
		}
		previous = c.Name
		if c.HourlyMicroUSD == nil {
			out.Known = false
			out.MissingComponents = append(out.MissingComponents, c.Name)
			continue
		}
		amount, err := checkedMoneyProduct(*c.HourlyMicroUSD, c.Quantity)
		if err != nil || amount > math.MaxInt64-out.HourlyMicroUSD {
			return out, ErrInvalid
		}
		out.HourlyMicroUSD += amount
	}
	product, err := checkedMoneyProduct(out.HourlyMicroUSD, horizon)
	if err != nil {
		return out, err
	}
	out.HorizonMicroUSD = product / 3600
	if product%3600 != 0 {
		out.HorizonMicroUSD++
	}
	semantic := append([]MoneyComponent(nil), out.Components...)
	for i := range semantic {
		semantic[i].PriceCheckedAt = nil
	}
	raw, err := json.Marshal(semantic)
	if err != nil {
		return out, err
	}
	digest := sha256.Sum256(raw)
	out.PriceDigest = hex.EncodeToString(digest[:])
	return out, nil
}

func clusterNodeMoney(in ClusterInput, options provider.ClusterOptions, horizon int64) (MoneyEstimate, error) {
	components := []MoneyComponent{}
	identity := in.Provider + "/" + in.AccountID + "/" + options.Digest
	for _, name := range []string{"control_plane", "network_egress", "load_balancers", "persistent_storage"} {
		components = append(components, MoneyComponent{Name: name, Source: "provider_options", Identity: identity, Quantity: 1})
	}
	for _, pool := range in.Spec.NodePools {
		component := MoneyComponent{Name: "nodes:" + pool.Name, Source: "provider_options", Identity: identity + "/" + pool.Size, Quantity: int64(pool.Count)}
		found := false
		for _, size := range options.NodeSizeFacts {
			if size.ID != pool.Size {
				continue
			}
			if found {
				return MoneyEstimate{}, ErrInvalid
			}
			found = true
			if size.HourlyUSD != nil {
				value, err := hourlyMicroUSD(*size.HourlyUSD)
				if err != nil {
					return MoneyEstimate{}, err
				}
				component.HourlyMicroUSD = number(value)
			}
		}
		components = append(components, component)
	}
	return moneyEstimate(components, horizon, options.CheckedAt)
}

func (s *Service) clusterMoney(ctx context.Context, in ClusterInput, options provider.ClusterOptions, reader provider.ClusterReader, access provider.AccountAccess) (MoneyEstimate, error) {
	estimate, err := clusterNodeMoney(in, options, 0)
	if err != nil || in.Ref.ID == "" {
		return estimate, err
	}
	retirer, ok := reader.(provider.ClusterRetirer)
	if !ok {
		return estimate, nil
	}
	inventory, err := retirer.AssociatedResources(ctx, access, in.Ref)
	if err != nil {
		return MoneyEstimate{}, err
	}
	if inventory.Ref != in.Ref || !freshCapacity(inventory.ObservedAt, s.now()) {
		return MoneyEstimate{}, ErrConflict
	}
	for _, item := range inventory.Resources {
		if item.ID == "" || item.Kind == "" {
			return MoneyEstimate{}, ErrInvalid
		}
		estimate.Components = append(estimate.Components, MoneyComponent{Category: costCategory(item.Kind, ""), Name: "associated:" + item.Kind + ":" + item.ID, Source: "associated_inventory", Identity: in.Provider + "/" + in.AccountID + "/" + item.Incarnation + "/" + item.ID, Quantity: 1, HourlyMicroUSD: item.EstimatedHourlyMicroUSD})
	}
	if !inventory.Complete {
		estimate.Components = append(estimate.Components, MoneyComponent{Name: "associated_inventory", Source: "associated_inventory", Identity: in.Ref.ID, Quantity: 1})
	}
	checked := estimate.CheckedAt
	if inventory.ObservedAt.Before(checked) {
		checked = inventory.ObservedAt
	}
	return moneyEstimate(estimate.Components, estimate.HorizonSeconds, checked)
}

// costCategory accepts only public charge kinds. Provider names and resource IDs
// never become review labels.
func costCategory(category, name string) string {
	if category == "" {
		parts := strings.Split(name, ":")
		category = parts[0]
		if category == "associated" && len(parts) > 1 {
			category = parts[1]
		}
	}
	switch category {
	case "nodes", "server":
		return "nodes"
	case "network_egress", "persistent_storage", "control_plane", "load_balancers":
		return category
	case "volume", "storage":
		return "persistent_storage"
	case "load_balancer":
		return "load_balancers"
	default:
		return "other"
	}
}

func freshPrice(checked, now time.Time) bool {
	return !checked.IsZero() && !checked.After(now) && now.Sub(checked) < 5*time.Minute
}
