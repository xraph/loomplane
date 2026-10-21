package config

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"

	"github.com/spiffe/go-spiffe/v2/spiffeid"

	domain "github.com/xraph/loomplane/pkg/federation"
)

// FederationRuntimeConfig enables a separate agent listener using SPIRE identity
// and signing keys provisioned for this organization's project environments.
type FederationRuntimeConfig struct {
	Enabled         bool
	ListenAddress   string
	OrganizationID  string
	TrustDomain     string
	WorkloadAPI     string
	SigningKeysFile string
}

func (c FederationConfig) Validate() error {
	if (c.Runtime.Enabled || c.Identity.Enabled) && !c.Enabled {
		return errors.New("federation runtime and identity require federation.enabled")
	}
	if c.Runtime.Enabled && c.Identity.Enabled && (c.Runtime.OrganizationID != c.Identity.OrganizationID || c.Runtime.TrustDomain != c.Identity.TrustDomain) {
		return errors.New("federation runtime and identity must use the same organization and trust domain")
	}
	if c.Launch.Enabled && (!c.Runtime.Enabled || !c.Identity.Enabled) {
		return errors.New("federation launch requires runtime and identity")
	}
	return errors.Join(c.Runtime.Validate(), c.Identity.Validate(), c.Launch.Validate())
}

// FederationIdentityConfig enables local SPIRE administration and enrollment cleanup.
type FederationIdentityConfig struct {
	Enabled        bool
	OrganizationID string
	TrustDomain    string
	AdminSocket    string
}

func (c FederationIdentityConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if err := validateFederationScope("identity", c.OrganizationID, c.TrustDomain); err != nil {
		return err
	}
	if !domain.ValidUnixSocketAddress(c.AdminSocket) {
		return errors.New("federation.identity.admin_socket requires a canonical Unix socket address")
	}
	return nil
}

func validateFederationScope(component, org, trustDomain string) error {
	if !domain.ValidIdentitySegment(org) {
		return fmt.Errorf("federation.%s.organization_id is invalid", component)
	}
	td, err := spiffeid.TrustDomainFromString(trustDomain)
	if err != nil || td.Name() != trustDomain {
		return fmt.Errorf("federation.%s.trust_domain is invalid", component)
	}
	return nil
}

func (c FederationRuntimeConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	host, port, err := net.SplitHostPort(c.ListenAddress)
	number, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || net.ParseIP(host) == nil || number < 1 || number > 65535 {
		return errors.New("federation.runtime.listen_address requires an IP address and port (1-65535)")
	}
	if err := validateFederationScope("runtime", c.OrganizationID, c.TrustDomain); err != nil {
		return err
	}
	if !domain.ValidWorkloadAPIAddress(c.WorkloadAPI) {
		return errors.New("federation.runtime.workload_api requires a canonical Unix socket address")
	}
	if !filepath.IsAbs(c.SigningKeysFile) || filepath.Clean(c.SigningKeysFile) != c.SigningKeysFile {
		return errors.New("federation.runtime.signing_keys_file requires a canonical absolute path")
	}
	return nil
}

// FederationLaunchConfig enables guarded delivery to an already recorded pod.
type FederationLaunchConfig struct {
	Enabled         bool
	ControlEndpoint string
	AgentImage      string
	TrustGeneration int64
}

func (c FederationLaunchConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if !domain.ValidControlEndpoint(c.ControlEndpoint) || !domain.ValidAgentImage(c.AgentImage) || c.TrustGeneration < 1 {
		return errors.New("federation launch requires an HTTPS origin, a pinned agent image and a positive trust generation")
	}
	return nil
}
