package cloudinfra

import (
	"strconv"
	"time"

	infra "github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/pkg/client"
	"github.com/xraph/loomplane/pkg/provider"
)

type Operation struct {
	BudgetReview         *client.BudgetReview            `json:"budget_review,omitempty"`
	MoneyEstimate        *client.MoneyEstimate           `json:"money_estimate,omitempty"`
	CapacityTarget       *infra.ResourceVector           `json:"capacity_target,omitempty"`
	CapacityCheckedAt    time.Time                       `json:"capacity_checked_at,omitempty,omitzero"`
	AbsenceConfirmedAt   time.Time                       `json:"absence_confirmed_at,omitempty,omitzero"`
	EstimatedHourlyUSD   float64                         `json:"estimated_hourly_usd"`
	HourlyPriceKnown     bool                            `json:"hourly_price_known,omitempty"`
	Accelerator          *provider.Accelerator           `json:"accelerator,omitempty"`
	StateRevision        int64                           `json:"state_revision"`
	ID                   string                          `json:"id" forge:"id"`
	OrgID                string                          `json:"org_id"`
	CloudID              string                          `json:"cloud_id"`
	ServerID             string                          `json:"server_id"`
	Review               string                          `json:"review"`
	Input                infra.ServerInput               `json:"input"`
	Provider             string                          `json:"provider"`
	AccountID            string                          `json:"account_id"`
	ConnectionID         string                          `json:"connection_id"`
	ConnectionGeneration int64                           `json:"connection_generation,omitempty"`
	ConnectionRevision   time.Time                       `json:"connection_revision"`
	Ownership            string                          `json:"ownership"`
	ResourceID           string                          `json:"resource_id"`
	Status               string                          `json:"status"`
	Message              string                          `json:"message"`
	Attempt              int                             `json:"attempt"`
	Attempted            bool                            `json:"attempted"`
	Actions              []provider.InfrastructureAction `json:"actions"`
	PublicIPv4           []string                        `json:"public_ipv4"`
	HostKey              string                          `json:"host_key,omitempty"`
	Enrolled             bool                            `json:"enrolled"`
	BootstrapExpiresAt   time.Time                       `json:"bootstrap_expires_at"`
	CreatedAt            time.Time                       `json:"created_at"`
	ExpiresAt            time.Time                       `json:"expires_at"`
	UpdatedAt            time.Time                       `json:"updated_at"`
}

// PublicOperation projects a server review for HTTP consumers.
func PublicOperation(v *infra.Operation) *Operation {
	if v == nil {
		return nil
	}
	return &Operation{BudgetReview: publicBudget(v.BudgetReview), MoneyEstimate: publicMoney(v.MoneyEstimate), CapacityTarget: v.CapacityTarget, CapacityCheckedAt: v.CapacityCheckedAt, AbsenceConfirmedAt: v.AbsenceConfirmedAt, EstimatedHourlyUSD: v.EstimatedHourlyUSD, HourlyPriceKnown: v.HourlyPriceKnown, Accelerator: v.Accelerator, StateRevision: v.StateRevision, ID: v.ID, OrgID: v.OrgID, CloudID: v.CloudID, ServerID: v.ServerID, Review: v.Review, Input: v.Input, Provider: v.Provider, AccountID: v.AccountID, ConnectionID: v.ConnectionID, ConnectionGeneration: v.ConnectionGeneration, ConnectionRevision: v.ConnectionRevision, Ownership: v.Ownership, ResourceID: v.ResourceID, Status: v.Status, Message: v.Message, Attempt: v.Attempt, Attempted: v.Attempted, Actions: v.Actions, PublicIPv4: v.PublicIPv4, HostKey: v.HostKey, Enrolled: v.Enrolled, BootstrapExpiresAt: v.BootstrapExpiresAt, CreatedAt: v.CreatedAt, ExpiresAt: v.ExpiresAt, UpdatedAt: v.UpdatedAt}
}
func publicOperations(in []*infra.Operation) []*Operation {
	out := make([]*Operation, 0, len(in))
	for _, v := range in {
		out = append(out, PublicOperation(v))
	}
	return out
}

type CapacityPolicy struct {
	Budget                   *client.CapacityBudget      `json:"budget,omitempty"`
	OrgID                    string                      `json:"org_id"`
	CloudID                  string                      `json:"cloud_id"`
	Revision                 int64                       `json:"revision"`
	Enabled                  bool                        `json:"enabled"`
	RequireExpansionApproval bool                        `json:"require_expansion_approval"`
	Limits                   infra.ResourceVector        `json:"limits"`
	Pools                    []infra.PoolExpansionPolicy `json:"pools"`
	FrontendLimits           []infra.FrontendLimit       `json:"frontend_limits"`
	GPULimits                []infra.GPULimit            `json:"gpu_limits,omitempty"`
}

func publicPolicy(v *infra.CapacityPolicy) *CapacityPolicy {
	if v == nil {
		return nil
	}
	out := &CapacityPolicy{OrgID: v.OrgID, CloudID: v.CloudID, Revision: v.Revision, Enabled: v.Enabled, RequireExpansionApproval: v.RequireExpansionApproval, Limits: v.Limits, Pools: v.Pools, FrontendLimits: v.FrontendLimits, GPULimits: v.GPULimits}
	if v.Budget != nil {
		out.Budget = &client.CapacityBudget{MaxHourlyMicroUSD: strconv.FormatInt(v.Budget.MaxHourlyMicroUSD, 10), MaxHorizonMicroUSD: strconv.FormatInt(v.Budget.MaxHorizonMicroUSD, 10), HorizonSeconds: v.Budget.HorizonSeconds}
	}
	return out
}

type CapacityPolicyInput struct {
	ExpectedRevision int64          `json:"expected_revision"`
	Policy           CapacityPolicy `json:"policy"`
}

func privatePolicy(v CapacityPolicy) (infra.CapacityPolicy, error) {
	out := infra.CapacityPolicy{OrgID: v.OrgID, CloudID: v.CloudID, Revision: v.Revision, Enabled: v.Enabled, RequireExpansionApproval: v.RequireExpansionApproval, Limits: v.Limits, Pools: v.Pools, FrontendLimits: v.FrontendLimits, GPULimits: v.GPULimits}
	if v.Budget != nil {
		hourly, e := parseMicroUSD(v.Budget.MaxHourlyMicroUSD)
		if e != nil {
			return out, e
		}
		horizon, e := parseMicroUSD(v.Budget.MaxHorizonMicroUSD)
		if e != nil {
			return out, e
		}
		out.Budget = &infra.CapacityBudget{MaxHourlyMicroUSD: hourly, MaxHorizonMicroUSD: horizon, HorizonSeconds: v.Budget.HorizonSeconds}
	}
	return out, nil
}
func parseMicroUSD(v string) (int64, error) {
	if v == "" {
		return 0, infra.ErrInvalid
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0, infra.ErrInvalid
		}
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, infra.ErrInvalid
	}
	return n, nil
}

type ScalingPoolInput struct {
	Name             string                    `json:"name"`
	AcceleratorKind  string                    `json:"accelerator_kind,omitempty"`
	ExpectedRevision int64                     `json:"expected_revision"`
	Policy           CapacityPolicy            `json:"policy"`
	Template         infra.PoolExpansionPolicy `json:"template"`
}
type ScalingPool struct {
	Pool   *infra.Pool     `json:"pool"`
	Policy *CapacityPolicy `json:"policy"`
}
