package kubernetes

import "strings"

func hasGroupScope(s ManagedScope) bool {
	return s.GroupID != "" || s.MemberID != "" || s.GroupGeneration != 0 || s.BackendGeneration != 0 || s.GroupOperation != ""
}

func hasLifecycleScope(s ManagedScope) bool {
	return s.LifecycleRunID != "" || s.LifecycleJobName != "" || s.LifecycleDigest != "" || s.LifecycleSpecDigest != "" || s.LifecycleSecretDigest != ""
}

// legacyManagedScopeValid accepts only the original service execution profile.
// Optional identity fields cannot widen its mutation authority.
func legacyManagedScopeValid(s ManagedScope) bool {
	return !hasGroupScope(s) && !hasLifecycleScope(s) && s.Epoch > 0 && s.Release != "" && s.Release == WorkloadReleaseID(s.RuntimeRef, s.RuntimeRevision) &&
		s.Org != "" && s.Project != "" && s.Environment != "" && s.Service != "" && s.Plan != "" && s.Deployment != "" && s.Action != "" && s.Connection != "" && s.Instance != "" && s.Namespace != "" && s.NamespaceUID != "" && len(s.ClusterCA) == 64 &&
		strings.HasPrefix(s.RuntimeRef, s.Org+"/"+s.Environment+"/"+s.Service+"/runtime_") &&
		(s.RuntimeRevision == s.Deployment+"/"+s.Action || strings.HasPrefix(s.RuntimeRevision, s.Deployment+"/"+s.Action+"/"))
}
