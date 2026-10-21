package config

import (
	"errors"
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
	if !c.Runtime.Enabled {
		return nil
	}
	if !c.Enabled {
		return errors.New("federation.runtime requires federation.enabled")
	}
	return c.Runtime.Validate()
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
	if !domain.ValidIdentitySegment(c.OrganizationID) {
		return errors.New("federation.runtime.organization_id is invalid")
	}
	td, err := spiffeid.TrustDomainFromString(c.TrustDomain)
	if err != nil || td.Name() != c.TrustDomain {
		return errors.New("federation.runtime.trust_domain is invalid")
	}
	if !domain.ValidWorkloadAPIAddress(c.WorkloadAPI) {
		return errors.New("federation.runtime.workload_api requires a canonical Unix socket address")
	}
	if !filepath.IsAbs(c.SigningKeysFile) || filepath.Clean(c.SigningKeysFile) != c.SigningKeysFile {
		return errors.New("federation.runtime.signing_keys_file requires a canonical absolute path")
	}
	return nil
}
