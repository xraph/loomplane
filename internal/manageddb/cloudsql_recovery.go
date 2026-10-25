package manageddb

import (
	"errors"
	"strings"

	"github.com/xraph/loomplane/pkg/provider"
)

// CloudSQLRecoveryNetwork is the shared reviewed recovery route.
type CloudSQLRecoveryNetwork = provider.CloudSQLRecoveryNetwork

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
