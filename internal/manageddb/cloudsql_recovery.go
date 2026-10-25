package manageddb

import (
	"errors"
	"regexp"
	"strings"
)

// CloudSQLRecoveryNetwork selects the consumer VPC used by the recovery worker.
// It is configuration, not evidence that PSC endpoints or DNS are available.
// The first protocol uses the network's host project as its consumer project.
type CloudSQLRecoveryNetwork struct {
	ConsumerProject string `json:"consumer_project"`
	Network         string `json:"network"`
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

// CloudSQLRecoverySettings keeps recovery optional for existing GCP accounts.
// Shared VPC service-project overrides need a separate reviewed contract.
func CloudSQLRecoverySettings(settings map[string]string) (CloudSQLRecoveryNetwork, error) {
	network := strings.TrimSpace(settings["cloud_sql_recovery_network"])
	if strings.TrimSpace(settings["cloud_sql_recovery_consumer_project"]) != "" {
		return CloudSQLRecoveryNetwork{}, errors.New("cloud sql recovery uses the VPC host project as its PSC consumer project")
	}
	if network == "" {
		return CloudSQLRecoveryNetwork{}, nil
	}
	parts := strings.Split(network, "/")
	n := CloudSQLRecoveryNetwork{Network: network}
	if len(parts) > 1 {
		n.ConsumerProject = parts[1]
	}
	if !n.Valid() {
		return CloudSQLRecoveryNetwork{}, errors.New("configure the Cloud SQL recovery VPC as projects/PROJECT/global/networks/NETWORK")
	}
	return n, nil
}

// Settings returns the canonical, non-secret connection setting.
func (n CloudSQLRecoveryNetwork) Settings() map[string]string {
	if !n.Valid() {
		return nil
	}
	return map[string]string{"cloud_sql_recovery_network": n.Network}
}
