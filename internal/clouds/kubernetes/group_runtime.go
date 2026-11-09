package kubernetes

import (
	"context"
	"encoding/hex"

	domain "github.com/xraph/loomplane/pkg/federation"
)

// GroupRuntimeJournal admits only an accepted member attempt with current
// authority and a retained capacity hold. Admission does not replace Issue's
// before-effect checks.
type GroupRuntimeJournal interface {
	ManagedJournal
	OpenGroupScope(context.Context, ManagedScope) error
}

// NewGroupRuntimeClient installs private ingress ownership after checking the
// exact saved member scope. User connection settings cannot select this mode.
func NewGroupRuntimeClient(ctx context.Context, cfg *Config, scope ManagedScope, journal GroupRuntimeJournal) (*Client, error) {
	if cfg == nil || journal == nil || !groupManagedScopeValid(scope) || cfg.Namespace != scope.Namespace || cfg.Details["namespace_uid"] != scope.NamespaceUID || cfg.Details["cluster_ca"] != scope.ClusterCA {
		return nil, ErrManagedAuthority
	}
	if err := journal.OpenGroupScope(ctx, scope); err != nil {
		return nil, err
	}
	copyConfig := *cfg
	copyConfig.groupIngress = true
	copyConfig.ManagedWorkload = true
	copyConfig.ManagedProfile = ManagedProfileJournal
	copyConfig.ManagedScope = scope
	copyConfig.ManagedJournal = journal
	return New(&copyConfig)
}

func groupManagedScopeValid(s ManagedScope) bool {
	if s.LifecycleRunID != "" || s.LifecycleJobName != "" || s.LifecycleDigest != "" || s.LifecycleSpecDigest != "" || s.LifecycleSecretDigest != "" {
		return false
	}
	identity, err := domain.NewMemberIdentity(s.GroupID, s.BackendGeneration, s.MemberID)
	ca, caErr := hex.DecodeString(s.ClusterCA)
	return err == nil && caErr == nil && len(ca) == 32 && (s.GroupOperation == "prepare" && s.BackendGeneration == s.GroupGeneration || s.GroupOperation == "promote" && s.BackendGeneration <= s.GroupGeneration) && s.RuntimeRef == identity.RuntimeRef && s.Instance == identity.InstanceID && s.Release == WorkloadReleaseID(s.RuntimeRef, s.RuntimeRevision) && s.Release != "" && s.RuntimeRevision != "" && s.Epoch > 0 && s.Org != "" && s.Project != "" && s.Environment != "" && s.Service != "" && s.Plan != "" && s.Connection != "" && s.Namespace != "" && s.NamespaceUID != "" && s.Action == "" && s.Deployment == ""
}

func ordinaryIngress(c *Client) bool { return !c.Config.groupIngress }
