package provider

import (
	"regexp"
	"strings"
)

// CloudSQLRecoveryNetwork selects the consumer VPC used by the recovery worker.
// It is configuration, not evidence that PSC endpoints or DNS are available.
// The first protocol uses the network's host project as its consumer project.
type CloudSQLRecoveryNetwork struct {
	ConsumerProject string `json:"consumer_project" yaml:"consumer_project"`
	Network         string `json:"network" yaml:"network"`
}

var (
	cloudSQLProject = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	cloudSQLNetwork = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// Valid accepts canonical VPC resources, not arbitrary URLs or inferred networks.
func (n CloudSQLRecoveryNetwork) Valid() bool {
	parts := strings.Split(n.Network, "/")
	return len(parts) == 5 && parts[0] == "projects" && parts[1] == n.ConsumerProject && cloudSQLProject.MatchString(parts[1]) && parts[2] == "global" && parts[3] == "networks" && cloudSQLNetwork.MatchString(parts[4])
}

// CloudSQLSourceEndpoint names an externally managed manual PSC endpoint.
// This contract uses a VPC host project, without a Shared VPC service override.
type CloudSQLSourceEndpoint struct {
	ConsumerProject string `json:"consumer_project" yaml:"consumer_project"`
	Network         string `json:"network" yaml:"network"`
	ForwardingRule  string `json:"forwarding_rule" yaml:"forwarding_rule"`
}

func (e CloudSQLSourceEndpoint) Valid(region string) bool {
	network := CloudSQLRecoveryNetwork{ConsumerProject: e.ConsumerProject, Network: e.Network}
	parts := strings.Split(e.ForwardingRule, "/")
	return network.Valid() && cloudSQLNetwork.MatchString(region) && len(parts) == 6 && parts[0] == "projects" && parts[1] == e.ConsumerProject && parts[2] == "regions" && parts[3] == region && parts[4] == "forwardingRules" && cloudSQLNetwork.MatchString(parts[5])
}

// Settings returns the canonical, non-secret connection setting.
func (n CloudSQLRecoveryNetwork) Settings() map[string]string {
	if !n.Valid() {
		return nil
	}
	return map[string]string{"cloud_sql_recovery_network": n.Network}
}
