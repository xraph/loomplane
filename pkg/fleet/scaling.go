package fleet

import (
	"errors"
	"math"
	"regexp"
)

// ErrScalingPolicy identifies an unsupported or inconsistent policy request.
var ErrScalingPolicy = errors.New("invalid fleet scaling policy")
var scalingServiceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

// ScalingPolicy pins the replica writer and its reviewed bounds. Qualification
// and current cluster capabilities are checked separately before admission.
type ScalingPolicy struct {
	Version                       int                `json:"version"`
	Revision                      int64              `json:"revision"`
	Mode                          string             `json:"mode"`
	MinReplicas                   int                `json:"min_replicas"`
	MaxReplicas                   int                `json:"max_replicas"`
	CPUUtilization                int                `json:"cpu_utilization,omitempty"`
	DownscaleStabilizationSeconds int                `json:"downscale_stabilization_seconds,omitempty"`
	ScaleUpLimit                  int                `json:"scale_up_limit,omitempty"`
	CooldownSeconds               int                `json:"cooldown_seconds,omitempty"`
	Signals                       []ScalingSignalRef `json:"signals,omitempty"`
}

// ScalingSignalRef reserves a versioned adapter reference. Custom signals remain
// blocked until the runtime can verify their registration and qualification.
type ScalingSignalRef struct {
	Name     string `json:"name"`
	Revision int64  `json:"revision"`
}

// ScaleInput is carried by the durable action envelope, which supplies the
// idempotency key and expected instance revision.
type ScaleInput struct {
	Service                string        `json:"service"`
	ExpectedPolicyRevision int64         `json:"expected_policy_revision"`
	Policy                 ScalingPolicy `json:"policy"`
}

// Validate checks the service name and rejects stale or overflowing revisions.
func (in ScaleInput) Validate() error {
	if !scalingServiceName.MatchString(in.Service) || in.ExpectedPolicyRevision < 0 || in.ExpectedPolicyRevision == math.MaxInt64 || in.Policy.Revision != in.ExpectedPolicyRevision+1 {
		return ErrScalingPolicy
	}
	return in.Policy.Validate()
}

// Validate requires explicit bounds and rejects unqualified signal adapters.
func (p ScalingPolicy) Validate() error {
	if p.Version != 1 || p.Revision < 1 || p.MinReplicas < 1 || p.MaxReplicas < p.MinReplicas || p.MaxReplicas > 100 || len(p.Signals) != 0 {
		return ErrScalingPolicy
	}
	switch p.Mode {
	case "manual":
		if p.MinReplicas != p.MaxReplicas || p.CPUUtilization != 0 || p.DownscaleStabilizationSeconds != 0 || p.ScaleUpLimit != 0 || p.CooldownSeconds != 0 {
			return ErrScalingPolicy
		}
	case "automatic":
		if p.CPUUtilization < 1 || p.CPUUtilization > 100 || p.DownscaleStabilizationSeconds < 1 || p.DownscaleStabilizationSeconds > 3600 || p.ScaleUpLimit < 1 || p.ScaleUpLimit > 100 || p.CooldownSeconds < 1 || p.CooldownSeconds > 1800 {
			return ErrScalingPolicy
		}
	default:
		return ErrScalingPolicy
	}
	return nil
}
