package workloadidentity

import (
	"context"
	"strings"

	agentv1 "github.com/spiffe/spire-api-sdk/proto/spire/api/server/agent/v1"
	spiretypes "github.com/spiffe/spire-api-sdk/proto/spire/api/types"
	"google.golang.org/grpc"

	domain "github.com/xraph/loomplane/pkg/federation"
)

// NodeReader supplies live parent evidence for the Kubernetes installation verifier.
type NodeReader interface {
	GetAgent(context.Context, *agentv1.GetAgentRequest, ...grpc.CallOption) (*spiretypes.Agent, error)
}

func (a *Admin) GetAgent(ctx context.Context, request *agentv1.GetAgentRequest, _ ...grpc.CallOption) (*spiretypes.Agent, error) {
	if request == nil || request.Id == nil || request.Id.TrustDomain != a.domain {
		return nil, ErrIdentity
	}
	parts := strings.Split(request.Id.Path, "/")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "spire" || parts[2] != "agent" || parts[3] != "k8s_psat" || !domain.ValidIdentitySegment(parts[4]) || !domain.ValidIdentitySegment(parts[5]) {
		return nil, ErrIdentity
	}
	ctx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	if err := a.Health(ctx); err != nil {
		return nil, err
	}
	id := &spiretypes.SPIFFEID{TrustDomain: a.domain, Path: request.Id.Path}
	agent, err := agentv1.NewAgentClient(a.connection).GetAgent(ctx, &agentv1.GetAgentRequest{Id: id})
	if err != nil {
		return nil, err
	}
	if agent == nil || agent.Id == nil || agent.Id.TrustDomain != id.TrustDomain || agent.Id.Path != id.Path {
		return nil, ErrIdentity
	}
	return agent, nil
}

// Register, Revoke and GetAgent share the lifecycle-owned administrative client.
// No caller can retain usable authority before startup or after shutdown.
func (r *CleanupRuntime) Register(ctx context.Context, binding Binding) (*spiretypes.Entry, error) {
	admin, err := r.authority()
	if err != nil {
		return nil, err
	}
	return admin.Register(ctx, binding)
}
func (r *CleanupRuntime) Revoke(ctx context.Context, id string) error {
	admin, err := r.authority()
	if err != nil {
		return err
	}
	return admin.Revoke(ctx, id)
}
func (r *CleanupRuntime) GetAgent(ctx context.Context, request *agentv1.GetAgentRequest, opts ...grpc.CallOption) (*spiretypes.Agent, error) {
	admin, err := r.authority()
	if err != nil {
		return nil, err
	}
	return admin.GetAgent(ctx, request, opts...)
}
func (r *CleanupRuntime) authority() (cleanupAdmin, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.running || r.admin == nil {
		return nil, ErrIdentity
	}
	return r.admin, nil
}
