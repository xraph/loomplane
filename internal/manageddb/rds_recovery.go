package manageddb

import (
	"errors"
	"regexp"
	"strings"
)

// RDSRecoveryNetwork is an existing private route for the recovery worker.
// Provider metadata and the actual SQL connection must still verify it.
type RDSRecoveryNetwork struct {
	VPCID           string `json:"vpc_id"`
	SubnetGroup     string `json:"subnet_group"`
	SecurityGroupID string `json:"security_group_id"`
	VerifierGroupID string `json:"verifier_group_id"`
}

var rdsNetworkName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,254}$`)

// Valid requires a separate verifier group, not application ingress or a CIDR.
func (n RDSRecoveryNetwork) Valid() bool {
	return strings.HasPrefix(n.VPCID, "vpc-") && rdsNetworkName.MatchString(n.VPCID) && rdsNetworkName.MatchString(n.SubnetGroup) && strings.HasPrefix(n.SecurityGroupID, "sg-") && rdsNetworkName.MatchString(n.SecurityGroupID) && strings.HasPrefix(n.VerifierGroupID, "sg-") && rdsNetworkName.MatchString(n.VerifierGroupID) && n.SecurityGroupID != n.VerifierGroupID
}

// RDSRecoverySettings preserves all four route fields together. Empty means
// native restore is unavailable, without disabling normal AWS deployments.
func RDSRecoverySettings(settings map[string]string) (RDSRecoveryNetwork, error) {
	n := RDSRecoveryNetwork{VPCID: strings.TrimSpace(settings["rds_recovery_vpc"]), SubnetGroup: strings.TrimSpace(settings["rds_recovery_subnet_group"]), SecurityGroupID: strings.TrimSpace(settings["rds_recovery_security_group"]), VerifierGroupID: strings.TrimSpace(settings["rds_recovery_verifier_group"])}
	if n == (RDSRecoveryNetwork{}) {
		return n, nil
	}
	if !n.Valid() {
		return RDSRecoveryNetwork{}, errors.New("configure all four private RDS recovery network fields with separate copy and verifier groups")
	}
	return n, nil
}

// Settings contains only route configuration, never credentials or proof.
func (n RDSRecoveryNetwork) Settings() map[string]string {
	if !n.Valid() {
		return nil
	}
	return map[string]string{"rds_recovery_vpc": n.VPCID, "rds_recovery_subnet_group": n.SubnetGroup, "rds_recovery_security_group": n.SecurityGroupID, "rds_recovery_verifier_group": n.VerifierGroupID}
}
