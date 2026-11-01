package kubernetes

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/security"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const ReplicaProofKeyLabel = "loomplane/kubernetes/accepted-replica-spec/v1"

// ReplicaProofKey returns no evidence key when the instance uses the public
// development secret or has no configured 32-byte secret.
func ReplicaProofKey(secret string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(secret)
	if err != nil || len(raw) != 32 || base64.StdEncoding.EncodeToString(raw) == config.Defaults().SecretKey {
		return nil, nil
	}
	return security.DeriveKey(secret, ReplicaProofKeyLabel)
}

func (s *ControllerStore) WithReplicaProof(key []byte) *ControllerStore {
	clone := *s
	clone.replicaProofKey = bytes.Clone(key)
	return &clone
}

type replicaMACKey struct{}

func replicaSpecMAC(key []byte, scope ManagedScope, resource, uid string, body []byte) []byte {
	if len(key) != 32 || uid == "" || resource != "deployments" && resource != "horizontalpodautoscalers" {
		return nil
	}
	var obj struct {
		Metadata struct {
			Labels          map[string]string
			OwnerReferences []metav1.OwnerReference
		}
		Spec map[string]any
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&obj) != nil || len(obj.Spec) == 0 {
		return nil
	}
	if resource == "deployments" {
		delete(obj.Spec, "replicas")
	}
	mac := hmac.New(sha256.New, key)
	if json.NewEncoder(mac).Encode([]any{ReplicaProofKeyLabel, scope, resource, uid, obj}) != nil {
		return nil
	}
	return mac.Sum(nil)
}

func (s *ControllerStore) resolveApplicationResponse(ctx context.Context, id string, scope ManagedScope, req ManagedRequest, out ManagedOutcome, body []byte) error {
	if out.State == "accepted" && (req.Method == "PUT" || req.Method == "POST") {
		if sig := replicaSpecMAC(s.replicaProofKey, scope, req.Resource, out.UID, body); sig != nil {
			ctx = context.WithValue(ctx, replicaMACKey{}, sig)
		}
	}
	return s.Resolve(ctx, id, scope, out)
}

func corroboratedScale(d *appsv1.Deployment, h *autoscalingv2.HorizontalPodAutoscaler) bool {
	if d == nil || h == nil || d.UID == "" || h.UID == "" || d.DeletionTimestamp != nil || h.DeletionTimestamp != nil || d.Namespace != h.Namespace || d.Name != h.Name || d.Spec.Replicas == nil || d.Status.ObservedGeneration != d.Generation || h.Spec.MinReplicas == nil || h.Status.LastScaleTime == nil || h.Spec.ScaleTargetRef.APIVersion != "apps/v1" || h.Spec.ScaleTargetRef.Kind != "Deployment" || h.Spec.ScaleTargetRef.Name != d.Name {
		return false
	}
	count := *d.Spec.Replicas
	if count < *h.Spec.MinReplicas || count > h.Spec.MaxReplicas || count != h.Status.DesiredReplicas {
		return false
	}
	// Managed fields corroborate the last replicas writer. They do not attest
	// the API user's identity or reconstruct intermediate object history.
	owned := false
	for _, m := range d.ManagedFields {
		if m.Subresource == "status" || m.FieldsV1 == nil {
			continue
		}
		var fields map[string]map[string]json.RawMessage
		if json.Unmarshal(m.FieldsV1.GetRawBytes(), &fields) != nil {
			return false
		}
		if fields["f:spec"]["f:replicas"] != nil {
			if owned || m.Subresource != "scale" || m.Operation != metav1.ManagedFieldsOperationUpdate || m.Manager != "kube-controller-manager" && m.Manager != "horizontal-pod-autoscaler" {
				return false
			}
			owned = true
		}
	}
	return owned
}

// ReplicaProgression compares live objects with the two accepted API responses.
// It returns the original Deployment generation. It never changes a receipt.
func (s *ControllerStore) ReplicaProgression(ctx context.Context, client kubeclient.Interface, org, namespaceUID, ca string, d *appsv1.Deployment, h *autoscalingv2.HorizontalPodAutoscaler) (int64, error) {
	if s == nil || client == nil || len(s.replicaProofKey) != 32 || !corroboratedScale(d, h) {
		return 0, nil
	}
	release := d.Spec.Template.Annotations[WorkloadReleaseAnnotation]
	if release == "" || namespaceUID == "" || ca == "" {
		return 0, nil
	}
	var scopeRaw, depMAC, hpaMAC []byte
	var generation int64
	var accepted time.Time
	var maximum int32
	err := s.db.QueryRow(ctx, `SELECT i.scope,i.replica_spec_mac,j.replica_spec_mac,(i.outcome->>'Generation')::bigint,greatest(i.resolved_at,j.resolved_at),coalesce((a->'service'->'scale'->>'max')::integer,0)
 FROM loomplane_managed_workload_issuances i JOIN loomplane_managed_workload_issuances j ON j.org_id=i.org_id AND j.scope=i.scope
 JOIN loomplane_workload_controllers c ON c.org_id=i.org_id AND c.connection_id=i.scope->>'Connection' AND c.instance_id=i.scope->>'Instance' AND c.runtime_ref=i.scope->>'RuntimeRef'
 JOIN loomplane_plans p ON p.org_id=i.org_id AND p.id=i.scope->>'Plan' AND p.status='approved'
 CROSS JOIN LATERAL jsonb_array_elements(p.result->'actions') a
 WHERE i.org_id=$1 AND i.release_id=$2 AND i.scope->>'NamespaceUID'=$3 AND i.scope->>'ClusterCA'=$4 AND i.scope->>'Namespace'=$5 AND a->>'id'=i.scope->>'Action'
 AND c.body->>'release_id'=i.release_id AND c.body->>'deployment_uid'=$6 AND c.body->>'deployment_generation'=i.outcome->>'Generation'
 AND i.cleanup_grant_id IS NULL AND j.cleanup_grant_id IS NULL AND i.replica_spec_mac IS NOT NULL AND j.replica_spec_mac IS NOT NULL AND i.outcome->>'State'='accepted' AND j.outcome->>'State'='accepted'
 AND i.request->>'Resource'='deployments' AND i.outcome->>'UID'=$6 AND j.request->>'Resource'='horizontalpodautoscalers' AND j.outcome->>'UID'=$7 AND (j.outcome->>'Generation')::bigint=$8
 ORDER BY i.epoch DESC,i.issued_at DESC,j.issued_at DESC LIMIT 1`, org, release, namespaceUID, ca, d.Namespace, string(d.UID), string(h.UID), h.Generation).Scan(&scopeRaw, &depMAC, &hpaMAC, &generation, &accepted, &maximum)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if generation >= d.Generation || maximum != h.Spec.MaxReplicas || h.Status.LastScaleTime.Before(&metav1.Time{Time: accepted.Truncate(time.Second)}) {
		return 0, nil
	}
	var scope ManagedScope
	if err := json.Unmarshal(scopeRaw, &scope); err != nil {
		return 0, err
	}
	for _, v := range []struct {
		resource, uid string
		obj           any
		mac           []byte
	}{{"deployments", string(d.UID), d, depMAC}, {"horizontalpodautoscalers", string(h.UID), h, hpaMAC}} {
		body, err := json.Marshal(v.obj)
		if err != nil {
			return 0, err
		}
		if !hmac.Equal(v.mac, replicaSpecMAC(s.replicaProofKey, scope, v.resource, v.uid, body)) {
			return 0, nil
		}
	}
	// A newer API server may add fields that this client library does not
	// decode. Compare the full JSON response at the same resource version.
	for _, source := range []struct {
		api      rest.Interface
		resource string
		obj      metav1.Object
		mac      []byte
	}{{client.AppsV1().RESTClient(), "deployments", d, depMAC}, {client.AutoscalingV2().RESTClient(), "horizontalpodautoscalers", h, hpaMAC}} {
		if source.api == nil {
			return 0, nil
		}
		raw, err := source.api.Get().SetHeader("Accept", "application/json").Namespace(source.obj.GetNamespace()).Resource(source.resource).Name(source.obj.GetName()).DoRaw(ctx)
		if err != nil {
			return 0, err
		}
		var object metav1.PartialObjectMetadata
		if err := json.Unmarshal(raw, &object); err != nil {
			return 0, err
		}
		if object.UID != source.obj.GetUID() || object.ResourceVersion != source.obj.GetResourceVersion() || !hmac.Equal(source.mac, replicaSpecMAC(s.replicaProofKey, scope, source.resource, string(object.UID), raw)) {
			return 0, nil
		}
	}
	return generation, nil
}
