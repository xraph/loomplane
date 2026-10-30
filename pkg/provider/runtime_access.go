package provider

// RuntimeAccessProfile selects additional namespaced runtime permissions.
// An omitted profile retains the original enrollment permissions.
type RuntimeAccessProfile struct {
	SchemaVersion     int  `json:"schema_version"`
	CleanupDependents bool `json:"cleanup_dependents"`
	HPA               bool `json:"hpa"`
}

func (p RuntimeAccessProfile) Valid() bool {
	return p.SchemaVersion == 1 && p.CleanupDependents
}

func (p RuntimeAccessProfile) Includes(old RuntimeAccessProfile) bool {
	return p.Valid() && (!old.CleanupDependents || p.CleanupDependents) && (!old.HPA || p.HPA)
}

// RuntimeAccessInput pins the identities and configuration shown in the review.
// Preview derives these pins from the saved attachment and authenticated reads.
type RuntimeAccessInput struct {
	AttachmentID                 string               `json:"attachment_id"`
	AttachmentRevision           int64                `json:"attachment_revision"`
	CapabilityRevision           int64                `json:"capability_revision"`
	ProjectID                    string               `json:"project_id"`
	GrantRevision                int64                `json:"grant_revision"`
	Namespace                    string               `json:"namespace"`
	NamespaceUID                 string               `json:"namespace_uid"`
	ServiceAccountName           string               `json:"service_account_name"`
	ServiceAccountUID            string               `json:"service_account_uid"`
	RoleName                     string               `json:"role_name"`
	RoleUID                      string               `json:"role_uid"`
	RoleResourceVersion          string               `json:"role_resource_version"`
	RoleRulesDigest              string               `json:"role_rules_digest"`
	RoleBindingName              string               `json:"role_binding_name"`
	RoleBindingUID               string               `json:"role_binding_uid"`
	RoleBindingResourceVersion   string               `json:"role_binding_resource_version"`
	RoleBindingSubjectsRefDigest string               `json:"role_binding_subjects_ref_digest"`
	Profile                      RuntimeAccessProfile `json:"profile"`
}
