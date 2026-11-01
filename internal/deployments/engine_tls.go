package deployments

import (
	"context"
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/engineprobe"
	"github.com/xraph/loomplane/internal/enginetls"
	"github.com/xraph/loomplane/internal/recipes"
)

// runtimeCertificate is encrypted alongside rendered files. Its issuer key is
// consumed here and never copied into RuntimeValues or a container.
type runtimeCertificate struct {
	Bundle    []byte             `json:"bundle"`
	Identity  enginetls.Identity `json:"identity"`
	Provider  string             `json:"provider"`
	Namespace string             `json:"namespace,omitempty"`
	Component string             `json:"component"`
}

func runTLSIdentity(run ComponentRun) enginetls.Identity {
	return enginetls.Identity{Org: run.OrgID, Environment: run.EnvironmentID, Resource: run.Resource, Kind: run.Kind}
}

func deferredCertificate(run ComponentRun, file recipes.File) (*runtimeCertificate, error) {
	expected, err := engineprobe.CertificatePath(run.Kind)
	if err != nil || file.Path != expected || run.Component.Readiness == nil || run.Component.Readiness.Checker != run.Kind+"-v1" {
		return nil, errors.New("unsupported deferred engine certificate")
	}
	raw, ok := run.Secrets[file.CertificateSecret]
	if !ok {
		return nil, errors.New("engine certificate bundle is unavailable")
	}
	identity := runTLSIdentity(run)
	if _, err = enginetls.Parse(raw, identity, time.Now()); err != nil {
		return nil, err
	}
	return &runtimeCertificate{Bundle: append([]byte(nil), raw...), Identity: identity, Provider: run.Provider, Namespace: run.TargetSettings["namespace"], Component: run.Component.Name}, nil
}

func (r *runtimeCertificate) render(ctx context.Context, provider, path string, labels map[string]string) ([]byte, error) {
	expected, err := engineprobe.CertificatePath(r.Identity.Kind)
	if err != nil || path != expected || provider != r.Provider || labels[owned.LabelOrg] != r.Identity.Org || labels[owned.LabelEnvironment] != r.Identity.Environment || labels[owned.LabelCatalogResource] != r.Identity.Resource || labels[owned.LabelCatalogKind] != r.Identity.Kind || labels[owned.LabelCatalogComponent] != r.Component || labels[owned.LabelHealthChecker] != r.Identity.Kind+"-v1" {
		return nil, errors.New("deferred engine certificate identity does not match its runtime")
	}
	bundle, err := enginetls.Parse(r.Bundle, r.Identity, time.Now())
	if err != nil {
		return nil, err
	}
	if owned.IsRuntimePreflight(ctx) {
		return nil, nil
	}
	identity, ok := owned.RuntimeIdentityFrom(ctx)
	if !ok || identity.Provider != r.Provider || identity.Instance.String() == "" || labels[owned.LabelInstance] != identity.Instance.String() {
		return nil, errors.New("deferred engine certificate needs trusted runtime identity")
	}
	var host string
	switch r.Provider {
	case "server":
		if r.Namespace != "" || identity.Namespace != "" {
			return nil, errors.New("unexpected server certificate namespace")
		}
		host = server.ContainerName(identity.Instance)
	case "kubernetes":
		if r.Namespace == "" || identity.Namespace != r.Namespace || len(validation.IsDNS1123Label(r.Namespace)) != 0 {
			return nil, errors.New("deferred engine certificate namespace changed")
		}
		host = kubernetes.WorkloadName(identity.Instance) + "." + r.Namespace + ".svc"
	default:
		return nil, errors.New("unsupported engine certificate target")
	}
	return bundle.Certificate(host, time.Now())
}

func publicEngineCA(reads *compositionReads, name string) (string, error) {
	// The public value must come from this run's loaded bundle, not a second
	// mutable Vault read after its files were prepared.
	raw, ok := reads.secrets[name]
	if !ok {
		return "", errors.New("the public CA requires this run's engine TLS bundle")
	}
	d := reads.job.Deployment
	bundle, err := enginetls.Parse(raw, enginetls.Identity{Org: d.OrgID, Environment: d.EnvironmentID, Resource: reads.resource, Kind: reads.kind.Name}, time.Now())
	if err != nil {
		return "", err
	}
	return string(bundle.CACert), nil
}
