package fleets

import (
	"context"
	"math"
	"time"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/pkg/fleet"
)

// ScalingWriterBinding pins the authority and native resources for one transfer.
// A replacement resource or refreshed authority requires a new reviewed operation.
type ScalingWriterBinding struct {
	AuthorityID        string `json:"authority_id"`
	AuthorityRevision  int64  `json:"authority_revision"`
	ConnectionID       string `json:"connection_id"`
	AttachmentID       string `json:"attachment_id"`
	AttachmentRevision int64  `json:"attachment_revision"`
	TrustRevision      int64  `json:"trust_revision"`
	Namespace          string `json:"namespace"`
	NamespaceUID       string `json:"namespace_uid"`
	Deployment         string `json:"deployment"`
	DeploymentUID      string `json:"deployment_uid"`
	RuntimeRef         string `json:"runtime_ref"`
	RuntimeRevision    int64  `json:"runtime_revision"`
	OldWriter          string `json:"old_writer"`
	OldWriterUID       string `json:"old_writer_uid"`
}

// ScalingWriterEvent references proof in a trusted receipt store. ProofKind is
// checked for ordering here; only the verifier can establish what happened.
type ScalingWriterEvent struct {
	Sequence                  int64                `json:"sequence"`
	Phase                     string               `json:"phase"`
	ProofKind                 string               `json:"proof_kind"`
	ProofID                   string               `json:"proof_id"`
	Binding                   ScalingWriterBinding `json:"binding"`
	DeploymentGeneration      int64                `json:"deployment_generation"`
	DeploymentResourceVersion string               `json:"deployment_resource_version"`
	WriterUID                 string               `json:"writer_uid,omitempty"`
	ObservedAt                time.Time            `json:"observed_at"`
	ExpiresAt                 time.Time            `json:"expires_at"`
}

// ScalingWriterVerifier must check the original principal, immutable operation
// authority, live allocation and current generation/epoch in the same transaction.
// It resolves ProofID from trusted storage and checks its exact policy and native
// identities. Fencing proof must cover every competing writer, including old
// controller recreation authority; a missing HPA alone is insufficient. Active
// proof must confirm the desired policy and writer UID at the resource version.
// Automatic policy requires complete-sample and application qualification too.
// Implementations read persisted receipts here; they must not call a provider
// while holding database locks. There is no installed verifier in this slice.
type ScalingWriterVerifier interface {
	VerifyScalingWriterTransition(context.Context, driver.Tx, ScalingPolicyRecord, ScalingWriterEvent) error
}

// ObservedWriter reports only the last established writer state. Applying may
// have an ambiguous outcome, so it remains unknown until native observation.
func (e ScalingWriterEvent) ObservedWriter(p fleet.ScalingPolicy) string {
	switch e.Phase {
	case "quiesced":
		return "none"
	case "active":
		if p.Mode == "automatic" {
			return "hpa"
		}
		return "manual"
	default:
		return "unknown"
	}
}

func validateScalingWriterTransition(policy ScalingPolicyRecord, previous *ScalingWriterEvent, next ScalingWriterEvent, now time.Time) error {
	if err := policy.Input.Validate(); err != nil {
		return err
	}
	b := next.Binding
	for _, value := range []string{b.AuthorityID, b.ConnectionID, b.AttachmentID, b.Namespace, b.NamespaceUID, b.Deployment, b.DeploymentUID, b.RuntimeRef, b.OldWriterUID, next.ProofID, next.DeploymentResourceVersion} {
		if value == "" || len(value) > 512 {
			return ErrInvalid
		}
	}
	if b.AuthorityRevision < 1 || b.AttachmentRevision < 1 || b.TrustRevision < 1 || b.RuntimeRevision < 1 || (b.OldWriter != "manual" && b.OldWriter != "hpa") || next.DeploymentGeneration < 1 || next.Sequence < 1 || now.IsZero() {
		return ErrInvalid
	}
	if next.ObservedAt.IsZero() || next.ObservedAt.After(now) || now.Sub(next.ObservedAt) > time.Minute || !next.ExpiresAt.After(now) || !next.ExpiresAt.After(next.ObservedAt) {
		return ErrConflict
	}
	if next.Phase != "active" && next.WriterUID != "" {
		return ErrInvalid
	}
	if previous == nil {
		if next.Sequence != 1 || next.Phase != "revoking" || next.ProofKind != "revoke_authorized" {
			return ErrConflict
		}
		return nil
	}
	if previous.Sequence == math.MaxInt64 || next.Sequence != previous.Sequence+1 || next.Binding != previous.Binding || next.ObservedAt.Before(previous.ObservedAt) || next.DeploymentGeneration < previous.DeploymentGeneration || next.ProofID == previous.ProofID {
		return ErrConflict
	}
	if next.Phase == previous.Phase && (next.Phase == "revoking" || next.Phase == "applying") && next.ProofKind == "effect_unknown" {
		return nil
	}
	switch previous.Phase {
	case "revoking":
		if next.Phase == "quiesced" && next.ProofKind == "writers_fenced" {
			return nil
		}
	case "quiesced":
		if next.Phase == "applying" && next.ProofKind == "apply_authorized" {
			return nil
		}
	case "applying":
		if next.Phase == "active" && next.ProofKind == "writer_observed" && next.WriterUID != "" && len(next.WriterUID) <= 512 {
			return nil
		}
	}
	return ErrConflict
}
