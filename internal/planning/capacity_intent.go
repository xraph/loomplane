package planning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
)

// CapacityIntent pins the workload you approve before a runtime exists.
// The admitted CloudPlacement is persisted separately from this review.
type CapacityIntent struct {
	Demand             cloudinfra.CapacityDemand      `json:"demand"`
	ProviderPin        cloudinfra.CapacityTemplatePin `json:"provider_pin"`
	Template           cloudinfra.PoolExpansionPolicy `json:"template"`
	TemplateHash       string                         `json:"template_hash"`
	CommitSHA          string                         `json:"commit_sha"`
	AssignmentRevision int64                          `json:"assignment_revision"`
	CloudRevision      int64                          `json:"cloud_revision"`
	TargetRevision     int64                          `json:"target_revision"`
	GrantRevision      int64                          `json:"grant_revision"`
	TargetName         string                         `json:"target_name"`
}

type capacityPolicyReader interface {
	GetCapacityPolicy(context.Context, string, string) (*cloudinfra.CapacityPolicy, error)
}

func (s *PgStore) GetCapacityPolicy(ctx context.Context, org, cloud string) (*cloudinfra.CapacityPolicy, error) {
	return cloudinfra.NewPgStore(s.db).GetCapacityPolicy(ctx, org, cloud)
}

func capacityTemplateHash(p cloudinfra.PoolExpansionPolicy) (string, error) {
	// Ceilings constrain admission without changing the approved workload.
	p.MinServers, p.MaxServers, p.Limits = 0, 0, nil
	return Digest(p)
}

func newCapacityIntent(plan *Plan, a *cloudprofiles.Assignment, target cloudprofiles.Target, req cloudinfra.PlacementRequest, policy *cloudinfra.CapacityPolicy, repo string) (*CapacityIntent, error) {
	if policy == nil || !policy.Enabled || policy.Revision < 1 || req.LocalData || req.StickyServerID != "" || req.Replicas != 1 || req.CPUMillis <= 0 || req.MemoryBytes <= 0 {
		return nil, fmt.Errorf("%w: future capacity needs an enabled pool and one bounded stateless replica", ErrInvalid)
	}
	i := slices.IndexFunc(policy.Pools, func(p cloudinfra.PoolExpansionPolicy) bool { return p.PoolID == target.PoolID })
	if i < 0 {
		return nil, fmt.Errorf("%w: pool has no permitted expansion template", ErrInvalid)
	}
	template := policy.Pools[i]
	if template.ConnectionID == "" || template.Region == "" || template.Image == "" || len(template.AllowedSizes) == 0 || template.Architecture == "" || req.Architecture != "" && req.Architecture != template.Architecture {
		return nil, fmt.Errorf("%w: pool template or architecture is incompatible", ErrInvalid)
	}
	var commit string
	for _, source := range plan.Sources {
		if source.Alias == repo || repo == "" && len(plan.Sources) == 1 {
			commit = source.CommitSHA
		}
	}
	if commit == "" {
		return nil, fmt.Errorf("%w: future capacity needs a pinned source commit", ErrInvalid)
	}
	template.MinServers, template.MaxServers, template.Limits = 0, 0, nil
	hash, err := capacityTemplateHash(template)
	if err != nil {
		return nil, err
	}
	intent := &CapacityIntent{Template: template, Demand: cloudinfra.CapacityDemand{OrgID: plan.OrgID, CloudID: a.CloudID, PoolID: target.PoolID, TargetID: target.ID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, PlanID: plan.ID, Service: req.Service, Architecture: template.Architecture, Resources: cloudinfra.ResourceVector{CPUMillis: req.CPUMillis, MemoryBytes: req.MemoryBytes}, PolicyRevision: policy.Revision}, TemplateHash: hash, CommitSHA: commit, AssignmentRevision: a.Revision, CloudRevision: a.CloudRevision, TargetRevision: target.Revision, GrantRevision: a.GrantRevision, TargetName: target.Name}
	intent.Demand.IntentHash, err = Digest(intent)
	return intent, err
}

func (s *Service) futureCapacity(ctx context.Context, plan *Plan, a *cloudprofiles.Assignment, target cloudprofiles.Target, req cloudinfra.PlacementRequest, repo string, original error) (*CapacityIntent, error) {
	reader, ok := s.store.(capacityPolicyReader)
	if !ok || !errors.Is(original, cloudinfra.ErrConflict) {
		return nil, original
	}
	policy, err := reader.GetCapacityPolicy(ctx, plan.OrgID, a.CloudID)
	if err != nil {
		return nil, err
	}
	if !policy.Enabled {
		return nil, original
	}
	intent, err := newCapacityIntent(plan, a, target, req, policy, repo)
	if err != nil {
		return nil, err
	}
	if s.capacityTemplates == nil {
		return nil, fmt.Errorf("%w: capacity template validation is unavailable", ErrUnavailable)
	}
	pin, err := s.capacityTemplates.ValidateCapacityTemplate(ctx, security.Principal{OrgID: plan.OrgID, UserID: plan.CreatedBy}, intent.Demand, intent.Template)
	if err != nil {
		return nil, err
	}
	if pin == nil || pin.ConnectionGeneration < 1 || pin.AccountID == "" {
		return nil, ErrInvalid
	}
	intent.ProviderPin = *pin
	intent.Demand.IntentHash = ""
	intent.Demand.IntentHash, err = Digest(intent)
	return intent, err
}

// CapacityProvisioner returns candidates. ResolveCapacityIntents owns admission.
type CapacityProvisioner interface {
	EnsureCapacity(context.Context, cloudinfra.CapacityDemand) (*cloudinfra.CapacityDecision, error)
}
type CapacityPrincipalVerifier = security.PrincipalVerifier

type approvedCapacityIntent struct {
	Intent       CapacityIntent
	Actor        security.Principal
	PlanRevision string
}

func capacityReview(d cloudinfra.CapacityDemand) error {
	return &cloudinfra.CapacityAdmissionError{Block: cloudinfra.CapacityBlock{Reason: "review_required", CloudID: d.CloudID, PoolID: d.PoolID, TargetID: d.TargetID, PolicyRevision: d.PolicyRevision}}
}

func (s *PgStore) approvedCapacityIntent(ctx context.Context, org, plan, service string) (*approvedCapacityIntent, error) {
	var intent, actor []byte
	out := &approvedCapacityIntent{}
	err := s.db.QueryRow(ctx, `SELECT i.body,i.actor,i.plan_revision FROM loomplane_capacity_intents i JOIN loomplane_plans p ON p.org_id=i.org_id AND p.id=i.plan_id AND p.status='approved' AND p.revision=i.plan_revision WHERE i.org_id=$1 AND i.plan_id=$2 AND i.service=$3 AND NOT EXISTS(SELECT 1 FROM loomplane_deployments d WHERE d.org_id=p.org_id AND d.plan_id=p.id AND d.status='cancelled') AND loomplane_capacity_intent_current(p,i.body) AND EXISTS(SELECT 1 FROM jsonb_array_elements(p.result->'actions') a WHERE a->>'name'=i.service AND a->'service'->'capacity_intent'=i.body AND a->'service'->>'commit_sha'=i.body->>'commit_sha')`, org, plan, service).Scan(&intent, &actor, &out.PlanRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, capacityReview(cloudinfra.CapacityDemand{OrgID: org, PlanID: plan, Service: service})
	}
	if err != nil {
		return nil, err
	}
	if checkErr := json.Unmarshal(intent, &out.Intent); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := json.Unmarshal(actor, &out.Actor); checkErr != nil {
		return nil, checkErr
	}
	saved := out.Intent.Demand.IntentHash
	out.Intent.Demand.IntentHash = ""
	hash, err := Digest(out.Intent)
	out.Intent.Demand.IntentHash = saved
	if err != nil {
		return nil, err
	}
	templateHash, hashErr := capacityTemplateHash(out.Intent.Template)
	if hashErr != nil {
		return nil, hashErr
	}
	if saved == "" || hash != saved || templateHash != out.Intent.TemplateHash {
		return nil, capacityReview(out.Intent.Demand)
	}
	return out, nil
}

// ResolveApprovedCapacityDemand only reads. Expansion may call it while holding
// the organization and Cloud locks in another transaction.
func (s *Service) ResolveApprovedCapacityDemand(ctx context.Context, org, plan, service string) (*cloudinfra.CapacityDemand, error) {
	st, ok := s.store.(*PgStore)
	if !ok {
		return nil, ErrUnavailable
	}
	saved, err := st.approvedCapacityIntent(ctx, org, plan, service)
	if err != nil {
		return nil, err
	}
	p := saved.Actor
	if p.OrgID != org || p.UserID == "" || p.ReadOnly || s.authz == nil {
		return nil, capacityReview(saved.Intent.Demand)
	}
	if p.KeyID != "" {
		if s.capacityPrincipal == nil {
			return nil, capacityReview(saved.Intent.Demand)
		}
		if err = s.capacityPrincipal.ValidatePrincipal(ctx, p); err != nil {
			return nil, capacityReview(saved.Intent.Demand)
		}
	}
	if err = s.authz.Require(ctx, p, security.DeploymentApprove, saved.Intent.Demand.ProjectID); err != nil {
		return nil, capacityReview(saved.Intent.Demand)
	}
	return &saved.Intent.Demand, nil
}

// ResolveCapacityIntents atomically admits candidates against immutable reviews.
// A pending capacity decision or lost host race leaves the plan unchanged.
func (s *Service) ResolveCapacityIntents(ctx context.Context, org, planID string) error {
	st, ok := s.store.(*PgStore)
	if !ok || s.capacity == nil {
		return ErrUnavailable
	}
	plan, err := st.GetPlan(ctx, org, planID)
	if err != nil {
		return err
	}
	if plan.Status != StatusApproved {
		return ErrNotReady
	}
	var blocked []error
	for _, a := range plan.Actions {
		if a.Service == nil || a.Service.CapacityIntent == nil {
			continue
		}
		checkErr := s.resolveCapacityIntent(ctx, st, plan, a)
		if checkErr != nil {
			var admission *cloudinfra.CapacityAdmissionError
			if !errors.As(checkErr, &admission) {
				return checkErr
			}
			blocked = append(blocked, checkErr)
		}
	}
	return errors.Join(blocked...)
}

func (s *Service) resolveCapacityIntent(ctx context.Context, st *PgStore, plan *Plan, a Action) error {
	org, planID := plan.OrgID, plan.ID
	demand, checkErr := s.ResolveApprovedCapacityDemand(ctx, org, planID, a.Name)
	if checkErr != nil {
		return checkErr
	}
	placed, checkErr := st.ResolvedCapacityPlacements(ctx, org, planID)
	if checkErr != nil {
		return checkErr
	}
	if _, exists := placed[a.Name]; exists {
		return nil
	}
	decision, checkErr := s.capacity.EnsureCapacity(ctx, *demand)
	if checkErr != nil {
		return checkErr
	}
	if decision == nil {
		return ErrUnavailable
	}
	if decision.Block != nil {
		return &cloudinfra.CapacityAdmissionError{Block: *decision.Block}
	}
	if decision.Placement == nil {
		return ErrUnavailable
	}
	// Authorization is rechecked after any provider work and before admission.
	if _, checkErr = s.ResolveApprovedCapacityDemand(ctx, org, planID, a.Name); checkErr != nil {
		return checkErr
	}
	return st.admitCapacityPlacement(ctx, plan, a, *decision.Placement)
}

// ResolvedCapacityPlacements exposes only persisted, admitted runtime pins.
func (s *PgStore) ResolvedCapacityPlacements(ctx context.Context, org, plan string) (map[string]CloudPlacement, error) {
	rows, err := s.db.Query(ctx, `SELECT r.service,r.body FROM loomplane_capacity_placements r JOIN loomplane_capacity_intents i USING(org_id,plan_id,service) WHERE r.org_id=$1 AND r.plan_id=$2 AND r.intent_hash=i.body->'demand'->>'intent_hash'`, org, plan)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]CloudPlacement{}
	for rows.Next() {
		var name string
		var raw []byte
		if checkErr := rows.Scan(&name, &raw); checkErr != nil {
			return nil, checkErr
		}
		var pin CloudPlacement
		if checkErr := json.Unmarshal(raw, &pin); checkErr != nil {
			return nil, checkErr
		}
		out[name] = pin
	}
	return out, rows.Err()
}

func (s *PgStore) admitCapacityPlacement(ctx context.Context, plan *Plan, a Action, candidate cloudinfra.Placement) (err error) {
	intent := a.Service.CapacityIntent
	d := intent.Demand
	if candidate.PoolID != d.PoolID || candidate.CPUMillis != d.Resources.CPUMillis || candidate.MemoryBytes != d.Resources.MemoryBytes || candidate.ServerID == "" || candidate.ConnectionID == "" {
		return capacityReview(d)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = rollback(tx, err) }()
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, plan.OrgID); checkErr != nil {
		return checkErr
	}
	var cancelled bool
	if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_deployments WHERE org_id=$1 AND plan_id=$2 AND status='cancelled')`, plan.OrgID, plan.ID).Scan(&cancelled); checkErr != nil {
		return checkErr
	}
	if cancelled {
		return capacityReview(d)
	}
	var id string
	if checkErr := tx.QueryRow(ctx, `SELECT id FROM loomplane_cloud_profiles WHERE org_id=$1 AND id=$2 FOR UPDATE`, plan.OrgID, d.CloudID).Scan(&id); checkErr != nil {
		return checkErr
	}
	intentJSON, marshalErr := json.Marshal(intent)
	if marshalErr != nil {
		return marshalErr
	}
	var current bool
	if checkErr := tx.QueryRow(ctx, `SELECT p.status='approved' AND p.revision=$3 AND loomplane_capacity_intent_current(p,i.body) AND i.body=$5::jsonb FROM loomplane_plans p JOIN loomplane_capacity_intents i ON i.org_id=p.org_id AND i.plan_id=p.id WHERE p.org_id=$1 AND p.id=$2 AND i.service=$4 FOR UPDATE OF p,i`, plan.OrgID, plan.ID, plan.Revision, a.Name, intentJSON).Scan(&current); checkErr != nil {
		return checkErr
	}
	if !current {
		return capacityReview(d)
	}
	var previous []byte
	err = tx.QueryRow(ctx, `SELECT body FROM loomplane_capacity_placements WHERE org_id=$1 AND plan_id=$2 AND service=$3`, plan.OrgID, plan.ID, a.Name).Scan(&previous)
	if err == nil {
		return tx.Commit()
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var account string
	var details map[string]string
	var raw []byte
	if checkErr := tx.QueryRow(ctx, `SELECT account,details FROM loomplane_cloud_connections WHERE org_id=$1 AND id=$2 AND status='ready' AND generation=$3`, plan.OrgID, candidate.ConnectionID, candidate.ConnectionGeneration).Scan(&account, &raw); checkErr != nil {
		if errors.Is(checkErr, pgx.ErrNoRows) {
			return &cloudinfra.CapacityAdmissionError{Block: cloudinfra.CapacityBlock{Reason: "provisioning", CloudID: d.CloudID, PoolID: d.PoolID, TargetID: d.TargetID, Retryable: true}}
		}
		return checkErr
	}
	if checkErr := json.Unmarshal(raw, &details); checkErr != nil {
		return checkErr
	}
	identity := connections.AccountIdentity("server", account, details)
	if identity == "" || details["architecture"] != d.Architecture {
		return capacityReview(d)
	}
	pin := CloudPlacement{Allocation: candidate.Allocation, PoolID: candidate.PoolID, PoolRevision: candidate.PoolRevision, ServerID: candidate.ServerID, ServerRevision: candidate.ServerRevision, ConnectionID: candidate.ConnectionID, ConnectionGeneration: candidate.ConnectionGeneration, CPUMillis: candidate.CPUMillis, MemoryBytes: candidate.MemoryBytes, CloudID: d.CloudID, CloudRevision: intent.CloudRevision, AssignmentRevision: intent.AssignmentRevision, GrantRevision: intent.GrantRevision, TargetID: d.TargetID, TargetRevision: intent.TargetRevision, TargetName: intent.TargetName, Provider: "server", AccountIdentity: identity}
	var portConflict bool
	if cfgPort := a.Service.Endpoint; a.Service.Public && cfgPort != nil && cfgPort.HostPort > 0 {
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_capacity_reservations r JOIN loomplane_plans p ON p.id=r.plan_id CROSS JOIN LATERAL jsonb_array_elements(p.result->'actions') other WHERE r.org_id=$1 AND r.server_id=$2 AND other->>'name'=r.service AND (r.environment_id<>$3 OR r.service<>$4) AND (other->'service'->'endpoint'->>'host_port')::int=$5)`, plan.OrgID, candidate.ServerID, plan.EnvironmentID, a.Name, cfgPort.HostPort).Scan(&portConflict)
		if err != nil {
			return err
		}
		if portConflict {
			return &cloudinfra.CapacityAdmissionError{Block: cloudinfra.CapacityBlock{Reason: "placement_constraint", CloudID: d.CloudID, PoolID: d.PoolID, TargetID: d.TargetID}}
		}
	}
	cfg := *a.Service
	cfg.CapacityIntent = nil
	cfg.CloudPlacement = &pin
	cfg.Target = pin.ConnectionID
	cfg.TargetSettings = ReviewedConnectionSettings("server", details)
	a.Service = &cfg
	a.ConnectionID = pin.ConnectionID
	result, err := json.Marshal(Result{CloudSyncID: plan.CloudSyncID, Executable: true, Actions: []Action{a}})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT loomplane_admit_capacity_plan(jsonb_populate_record(p,jsonb_build_object('result',$3::jsonb)),p) FROM loomplane_plans p WHERE org_id=$1 AND id=$2`, plan.OrgID, plan.ID, result)
	if err != nil {
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) && pgerr.Code == "23514" {
			return &cloudinfra.CapacityAdmissionError{Block: cloudinfra.CapacityBlock{Reason: "provisioning", CloudID: d.CloudID, PoolID: d.PoolID, TargetID: d.TargetID, Retryable: true}}
		}
		return err
	}
	raw, err = json.Marshal(pin)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_capacity_placements(org_id,plan_id,service,intent_hash,body) VALUES($1,$2,$3,$4,$5)`, plan.OrgID, plan.ID, a.Name, d.IntentHash, raw)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// CapacityTemplateValidator checks live fit after deployment and target authorization.
type CapacityTemplateValidator interface {
	ValidateCapacityTemplate(context.Context, security.Principal, cloudinfra.CapacityDemand, cloudinfra.PoolExpansionPolicy) (*cloudinfra.CapacityTemplatePin, error)
}
