package federation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/xraph/loomplane/internal/agent"
	"github.com/xraph/loomplane/internal/agentinstall"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type launchCredentialFunc func(context.Context, string, string) (*connections.Connection, []byte, error)

func (f launchCredentialFunc) Credential(ctx context.Context, org, id string) (*connections.Connection, []byte, error) {
	return f(ctx, org, id)
}

type launchAtomicAuth struct{ denied atomic.Bool }

func (a *launchAtomicAuth) Require(context.Context, security.Principal, security.Permission, string) error {
	if a.denied.Load() {
		return security.ErrForbidden
	}
	return nil
}

func TestLaunchServiceUsesVerifiedCredentialAndGuardsRealWrite(t *testing.T) {
	for _, mode := range []string{"deliver", "revoke during read", "wrong CA", "recover", "recover wrong pod", "recover wrong namespace", "recover content", "recover revoked"} {
		t.Run(mode, func(t *testing.T) {
			svc, ready, store, principal, input, _ := launchServiceFixture(t)
			auth := &launchAtomicAuth{}
			svc.installations.destinations.authz = auth
			plan, err := agentinstall.NewKubernetesPlan("workloads", input.AttemptID, svc.cfg.AgentImage, false)
			if err != nil {
				t.Fatal(err)
			}
			pod := plan.Pod()
			pod.UID = types.UID(store.record.Profile.PodUID)
			pod.Spec.NodeName = "node"
			pod.Status.Phase = corev1.PodPending
			pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}}
			var reads, writes atomic.Int32
			var created *corev1.ConfigMap
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Error("destination credential missing")
					http.Error(w, "denied", http.StatusForbidden)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				var response any
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/namespaces/workloads":
					reads.Add(1)
					response = &corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: "workloads", UID: types.UID(ready.record.Binding.NamespaceUID)}}
					if mode == "recover wrong namespace" && writes.Load() > 0 {
						response.(*corev1.Namespace).UID = "foreign-namespace"
					}
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/namespaces/workloads/pods/"+pod.Name:
					reads.Add(1)
					if mode == "revoke during read" {
						auth.denied.Store(true)
					}
					response = pod.DeepCopy()
					if writes.Load() > 0 {
						response.(*corev1.Pod).Status.Phase = corev1.PodRunning
						if mode == "recover wrong pod" {
							response.(*corev1.Pod).UID = "foreign-pod"
						}
					}
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/namespaces/workloads/configmaps/"+pod.Name:
					reads.Add(1)
					if created == nil {
						http.NotFound(w, r)
						return
					}
					response = created.DeepCopy()
					if mode == "recover content" {
						response.(*corev1.ConfigMap).Data[agentinstall.ConfigKey] = "{}"
					}
					if mode == "recover revoked" {
						auth.denied.Store(true)
					}
				case r.Method == http.MethodPost && r.URL.Path == "/api/v1/namespaces/workloads/configmaps":
					writes.Add(1)
					var cm corev1.ConfigMap
					if decodeErr := json.NewDecoder(r.Body).Decode(&cm); decodeErr != nil {
						t.Error(decodeErr)
						http.Error(w, "invalid", http.StatusBadRequest)
						return
					}
					var launch agent.LaunchConfig
					if decodeErr := json.Unmarshal([]byte(cm.Data[agentinstall.ConfigKey]), &launch); decodeErr != nil || launch.EnrollmentID != ready.record.ID || launch.Registration.Agent != ready.record.Binding.Agent {
						t.Error("delivered wrong launch authority", decodeErr)
					}
					if cm.Immutable == nil || !*cm.Immutable || len(cm.OwnerReferences) != 1 || cm.OwnerReferences[0].UID != pod.UID {
						t.Error("unbound configuration write")
					}
					cm.UID = "config-uid"
					response = &cm
					created = cm.DeepCopy()
					if strings.HasPrefix(mode, "recover") {
						http.Error(w, "write outcome unknown", http.StatusInternalServerError)
						return
					}
				default:
					http.NotFound(w, r)
					return
				}
				if encodeErr := json.NewEncoder(w).Encode(response); encodeErr != nil {
					t.Error(encodeErr)
				}
			}))
			defer server.Close()
			raw, ca := attestationCredential(server)
			d := store.destinations.rows[input.DestinationID]
			d.Identity.Account = ca
			if mode == "wrong CA" {
				d.Identity.Account = "different-ca"
			}
			d.Capabilities.IdentityRevision = d.Identity.Revision()
			store.destinations.rows[d.ID] = d
			store.record.Profile.IdentityRevision = d.Identity.Revision()
			ready.record.Binding.Destination = d
			resourceReader, ok := svc.installations.destinations.connections.(*resources)
			if !ok {
				t.Fatal("unexpected fixture")
			}
			resourceReader.conn.Details["cluster_ca"] = d.Identity.Account
			resourceReader.conn.Details["namespace"] = "workloads"
			svc.installations.verify = func(context.Context, domain.Destination, KubernetesAttestationProfile) (workloadidentity.Binding, error) {
				return ready.record.Binding, nil
			}
			var revealed []byte
			svc.credentials = launchCredentialFunc(func(ctx context.Context, org, id string) (*connections.Connection, []byte, error) {
				if org != principal.OrgID || id != d.ConnectionID {
					return nil, nil, errors.New("unscoped credential reveal")
				}
				c, credentialErr := resourceReader.GetConnection(ctx, org, id)
				revealed = append([]byte(nil), raw...)
				return c, revealed, credentialErr
			})
			svc.deliver = svc.deliverKubernetes
			result, err := svc.Deliver(t.Context(), principal, input)
			if strings.HasPrefix(mode, "recover") {
				if err == nil || writes.Load() != 1 {
					t.Fatal("fixture did not leave uncertain write", err, writes.Load())
				}
				ready.fail = true
				result, err = svc.Recover(t.Context(), principal, input.DestinationID, input.AttemptID)
				if mode == "recover" {
					if err != nil || result.ConfigMapUID != "config-uid" || reads.Load() != 5 {
						t.Fatal("production recovery failed", err, reads.Load())
					}
				} else if err == nil {
					t.Fatal("unverified provider evidence confirmed")
				}
				if writes.Load() != 1 {
					t.Fatal("recovery wrote to Kubernetes")
				}
			} else if mode == "deliver" {
				if err != nil || result.ConfigMapUID != "config-uid" || writes.Load() != 1 || reads.Load() != 2 {
					t.Fatal("production writer failed", err, reads.Load(), writes.Load())
				}
			} else if err == nil || writes.Load() != 0 {
				t.Fatal("unsafe provider write", err, writes.Load())
			}
			if mode == "wrong CA" && reads.Load() != 0 {
				t.Fatal("wrong physical cluster reached provider")
			}
			if len(revealed) == 0 {
				t.Fatal("did not test credential reveal")
			}
			for _, v := range revealed {
				if v != 0 {
					t.Fatal("revealed credential buffer retained")
				}
			}
		})
	}
}
