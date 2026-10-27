package connections

import (
	"context"
	"fmt"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/registry"
	"github.com/xraph/loomplane/internal/clouds/server"
)

type ServerChecker struct{}

// IsGrant implements GrantChecker.
func (ServerChecker) IsGrant(raw []byte) bool { return keylessJSON(raw) }

func (ServerChecker) Parse(raw []byte, settings map[string]string) (Check, error) {
	cfg, err := server.ParseConfig(raw, settings)
	if err != nil {
		return Check{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return Check{Account: cfg.User + "@" + cfg.Address, Details: cfg.Details}, nil
}

func (ServerChecker) Check(ctx context.Context, raw []byte, settings map[string]string) (Check, error) {
	cfg, err := server.ParseConfig(raw, settings)
	if err != nil {
		return Check{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	c, err := server.Dial(ctx, cfg)
	if err != nil {
		return Check{}, err
	}
	defer c.Close()
	details, missing, err := c.Check(ctx)
	return Check{Account: cfg.User + "@" + cfg.Address, Details: details, Missing: missing}, err
}

type KubernetesChecker struct{}

// IsGrant implements GrantChecker.
func (KubernetesChecker) IsGrant(raw []byte) bool { return keylessJSON(raw) }

func (KubernetesChecker) Parse(raw []byte, settings map[string]string) (Check, error) {
	cfg, err := kubernetes.ParseConfig(raw, settings)
	if err != nil {
		return Check{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return Check{Account: cfg.Context + "/" + cfg.Namespace, Details: cfg.Details}, nil
}

func (KubernetesChecker) Check(ctx context.Context, raw []byte, settings map[string]string) (Check, error) {
	cfg, err := kubernetes.ParseConfig(raw, settings)
	if err != nil {
		return Check{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	c, err := kubernetes.New(cfg)
	if err != nil {
		return Check{}, err
	}
	result, err := c.Check(ctx)
	if err != nil {
		return Check{}, err
	}
	if checkErr := c.CheckPullSecret(ctx); checkErr != nil {
		return Check{}, checkErr
	}
	return Check{Account: cfg.Context + "/" + cfg.Namespace, Details: result.Details, Missing: result.Missing}, nil
}

type RegistryChecker struct{}

// IsGrant implements GrantChecker.
func (RegistryChecker) IsGrant(raw []byte) bool { return keylessJSON(raw) }

func (RegistryChecker) Parse(raw []byte, settings map[string]string) (Check, error) {
	cfg, err := registry.ParseConfig(raw, settings)
	if err != nil {
		return Check{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return Check{Account: cfg.Endpoint + "/" + cfg.Repository, Details: cfg.Details}, nil
}

func (RegistryChecker) Check(ctx context.Context, raw []byte, settings map[string]string) (Check, error) {
	cfg, err := registry.ParseConfig(raw, settings)
	if err != nil {
		return Check{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	result, err := registry.New(cfg).Check(ctx)
	if err != nil {
		return Check{}, err
	}
	return Check{Account: cfg.Endpoint + "/" + cfg.Repository, Details: result.Details, Missing: result.Missing}, nil
}
