package deployments

import (
	"context"
	"errors"
	"reflect"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	nativepg "github.com/xraph/loomplane/internal/clouds/postgres"
	nativecache "github.com/xraph/loomplane/internal/clouds/redis"
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
		client.Config.Details = result.Details
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
