package federation

import (
	"bytes"
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// KubernetesImageProofLoader must select credentials from the committed build
// target. Image and platform arguments come from the live owned pod and node.
type KubernetesImageProofLoader func(context.Context, WorkloadDeployment, string, string, string) ([][]byte, error)

// KubernetesWorkloadEvidence keeps the verified runtime selection and its raw
// image proof together for the issuance journal. It grants no service access.
type KubernetesWorkloadEvidence struct {
	Workload        workloadidentity.WorkloadBinding
	OperatingSystem string
	Architecture    string
	ImageIndexes    [][]byte
}

type KubernetesWorkloadEvidenceVerifier func(context.Context, domain.Destination, WorkloadDeployment, KubernetesWorkloadProfile) (KubernetesWorkloadEvidence, error)

// VerifyKubernetesWorkloadEvidence reads current ownership before requesting
// registry proofs, then checks ownership again before returning evidence.
func VerifyKubernetesWorkloadEvidence(credentials InstallationCredentials, nodes SPIRENodeReader, trustDomain, org string, images KubernetesImageProofLoader) KubernetesWorkloadEvidenceVerifier {
	_, scopeErr := workloadidentity.ControlURIForOrganization(trustDomain, org)
	return func(ctx context.Context, d domain.Destination, binding WorkloadDeployment, p KubernetesWorkloadProfile) (KubernetesWorkloadEvidence, error) {
		if err := ctx.Err(); err != nil {
			return KubernetesWorkloadEvidence{}, err
		}
		if scopeErr != nil || d.OrgID != org || nodes == nil || !workloadProfileMatches(binding, p, d) {
			return KubernetesWorkloadEvidence{}, ErrUnverified
		}
		bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		client, err := openInstallationKubernetes(bounded, credentials, org, d)
		if err != nil {
			return KubernetesWorkloadEvidence{}, err
		}
		r := &kubernetesWorkloadResolver{attestation: &KubernetesAttestationResolver{client: client, nodes: nodes, profile: p.KubernetesAttestationProfile, domain: trustDomain, now: time.Now}, profile: p, binding: binding, images: images}
		return r.ResolveEvidence(bounded, d)
	}
}

func cloneWorkloadIndexes(in [][]byte) [][]byte {
	out := make([][]byte, len(in))
	for i, raw := range in {
		out[i] = bytes.Clone(raw)
	}
	return out
}

func (r *kubernetesWorkloadResolver) ResolveEvidence(ctx context.Context, d domain.Destination) (KubernetesWorkloadEvidence, error) {
	if err := ctx.Err(); err != nil {
		return KubernetesWorkloadEvidence{}, err
	}
	if r == nil || r.attestation == nil || r.attestation.profile != r.profile.KubernetesAttestationProfile || !workloadProfileMatches(r.binding, r.profile, d) {
		return KubernetesWorkloadEvidence{}, ErrUnverified
	}
	// Every request owns its proof buffers; a reusable verifier retains no result.
	local := *r
	local.profile.ImageIndexes = cloneWorkloadIndexes(r.profile.ImageIndexes)
	var out KubernetesWorkloadEvidence
	binding, err := local.attestation.resolve(ctx, d, func(check context.Context, pod *corev1.Pod, node *corev1.Node) error {
		image, verifyErr := local.verifyOwnership(check, pod, node)
		if verifyErr != nil {
			return verifyErr
		}
		out.Workload.ContainerImage = image
		out.OperatingSystem = node.Status.NodeInfo.OperatingSystem
		out.Architecture = node.Status.NodeInfo.Architecture
		out.ImageIndexes = local.profile.ImageIndexes
		return nil
	})
	if err != nil {
		return KubernetesWorkloadEvidence{}, err
	}
	out.Workload.Binding = binding
	out.Workload.Workload = domain.WorkloadSelector{Scope: d.Scope, DestinationID: d.ID, ServiceID: local.binding.ServiceID}
	return out, nil
}
