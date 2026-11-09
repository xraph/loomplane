package fleets

import (
	"errors"
	"fmt"
	"time"

	"github.com/xraph/loomplane/pkg/fleet"
)

// ErrScalingEvidence identifies invalid observation identities or replica counts.
var ErrScalingEvidence = errors.New("invalid fleet scaling evidence")

// ErrScalingQualification keeps requests blocked until runtime evidence is accepted.
var ErrScalingQualification = fmt.Errorf("%w: fleet scaling qualification is unavailable", ErrApprovalRequired)

// ValidateScalingSnapshot checks the requested bounds against a sealed profile.
// Automatic scaling and multiple replicas need registered qualification evidence.
// A template declaration alone cannot satisfy that requirement.
func ValidateScalingSnapshot(snapshot InstanceSnapshot, in fleet.ScaleInput) error {
	if err := in.Validate(); err != nil {
		return err
	}
	profile, ok := snapshot.Template.Profiles[snapshot.ProfileID]
	if !ok {
		return ErrInvalid
	}
	size, ok := profile.Services[in.Service]
	if !ok || in.Policy.MinReplicas < size.MinReplicas || in.Policy.MaxReplicas > size.MaxReplicas {
		return ErrInvalid
	}
	demand, err := FleetDemand(snapshot)
	if err != nil {
		return err
	}
	if in.Policy.Mode == "automatic" || in.Policy.MaxReplicas > 1 || demand.GPUs > 0 {
		return ErrScalingQualification
	}
	return nil
}

// ScalingEvidence comes from persisted observations for one generation. A worker
// must revalidate its authority and ownership receipt before any replica effect.
// StableSince marks a continuous complete recommendation history, not one sample.
type ScalingEvidence struct {
	CurrentReplicas       int
	RecommendedReplicas   int
	Generation            int64
	ObservedGeneration    int64
	PolicyRevision        int64
	ObservedAt            time.Time
	MetricsKnown          bool
	Complete              bool
	StableSince           time.Time
	LastScaleAt           time.Time
	ManualWriterConfirmed bool
}

// ScalingDecision is a safety check on a scaler recommendation. Allowed does not
// grant authority, reserve capacity or replace the durable operation receipt.
type ScalingDecision struct {
	Allowed          bool
	PreviousReplicas int
	DesiredReplicas  int
	PolicyRevision   int64
	Writer           string
	Reason           string
	ObservedAt       time.Time
}

// EvaluateScaling checks freshness, stabilization and writer ownership before a
// recommendation can reach the separately authorized effect path.
func EvaluateScaling(policy fleet.ScalingPolicy, e ScalingEvidence, now time.Time) (ScalingDecision, error) {
	d := ScalingDecision{PreviousReplicas: e.CurrentReplicas, DesiredReplicas: e.CurrentReplicas, PolicyRevision: policy.Revision, Writer: "hpa", ObservedAt: e.ObservedAt}
	if err := policy.Validate(); err != nil {
		return d, err
	}
	if now.IsZero() || e.CurrentReplicas < 1 || e.CurrentReplicas > 100 || e.Generation < 1 {
		return d, ErrScalingEvidence
	}
	if policy.Mode == "manual" {
		d.Writer = "manual"
	}
	if e.ObservedAt.IsZero() || e.ObservedAt.After(now) || now.Sub(e.ObservedAt) > time.Minute || e.ObservedGeneration != e.Generation || e.PolicyRevision != policy.Revision {
		d.Reason = "metric_stale"
		return d, nil
	}
	if policy.Mode == "manual" {
		if !e.ManualWriterConfirmed {
			d.Reason = "writer_transfer_pending"
			return d, nil
		}
		d.Allowed = true
		d.DesiredReplicas = policy.MinReplicas
		d.Reason = "manual_requested"
		return d, nil
	}
	if !e.MetricsKnown || !e.Complete {
		d.Reason = "metric_unknown"
		return d, nil
	}
	if e.RecommendedReplicas < 1 || e.RecommendedReplicas > 100 {
		return d, ErrScalingEvidence
	}
	if e.LastScaleAt.After(now) || !e.LastScaleAt.IsZero() && now.Sub(e.LastScaleAt) < time.Duration(policy.CooldownSeconds)*time.Second {
		d.Reason = "cooldown"
		return d, nil
	}
	desired := max(policy.MinReplicas, min(policy.MaxReplicas, e.RecommendedReplicas))
	switch {
	case desired < e.CurrentReplicas:
		if e.StableSince.IsZero() || e.StableSince.After(e.ObservedAt) || now.Sub(e.StableSince) < time.Duration(policy.DownscaleStabilizationSeconds)*time.Second {
			d.Reason = "stabilizing"
			return d, nil
		}
		d.Reason = "metric_scale_down"
	case desired > e.CurrentReplicas:
		desired = min(desired, e.CurrentReplicas+policy.ScaleUpLimit)
		d.Reason = "metric_scale_up"
	default:
		d.Reason = "within_bounds"
		return d, nil
	}
	d.Allowed = true
	d.DesiredReplicas = desired
	return d, nil
}
