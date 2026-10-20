package federation

import "regexp"

var identitySegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// ValidIdentitySegment rejects separators and encoding in scoped runtime IDs.
func ValidIdentitySegment(value string) bool { return identitySegment.MatchString(value) }

// Valid checks every tenant boundary used by the runtime protocol.
func (s Scope) Valid() bool {
	return ValidIdentitySegment(s.OrgID) && ValidIdentitySegment(s.ProjectID) && ValidIdentitySegment(s.EnvironmentID)
}
