package federation

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.jetify.com/typeid/v2"
)

var ErrGroupProfile = errors.New("placement group profile is not qualified")

// DrainPolicy describes reviewed application behavior. Bounded mode also needs
// saved operator verification of the matching timeout configuration.
type DrainPolicy struct {
	MaxConnectionLifetimeSeconds *int64 `json:"max_connection_lifetime_seconds,omitempty"`
	TerminationGraceSeconds      int64  `json:"termination_grace_seconds"`
	Mode                         string `json:"mode"`
}

func (d DrainPolicy) Validate() error {
	if d.TerminationGraceSeconds < 0 {
		return ErrGroupProfile
	}
	switch d.Mode {
	case "bounded":
		if d.MaxConnectionLifetimeSeconds == nil || *d.MaxConnectionLifetimeSeconds <= 0 || d.TerminationGraceSeconds < *d.MaxConnectionLifetimeSeconds {
			return ErrGroupProfile
		}
	case "attested":
		if d.MaxConnectionLifetimeSeconds != nil && *d.MaxConnectionLifetimeSeconds <= 0 {
			return ErrGroupProfile
		}
	default:
		return ErrGroupProfile
	}
	return nil
}

// TLSRenewalAuthority records external operator responsibility, not a guarantee
// that renewal will succeed. Certificate expiry remains a live observation.
type TLSRenewalAuthority struct {
	ID                      string    `json:"id"`
	OrgID                   string    `json:"org_id"`
	SchemaVersion           int       `json:"schema_version"`
	Generation              int64     `json:"generation"`
	OwnerRef                string    `json:"owner_ref"`
	Method                  string    `json:"method"`
	VerificationEvidenceRef string    `json:"verification_evidence_ref"`
	VerifiedBy              string    `json:"verified_by"`
	VerifiedAt              time.Time `json:"verified_at"`
	Active                  bool      `json:"active"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
}

// MemberPrerequisite stores identities resolved through the attached namespace.
// Observation timestamps and certificate renewals do not enter a review pin.
type MemberPrerequisite struct {
	ID                         string      `json:"id"`
	OrgID                      string      `json:"org_id"`
	ProjectID                  string      `json:"project_id"`
	AttachmentID               string      `json:"attachment_id"`
	SchemaVersion              int         `json:"schema_version"`
	Revision                   int64       `json:"revision"`
	Namespace                  string      `json:"namespace"`
	NamespaceUID               string      `json:"namespace_uid"`
	TLSSecretName              string      `json:"tls_secret_name"`
	TLSSecretUID               string      `json:"tls_secret_uid"`
	Hostnames                  []string    `json:"hostnames"`
	RenewalAuthorityID         string      `json:"renewal_authority_id"`
	RenewalAuthorityGeneration int64       `json:"renewal_authority_generation"`
	DrainPolicy                DrainPolicy `json:"drain_policy"`
	TimeoutVerificationID      string      `json:"timeout_verification_id,omitempty"`
	CreatedAt                  time.Time   `json:"created_at"`
	UpdatedAt                  time.Time   `json:"updated_at"`
}

// PrerequisitePin excludes observation and storage times from approval digests.
type PrerequisitePin struct {
	ID                         string      `json:"id"`
	Revision                   int64       `json:"revision"`
	AttachmentID               string      `json:"attachment_id"`
	Namespace                  string      `json:"namespace"`
	NamespaceUID               string      `json:"namespace_uid"`
	TLSSecretName              string      `json:"tls_secret_name"`
	TLSSecretUID               string      `json:"tls_secret_uid"`
	Hostnames                  []string    `json:"hostnames"`
	RenewalAuthorityID         string      `json:"renewal_authority_id"`
	RenewalAuthorityGeneration int64       `json:"renewal_authority_generation"`
	DrainPolicy                DrainPolicy `json:"drain_policy"`
	TimeoutVerificationID      string      `json:"timeout_verification_id,omitempty"`
}

func (p MemberPrerequisite) Pin() PrerequisitePin {
	drain := p.DrainPolicy
	if drain.MaxConnectionLifetimeSeconds != nil {
		value := *drain.MaxConnectionLifetimeSeconds
		drain.MaxConnectionLifetimeSeconds = &value
	}
	return PrerequisitePin{ID: p.ID, Revision: p.Revision, AttachmentID: p.AttachmentID, Namespace: p.Namespace, NamespaceUID: p.NamespaceUID, TLSSecretName: p.TLSSecretName, TLSSecretUID: p.TLSSecretUID, Hostnames: append([]string(nil), p.Hostnames...), RenewalAuthorityID: p.RenewalAuthorityID, RenewalAuthorityGeneration: p.RenewalAuthorityGeneration, DrainPolicy: drain, TimeoutVerificationID: p.TimeoutVerificationID}
}

// MemberIdentity is independent of the logical service name and retry epoch.
type MemberIdentity struct {
	GroupID    string `json:"group_id"`
	Generation int64  `json:"generation"`
	MemberID   string `json:"member_id"`
	BindingID  string `json:"binding_id"`
	InstanceID string `json:"instance_id"`
	RuntimeRef string `json:"runtime_ref"`
	ReleaseID  string `json:"release_id"`
}

func NewMemberIdentity(group string, generation int64, member string) (MemberIdentity, error) {
	if !ValidIdentitySegment(group) || generation < 1 || !ValidIdentitySegment(member) {
		return MemberIdentity{}, ErrGroupProfile
	}
	raw, err := json.Marshal([]any{group, generation, member})
	if err != nil {
		return MemberIdentity{}, err
	}
	sum := sha256.Sum256(raw)
	binding, err := typeid.FromBytes("pgb", sum[:16])
	if err != nil {
		return MemberIdentity{}, err
	}
	instance, err := typeid.FromBytes("inst", sum[:16])
	if err != nil {
		return MemberIdentity{}, err
	}
	release, err := typeid.FromBytes("pgr", sum[:16])
	if err != nil {
		return MemberIdentity{}, err
	}
	return MemberIdentity{GroupID: group, Generation: generation, MemberID: member, BindingID: binding.String(), InstanceID: instance.String(), RuntimeRef: fmt.Sprintf("group/%s/%d/%s", group, generation, member), ReleaseID: release.String()}, nil
}

type GroupRoutingReview struct {
	Mode             string `json:"mode"`
	Hostname         string `json:"hostname"`
	ZoneConnectionID string `json:"zone_connection_id,omitempty"`
}

type GroupMemberPlan struct {
	LocationIdentity         string           `json:"location_identity"`
	MappingRevision          int64            `json:"mapping_revision"`
	Identity                 MemberIdentity   `json:"identity"`
	Replicas                 int              `json:"replicas"`
	DestinationID            string           `json:"destination_id"`
	DestinationGeneration    int64            `json:"destination_generation"`
	PhysicalIdentity         PhysicalIdentity `json:"physical_identity"`
	CapabilityRevision       string           `json:"capability_revision"`
	CloudID                  string           `json:"cloud_id"`
	TargetID                 string           `json:"target_id"`
	TargetRevision           int64            `json:"target_revision"`
	AttachmentRevision       int64            `json:"attachment_revision"`
	ConnectionID             string           `json:"connection_id"`
	ConnectionGeneration     int64            `json:"connection_generation"`
	LocationID               string           `json:"location_id"`
	LocationRevision         int64            `json:"location_revision"`
	LocationEvidenceRevision int64            `json:"location_evidence_revision"`
	GrantRevision            int64            `json:"grant_revision"`
	Prerequisite             PrerequisitePin  `json:"prerequisite"`
}

// QualifiedGroupPlan is a review snapshot. It grants no runtime authority.
type QualifiedGroupPlan struct {
	Scope
	SchemaVersion  int                `json:"schema_version"`
	GroupID        string             `json:"group_id"`
	Generation     int64              `json:"generation"`
	Revision       string             `json:"revision"`
	Name           string             `json:"name"`
	Service        string             `json:"service"`
	ConfigRevision string             `json:"config_revision"`
	Image          string             `json:"image"`
	Members        []GroupMemberPlan  `json:"members"`
	Routing        GroupRoutingReview `json:"routing"`
	MinReady       int                `json:"min_ready"`
}
