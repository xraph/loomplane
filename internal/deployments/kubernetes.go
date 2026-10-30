package deployments

import (
	"context"
	"errors"
	"reflect"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	nativepg "github.com/xraph/loomplane/internal/clouds/postgres"
	nativecache "github.com/xraph/loomplane/internal/clouds/redis"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

func (e *OwnedTarget) kubernetesSession(org, connection string) kubernetes.Session {
	return func(ctx context.Context, labels map[string]string) (*kubernetes.Client, error) {
		c, raw, err := e.connection(ctx, org, connection, labels)
		if err != nil {
			return nil, err
		}
		cfg, err := kubernetes.ParseConfig(raw, c.Details)
		if err != nil {
			return nil, err
		}
		// Namespace identity comes from the API; trust and endpoint come from the raw kubeconfig.
		for key, value := range planning.ReviewedConnectionSettings(e.Cloud, cfg.Details) {
			if c.Details[key] != value {
				return nil, errors.New("cluster trust settings changed since review; plan again")
			}
		}
		client, err := kubernetes.New(cfg)
		if err != nil {
			return nil, err
		}
		result, err := client.Check(ctx)
		if err != nil {
			return nil, err
		}
		// Attachment identity comes from validated credential storage, not the
		// Kubernetes API. Keep it alongside the freshly checked cluster facts.
		if c.Method == connections.MethodManagedKubernetes {
			for _, key := range []string{"managed_attachment_id", "managed_project_id", "managed_resource_id"} {
				result.Details[key] = c.Details[key]
			}
		}
		if len(result.Missing) > 0 || !reflect.DeepEqual(planning.ReviewedConnectionSettings(e.Cloud, result.Details), planning.ReviewedConnectionSettings(e.Cloud, c.Details)) {
			return nil, errors.New("cluster identity or namespace permissions changed; check the connection and plan again")
		}
		// Planning checks these when you review a database or a cache. A cluster
		// admin can revoke them afterwards, so every session against one checks again.
		if (labels[nativepg.LabelDatabase] != "" || labels[nativecache.LabelEngine] != "") && result.Details["postgres_recovery_permissions"] != "ready" {
			return nil, errors.New("this database needs pod exec, network-policy and temporary-volume cleanup permissions; check the connection and plan again")
		}
		operation := labels[owned.LabelOperation]
		activate := operation == "" || operation == "apply" || operation == "start"
		if activate {
			if err := client.CheckPullSecret(ctx); err != nil {
				return nil, err
			}
		}
		if activate && labels[owned.LabelPublic] == "true" && result.Details["ingress"] != "ready" {
			return nil, errors.New("this workload needs namespace ingress permissions")
		}
		if activate && labels[owned.LabelScaleMin] != labels[owned.LabelScaleMax] && result.Details["hpa"] != "ready" {
			return nil, errors.New("this workload needs HPA permissions and the metrics API")
		}
		if activate && labels[owned.LabelKubernetesRules] != "" && result.Details["rbac"] != "ready" {
			return nil, errors.New("this component needs a Role to discover Services; let Loomplane create serviceaccounts, roles and rolebindings in the namespace, and get, list and watch services, endpoints and endpointslices, then check the connection and plan again")
		}
		client.Config.Details = result.Details
		// Credential resolution verifies the persisted attachment before this session.
		client.Config.ManagedWorkload = c.Method == connections.MethodManagedKubernetes && c.Details["managed_attachment_id"] != ""
		if client.Config.ManagedWorkload {
			journal, ok := e.Controllers.(*kubernetes.ControllerStore)
			if !ok {
				return nil, kubernetes.ErrManagedAuthority
			}
			client.Config.ManagedJournal = journal
			client.Config.ManagedProfile = kubernetes.ManagedProfileJournal
			// Incomplete preflight identities get a read-only fenced client.
			if labels[owned.LabelRuntime] != "" && labels[owned.LabelInstance] != "" {
				identity := kubernetes.ManagedRuntimeIdentity{Org: org, Project: labels[owned.LabelProject], Environment: labels[owned.LabelEnvironment], Service: labels[owned.LabelService], Plan: labels[owned.LabelPlan], Connection: connection, Instance: labels[owned.LabelInstance], RuntimeRef: labels[owned.LabelRuntime], RuntimeRevision: labels[owned.LabelRevision], Namespace: client.Config.Namespace, NamespaceUID: result.Details["namespace_uid"], ClusterCA: result.Details["cluster_ca"]}
				profile, profileErr := journal.ManagedProfileFor(ctx, identity)
				if profileErr != nil {
					return nil, profileErr
				}
				if profile == kubernetes.ManagedProfileLegacy {
					if operation != "read" {
						if checkErr := journal.CheckLegacyManagedWrite(ctx, identity); checkErr != nil {
							return nil, checkErr
						}
					}
					client.Config.ManagedProfile = profile
					client.Config.ManagedIdentity = identity
					client.Config.ManagedLegacyGuard = journal
				} else if operation == "apply" {
					if labels[owned.LabelStateful] == "true" || labels[owned.LabelCatalogComponent] != "" {
						return nil, kubernetes.ErrManagedAuthority
					}
					attempt, exists := cloudinfra.WorkloadAttemptFrom(ctx)
					if !exists {
						return nil, kubernetes.ErrManagedAuthority
					}
					scope := kubernetes.ManagedScope{Org: org, Project: identity.Project, Environment: identity.Environment, Service: identity.Service, Plan: identity.Plan, Deployment: attempt.Deployment, Action: labels[owned.LabelAction], Connection: connection, Instance: identity.Instance, RuntimeRef: identity.RuntimeRef, RuntimeRevision: identity.RuntimeRevision, Namespace: client.Config.Namespace, NamespaceUID: result.Details["namespace_uid"], ClusterCA: result.Details["cluster_ca"], Epoch: attempt.Generation}
					scope.Release = kubernetes.WorkloadReleaseID(scope.RuntimeRef, scope.RuntimeRevision)
					if scope.Org != attempt.Org || scope.Project != attempt.Project || scope.Environment != attempt.Environment || scope.Service != attempt.Service || scope.Plan != attempt.Plan {
						return nil, kubernetes.ErrManagedAuthority
					}
					if checkErr := journal.OpenManagedScope(ctx, scope); checkErr != nil {
						return nil, checkErr
					}
					client.Config.ManagedScope = scope
				}
			}
			return kubernetes.New(client.Config)
		}
		return client, nil
	}
}
func (e *OwnedTarget) kubernetesBackend(org, connection string) owned.Backend {
	runtime := &kubernetes.Runtime{Session: e.kubernetesSession(org, connection), Values: e.values}
	if e.Controllers != nil {
		runtime.RecordController = e.Controllers.RecordController
	}
	return runtime
}
