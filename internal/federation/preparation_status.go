package federation

import (
	"context"
	"math"
	"time"

	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/agentinstall"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// PreparationStatus describes a point-in-time observation, never permission to
// route traffic. policy_current expires at PolicyValidUntil even without refresh.
type PreparationStatus struct {
	domain.Scope
	PreparationID           string     `json:"preparation_id"`
	DestinationID           string     `json:"destination_id"`
	DestinationGeneration   int64      `json:"destination_generation"`
	IdentityRevision        string     `json:"identity_revision"`
	State                   string     `json:"state"`
	PreparationState        string     `json:"preparation_state"`
	LaunchState             string     `json:"launch_state,omitempty"`
	EnrollmentPhase         string     `json:"enrollment_phase,omitempty"`
	AgentID                 string     `json:"agent_id,omitempty"`
	Epoch                   uint64     `json:"epoch,omitempty"`
	PolicyGeneration        uint64     `json:"policy_generation,omitempty"`
	CurrentPolicyGeneration uint64     `json:"current_policy_generation,omitempty"`
	Sequence                uint64     `json:"sequence,omitempty"`
	AcknowledgedSequence    uint64     `json:"acknowledged_sequence,omitempty"`
	AcknowledgedAt          *time.Time `json:"acknowledged_at,omitempty"`
	PolicyValidUntil        *time.Time `json:"policy_valid_until,omitempty"`
	ObservedAt              time.Time  `json:"observed_at"`
}
type PreparationStatusReader interface {
	ReadPreparationStatus(context.Context, string, string, string) (*PreparationStatus, error)
}
type PreparationStatusService struct {
	installations *InstallationService
	store         PreparationStatusReader
	now           func() time.Time
}

func NewPreparationStatusService(installations *InstallationService, store PreparationStatusReader) *PreparationStatusService {
	return &PreparationStatusService{installations: installations, store: store, now: time.Now}
}
func (s *PreparationStatusService) Get(ctx context.Context, p security.Principal, destination, id string) (*PreparationStatus, error) {
	if s == nil || s.installations == nil || s.store == nil {
		return nil, ErrDisabled
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if checkErr := s.installations.authorize(ctx, p, destination, false); checkErr != nil {
		return nil, checkErr
	}
	d, err := s.installations.destinations.verifiedDestination(ctx, p.OrgID, destination)
	if err != nil {
		return nil, err
	}
	result, err := s.store.ReadPreparationStatus(ctx, p.OrgID, destination, id)
	if err != nil {
		return nil, err
	}
	if result == nil || result.Scope != d.Scope || result.DestinationID != d.ID || result.PreparationID != id || result.DestinationGeneration != d.Generation || result.IdentityRevision != d.Identity.Revision() {
		return nil, ErrIdentityChanged
	}
	if checkErr := s.installations.authorize(ctx, p, destination, false); checkErr != nil {
		return nil, checkErr
	}
	current, err := s.installations.destinations.verifiedDestination(ctx, p.OrgID, destination)
	if err != nil {
		return nil, err
	}
	if current.Scope != d.Scope || current.Generation != d.Generation || current.Identity != d.Identity || current.ConnectionID != d.ConnectionID {
		return nil, ErrIdentityChanged
	}
	// The database read and final authorization may consume the remaining lease.
	if result.State == "policy_current" && (result.PolicyValidUntil == nil || !result.PolicyValidUntil.After(s.now())) {
		result.State = "lease_expired"
	}
	return result, nil
}

type preparationEvidence struct {
	preparation             *Preparation
	destination             *domain.Destination
	installation            *KubernetesInstallation
	launch                  *LaunchAttempt
	enrollment              *workloadidentity.EnrollmentRecord
	phase                   string
	epoch, policyGeneration uint64
	active                  bool
	delivery                *access.DeliveryState
}

func (e preparationEvidence) status(now time.Time) *PreparationStatus {
	p, d := e.preparation, e.destination
	out := &PreparationStatus{Scope: p.Scope, PreparationID: p.ID, DestinationID: p.DestinationID, DestinationGeneration: d.Generation, IdentityRevision: d.Identity.Revision(), PreparationState: p.State, State: "superseded", ObservedAt: now.UTC()}
	if e.launch != nil {
		out.LaunchState = e.launch.State
	}
	out.EnrollmentPhase = e.phase
	if e.enrollment != nil {
		out.AgentID = e.enrollment.Binding.Agent.AgentID
		out.Epoch = e.enrollment.Binding.Agent.Epoch
	}
	if p.State == PreparationRetiring || p.State == PreparationRetired {
		out.State = p.State
		return out
	}
	if d.Quarantined || p.IdentityRevision != d.Identity.Revision() || p.NamespaceUID != d.Identity.NamespaceUID {
		out.State = "unverified"
		return out
	}
	if p.DestinationGeneration == math.MaxInt64 || d.Generation < p.DestinationGeneration || d.Generation > p.DestinationGeneration+1 {
		return out
	}
	if p.State != PreparationPrepared {
		if d.Generation != p.DestinationGeneration || e.installation != nil || e.launch != nil || p.EnrollmentID != "" {
			return out
		}
		out.State = "preparing"
		return out
	}
	if e.installation == nil {
		if d.Generation == p.DestinationGeneration && p.EnrollmentID == "" && e.launch == nil {
			out.State = "awaiting_launch"
		}
		return out
	}
	i := e.installation
	name := "loomplane-agent-" + p.ID
	if !profileMatchesDestination(i.Profile, *d) || i.Generation != d.Generation || d.Generation != p.DestinationGeneration+1 || i.Profile.PodUID != p.PodUID || i.Profile.PodName != name || i.Profile.ServiceAccount != name || i.Profile.Container != agentinstall.ContainerName {
		return out
	}
	if p.EnrollmentID == "" {
		out.State = "awaiting_launch"
		return out
	}
	r := e.enrollment
	if r == nil || e.phase == "revoking" || e.phase == "revoked" {
		out.State = "revoked"
		return out
	}
	if r.ID != p.EnrollmentID || r.Binding.Agent.Scope != p.Scope || r.Binding.Agent.DestinationID != d.ID || r.Binding.Agent != r.Registration.Agent || r.Registration.DestinationGeneration != d.Generation || !installationBindingIdentityMatches(r.Binding, *d, i.Profile) || r.Binding.Namespace != p.Namespace || r.Binding.NamespaceUID != p.NamespaceUID || r.Binding.Agent.Epoch != e.epoch {
		return out
	}
	if e.phase != "consumed" && !r.ExpiresAt.After(now) {
		out.State = "bootstrap_expired"
		return out
	}
	a := e.launch
	if a == nil {
		out.State = "awaiting_launch"
		return out
	}
	if a.Scope != p.Scope || a.DestinationID != d.ID || a.ID != p.ID || a.EnrollmentID != r.ID || a.PodUID != p.PodUID || a.Namespace != p.Namespace || a.ConfigMapName != name || a.IdentityRevision != p.IdentityRevision || a.DestinationGeneration != d.Generation {
		return out
	}
	if a.State == LaunchRetiring || a.State == LaunchRetired {
		out.State = "retiring"
		return out
	}
	if a.State != LaunchDelivered || a.ConfigMapUID == "" {
		out.State = "delivery_pending"
		return out
	}
	if e.phase != "consumed" {
		out.State = "enrolling"
		return out
	}
	if !e.active {
		out.State = "revoked"
		return out
	}
	out.State = "awaiting_policy"
	v := e.delivery
	if v == nil {
		return out
	}
	out.PolicyGeneration = v.PolicyGeneration
	out.CurrentPolicyGeneration = e.policyGeneration
	out.Sequence = v.Sequence
	out.AcknowledgedSequence = v.AcknowledgedSequence
	if v.Agent != r.Binding.Agent || v.Revision == "" || v.Sequence == 0 {
		return out
	}
	if v.Sequence != v.AcknowledgedSequence || v.PolicyGeneration == 0 || v.PolicyGeneration != v.AcknowledgedPolicyGeneration || v.PolicyGeneration != e.policyGeneration {
		out.State = "policy_pending"
		return out
	}
	if v.ReceivedAt.IsZero() || v.ReceivedAt.After(now) || v.IssuedAt.After(now) || v.ReceivedAt.Before(v.IssuedAt) {
		return out
	}
	received, until := v.ReceivedAt, v.ExpiresAt.Add(-domain.MaxClockSkew)
	out.AcknowledgedAt = &received
	out.PolicyValidUntil = &until
	if !until.After(now) {
		out.State = "lease_expired"
		return out
	}

	out.State = "policy_current"
	return out
}
