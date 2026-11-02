package planning

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/connections"
	domain "github.com/xraph/loomplane/pkg/federation"
)

var ErrPrerequisiteObservation = errors.New("placement prerequisite observation is unavailable")
var ErrPrerequisiteAccessDenied = errors.New("the attached namespace denies prerequisite reads")

// MemberPrerequisiteObservation contains public certificate facts only.
type MemberPrerequisiteObservation struct {
	Namespace       string    `json:"namespace"`
	NamespaceUID    string    `json:"namespace_uid"`
	SecretName      string    `json:"secret_name"`
	SecretUID       string    `json:"secret_uid"`
	ResourceVersion string    `json:"resource_version"`
	Hostnames       []string  `json:"hostnames"`
	Fingerprint     string    `json:"fingerprint"`
	NotBefore       time.Time `json:"not_before"`
	NotAfter        time.Time `json:"not_after"`
	CheckedAt       time.Time `json:"checked_at"`
}

func (o MemberPrerequisiteObservation) VerifyHostname(host string, now time.Time) error {
	if o.CheckedAt.IsZero() || o.CheckedAt.After(now) || !o.CheckedAt.Add(time.Minute).After(now) || o.NotBefore.After(now) || o.NotAfter.Before(now.Add(24*time.Hour)) {
		return ErrPrerequisiteObservation
	}
	certificate := x509.Certificate{DNSNames: o.Hostnames}
	if certificate.VerifyHostname(host) != nil {
		return ErrPrerequisiteObservation
	}
	return nil
}

type MemberPrerequisiteObserver interface {
	Observe(context.Context, *cloudinfra.RuntimeAttachment, string) (MemberPrerequisiteObservation, error)
}
type ObservationCredentials interface {
	ObservationCredential(context.Context, string, string) (*connections.Connection, []byte, error)
}

// KubernetesPrerequisiteObserver uses the installed non-renewing credential path.
// Its client is fenced against persisted Kubernetes writes.
type KubernetesPrerequisiteObserver struct {
	Credentials ObservationCredentials
	Now         func() time.Time
}

func (o *KubernetesPrerequisiteObserver) Observe(ctx context.Context, a *cloudinfra.RuntimeAttachment, name string) (MemberPrerequisiteObservation, error) {
	var empty MemberPrerequisiteObservation
	if o.Credentials == nil || a == nil || a.DeletedAt != nil || a.RetirementOperationID != "" || name == "" {
		return empty, ErrPrerequisiteObservation
	}
	c, raw, err := o.Credentials.ObservationCredential(ctx, a.OrgID, a.RuntimeConnectionID)
	if err != nil {
		if errors.Is(err, connections.ErrCredentialRefreshNeeded) {
			return empty, connections.ErrCredentialRefreshNeeded
		}
		return empty, ErrPrerequisiteObservation
	}
	if c.OrgID != a.OrgID || c.ID != a.RuntimeConnectionID || c.Cloud != connections.Kubernetes || c.Method != connections.MethodManagedKubernetes || c.Generation != a.TrustRevision || c.Details["managed_attachment_id"] != a.ID || c.Details["managed_project_id"] != a.ProjectID || c.Details["managed_resource_id"] != a.ClusterID || c.Details["namespace"] != a.Namespace || c.Details["namespace_uid"] != a.NamespaceUID || c.Details["cluster_ca"] != a.TrustFingerprint {
		return empty, ErrPrerequisiteObservation
	}
	cfg, err := kubernetes.ParseConfig(raw, c.Details)
	if err != nil {
		return empty, ErrPrerequisiteObservation
	}
	endpoint := sha256.Sum256([]byte(cfg.REST.Host))
	if cfg.Namespace != a.Namespace || cfg.Details["cluster_ca"] != a.TrustFingerprint || hex.EncodeToString(endpoint[:]) != a.EndpointFingerprint {
		return empty, ErrPrerequisiteObservation
	}
	cfg.ManagedWorkload = true
	cfg.ManagedProfile = kubernetes.ManagedProfileJournal
	client, err := kubernetes.New(cfg)
	if err != nil {
		return empty, ErrPrerequisiteObservation
	}
	if err = kubernetes.VerifyRuntimeIdentity(ctx, client.Kube, a.Namespace, a.NamespaceUID, a.ServiceAccountName, a.ServiceAccountUID); err != nil {
		if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
			return empty, ErrPrerequisiteAccessDenied
		}
		return empty, ErrPrerequisiteObservation
	}
	secret, err := client.Kube.CoreV1().Secrets(a.Namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		return empty, ErrPrerequisiteAccessDenied
	}
	if err != nil || secret == nil || secret.DeletionTimestamp != nil || secret.Namespace != a.Namespace || secret.Name != name || secret.UID == "" || secret.Type != corev1.SecretTypeTLS {
		return empty, ErrPrerequisiteObservation
	}
	cert, key := secret.Data[corev1.TLSCertKey], secret.Data[corev1.TLSPrivateKeyKey]
	if len(cert) == 0 || len(cert) > 256*1024 || len(key) == 0 || len(key) > 64*1024 {
		return empty, ErrPrerequisiteObservation
	}
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil || len(pair.Certificate) == 0 {
		return empty, ErrPrerequisiteObservation
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || len(leaf.DNSNames) == 0 || len(leaf.DNSNames) > 100 {
		return empty, ErrPrerequisiteObservation
	}
	sum := sha256.Sum256(leaf.Raw)
	names := slices.Clone(leaf.DNSNames)
	slices.Sort(names)
	names = slices.Compact(names)
	now := time.Now().UTC()
	if o.Now != nil {
		now = o.Now().UTC()
	}
	if leaf.NotBefore.After(now) || !leaf.NotAfter.After(now) {
		return empty, domain.ErrGroupProfile
	}
	return MemberPrerequisiteObservation{Namespace: a.Namespace, NamespaceUID: a.NamespaceUID, SecretName: name, SecretUID: string(secret.UID), ResourceVersion: secret.ResourceVersion, Hostnames: names, Fingerprint: hex.EncodeToString(sum[:]), NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter, CheckedAt: now}, nil
}
