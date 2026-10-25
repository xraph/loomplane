package manageddb

import "strings"

// CloudSQLSourceEndpoint names an externally managed manual PSC endpoint.
// This contract uses a VPC host project, without a Shared VPC service override.
type CloudSQLSourceEndpoint struct {
	ConsumerProject string `json:"consumer_project"`
	Network         string `json:"network"`
	ForwardingRule  string `json:"forwarding_rule"`
}

func (e CloudSQLSourceEndpoint) Valid(region string) bool {
	network := CloudSQLRecoveryNetwork{ConsumerProject: e.ConsumerProject, Network: e.Network}
	parts := strings.Split(e.ForwardingRule, "/")
	return network.Valid() && cloudSQLNetwork.MatchString(region) && len(parts) == 6 && parts[0] == "projects" && parts[1] == e.ConsumerProject && parts[2] == "regions" && parts[3] == region && parts[4] == "forwardingRules" && cloudSQLNetwork.MatchString(parts[5])
}
