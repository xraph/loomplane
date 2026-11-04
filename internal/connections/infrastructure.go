package connections

import (
	"context"
	"fmt"
	"maps"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/registry"
	"github.com/xraph/loomplane/internal/clouds/server"
)

// ServerChecker checks Docker on a server: over pinned SSH, or through a
// joined node's streams for a node connection's marker.
type ServerChecker struct {
	// Streams reach joined nodes. Nil reaches only SSH servers.
	Streams server.NodeStreams
}

// IsGrant implements GrantChecker. A node's marker counts, so nobody can
// paste it to reach another node's Docker.
func (ServerChecker) IsGrant(raw []byte) bool {
	return keylessJSON(raw) || server.IsNodeCredential(raw)
}

// PastedGrant implements pastedGrantAdvice. A server connection's grant is
// only ever a node's marker, which Loomplane makes itself.
func (ServerChecker) PastedGrant() string {
	return "paste an SSH private key, or join the machine as a node instead of pasting a node credential"
}

// serverAccount is what a server connection acts as: the SSH user at the
// host, or the node it reaches.
func serverAccount(cfg *server.Config) string {
	if cfg.Node != nil {
		return "node/" + cfg.Node.NodeID
	}
	return cfg.User + "@" + cfg.Address
}

func (ServerChecker) Parse(raw []byte, settings map[string]string) (Check, error) {
	cfg, err := server.ParseConfig(raw, settings)
	if err != nil {
		return Check{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return Check{Account: serverAccount(cfg), Details: cfg.Details}, nil
}

func (sc ServerChecker) Check(ctx context.Context, raw []byte, settings map[string]string) (Check, error) {
	cfg, err := server.ParseConfig(raw, settings)
	if err != nil {
		return Check{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	c, err := server.Connect(ctx, cfg, sc.Streams)
	if err != nil {
		return Check{}, err
	}
	defer c.Close()
	details, missing, err := c.Check(ctx)
	if err == nil {
		// GPUs reports a failed read as status unknown, so it never fails the check.
		if inv, gpuErr := c.GPUs(ctx); gpuErr == nil {
			if details == nil {
				details = map[string]string{}
			}
			maps.Copy(details, inv.Details())
		}
	}
	return Check{Account: serverAccount(cfg), Details: details, Missing: missing}, err
}

type KubernetesChecker struct{}

// IsGrant implements GrantChecker. The in-cluster credential counts, so
// nobody can paste it to borrow Loomplane's own service account.
func (KubernetesChecker) IsGrant(raw []byte) bool {
	return keylessJSON(raw) || kubernetes.IsInClusterCredential(raw)
}

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
