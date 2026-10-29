package connections

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
)

// A preparer runs only on explicit connection saves, never during health checks.
type connectionPreparer interface {
	Prepare(context.Context, []byte, map[string]string) (map[string]string, error)
}

func prepareConnection(ctx context.Context, checker Checker, raw []byte, settings map[string]string) (map[string]string, error) {
	preparer, ok := checker.(connectionPreparer)
	if !ok {
		return settings, nil
	}
	if _, err := checker.Parse(raw, settings); err != nil {
		return nil, err
	}
	prepared, err := preparer.Prepare(ctx, raw, settings)
	if err != nil {
		if apierrors.IsUnauthorized(err) || rejected(err) {
			return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		return nil, fmt.Errorf("%w: prepare connection: %w", ErrUnavailable, err)
	}
	return prepared, nil
}

// Prepare installs prerequisites requested by an explicit connection save.
func (KubernetesChecker) Prepare(ctx context.Context, raw []byte, settings map[string]string) (map[string]string, error) {
	cfg, err := kubernetes.ParseConfig(raw, settings)
	if err != nil {
		return nil, err
	}
	if cfg.Details["registry_hostname"] == "" || cfg.Details["registry_setup"] == "existing" {
		return settings, nil
	}
	client, err := kubernetes.New(cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if setupErr := client.BootstrapManagedBuild(ctx); setupErr != nil {
		return nil, setupErr
	}
	return client.Config.Details, nil
}

// PreparationIdentity verifies the destination before a replacement can write
// setup resources. Its storage or controllers may still need repair.
func (KubernetesChecker) PreparationIdentity(ctx context.Context, raw []byte, settings map[string]string) (Check, error) {
	cfg, err := kubernetes.ParseConfig(raw, settings)
	if err != nil {
		return Check{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	client, err := kubernetes.New(cfg)
	if err != nil {
		return Check{}, err
	}
	namespace, err := client.Kube.CoreV1().Namespaces().Get(ctx, cfg.Namespace, metav1.GetOptions{})
	if apierrors.IsUnauthorized(err) {
		return Check{}, fmt.Errorf("%w: %w", ErrInvalid, kubernetes.ErrRejected)
	}
	if err != nil {
		return Check{}, fmt.Errorf("%w: verify namespace: %w", ErrUnavailable, err)
	}
	cfg.Details["namespace_uid"] = string(namespace.UID)
	return Check{Account: cfg.Context + "/" + cfg.Namespace, Details: cfg.Details}, nil
}

func (s *Service) checkPreparationIdentity(ctx context.Context, cloud string, checker Checker, raw []byte, settings map[string]string) (Check, error) {
	if identity, ok := checker.(interface {
		PreparationIdentity(context.Context, []byte, map[string]string) (Check, error)
	}); ok {
		return identity.PreparationIdentity(ctx, raw, settings)
	}
	return s.check(ctx, cloud, checker, raw, settings)
}
