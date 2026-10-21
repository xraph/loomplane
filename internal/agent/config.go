package agent

import (
	"errors"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/secureconfig"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

var ErrConfiguration = errors.New("invalid agent installation configuration")

// LaunchConfig is delivered by the trusted installer. Its registration mirrors
// server-owned enrollment; changing local bounds cannot change server authority.
// SigningKeys contains public verification keys, never signing private keys.
type LaunchConfig struct {
	Version             int                               `json:"version"`
	ControlEndpoint     string                            `json:"control_endpoint"`
	WorkloadAPI         string                            `json:"workload_api"`
	StatePath           string                            `json:"state_path"`
	EnrollmentID        string                            `json:"enrollment_id"`
	EnrollmentExpiresAt time.Time                         `json:"enrollment_expires_at"`
	Registration        runtimecatalog.Registration       `json:"registration"`
	Namespace           string                            `json:"namespace"`
	Targets             []runtimecatalog.KubernetesTarget `json:"targets"`
	TrustGeneration     uint64                            `json:"trust_generation"`
	SigningKeys         []access.SnapshotKey              `json:"signing_keys"`
}

func LoadLaunchConfig(path string) (LaunchConfig, error) {
	var cfg LaunchConfig
	if err := secureconfig.ReadJSON(path, 1<<20, false, &cfg); err != nil {
		return LaunchConfig{}, ErrConfiguration
	}
	if err := cfg.Validate(time.Now()); err != nil {
		return LaunchConfig{}, err
	}
	return cfg, nil
}

func (c LaunchConfig) Validate(now time.Time) error {
	identity, err := workloadidentity.ParseAgentURI(c.Registration.Agent.URI)
	ticket, ticketErr := uuid.Parse(c.EnrollmentID)
	if c.Version != 1 || err != nil || identity != c.Registration.Agent || ticketErr != nil || ticket.String() != c.EnrollmentID || !c.EnrollmentExpiresAt.After(now) || c.EnrollmentExpiresAt.After(now.Add(workloadidentity.EnrollmentLifetime+domain.MaxClockSkew)) {
		return ErrConfiguration
	}
	if _, endpointErr := parseControlEndpoint(c.ControlEndpoint); endpointErr != nil {
		return ErrConfiguration
	}
	if !domain.ValidWorkloadAPIAddress(c.WorkloadAPI) || !filepath.IsAbs(c.StatePath) || filepath.Clean(c.StatePath) != c.StatePath || c.StatePath == "/" {
		return ErrConfiguration
	}
	if targetsErr := runtimecatalog.ValidateKubernetesTargets(c.Registration, c.Namespace, c.Targets); targetsErr != nil {
		return ErrConfiguration
	}
	_, err = c.snapshotTrust(now)
	return err
}
func (c LaunchConfig) snapshotTrust(now time.Time) (*access.SnapshotTrust, error) {
	trust, err := access.NewSnapshotTrust(c.Registration.Agent.Scope, c.TrustGeneration, c.SigningKeys)
	if err != nil {
		return nil, ErrConfiguration
	}
	for _, key := range c.SigningKeys {
		if !now.Before(key.NotBefore) && key.NotAfter.After(now.Add(domain.MaxClockSkew)) {
			return trust, nil
		}
	}
	return nil, ErrConfiguration
}
