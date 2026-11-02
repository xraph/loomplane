package security

import "strings"

// KindFleet is a service credential limited to one fleet's explicit API.
const KindFleet Kind = "fleet"
const fleetSubjectPrefix = "fleet-key:"

// FleetScope is immutable credential metadata, never caller-selected routing data.
type FleetScope struct {
	ProjectID string `json:"project_id"`
	FleetID   string `json:"fleet_id"`
}

// FleetSubject separates service identities from member and automation identities.
func FleetSubject(keyID string) string { return fleetSubjectPrefix + keyID }
func IsFleetSubject(id string) bool    { return strings.HasPrefix(id, fleetSubjectPrefix) }
func (p Principal) IsFleet() bool      { return p.Kind == KindFleet }
