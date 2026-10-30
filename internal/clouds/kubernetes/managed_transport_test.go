package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/ctrlplane/id"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

type memoryManagedJournal struct {
	issued   []ManagedRequest
	outcomes map[string]ManagedOutcome
	denied   bool
}

func (j *memoryManagedJournal) Issue(_ context.Context, _ ManagedScope, r ManagedRequest) (string, error) {
	if j.denied {
		return "", ErrManagedAuthority
	}
	j.issued = append(j.issued, r)
	return fmt.Sprint(len(j.issued)), nil
}
func (j *memoryManagedJournal) Resolve(_ context.Context, id string, _ ManagedScope, o ManagedOutcome) error {
	j.outcomes[id] = o
	return nil
}
func transportScope() ManagedScope {
	s := ManagedScope{Org: "org", Project: "project", Environment: "env", Service: "api", Plan: "plan", Deployment: "deployment", Action: "service:api", Connection: "connection", Instance: id.New(id.PrefixInstance).String(), RuntimeRef: "org/env/api/runtime_one", RuntimeRevision: "deployment/service:api", Namespace: "apps", NamespaceUID: "namespace", ClusterCA: strings.Repeat("a", 64), Epoch: 1}
	s.Release = WorkloadReleaseID(s.RuntimeRef, s.RuntimeRevision)
	return s
}
func TestManagedTransportRetriesAndUncertainty(t *testing.T) {
	for _, code := range []int{429, 503} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			scope := transportScope()
			journal := &memoryManagedJournal{outcomes: map[string]ManagedOutcome{}}
			calls := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if len(journal.issued) != calls {
					t.Error("write sent before its issuance")
				}
				w.Header().Set("Content-Type", "application/json")
				if calls == 1 {
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(code)
					reason := "TooManyRequests"
					if code == 503 {
						reason = "ServiceUnavailable"
					}
					fmt.Fprintf(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":%q,"code":%d}`, reason, code)
					return
				}
				var secret corev1.Secret
				if err := json.NewDecoder(r.Body).Decode(&secret); err != nil {
					t.Error(err)
				}
				secret.UID = "uid"
				secret.ResourceVersion = "1"
				w.WriteHeader(201)
				_ = json.NewEncoder(w).Encode(secret)
			}))
			defer api.Close()
			client, err := New(&Config{REST: &rest.Config{Host: api.URL}, Namespace: scope.Namespace, ManagedWorkload: true, ManagedJournal: journal, ManagedScope: scope})
			if err != nil {
				t.Fatal(err)
			}
			iid, _ := id.ParseWithPrefix(scope.Instance, id.PrefixInstance)
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: WorkloadName(iid) + "-runtime", Namespace: scope.Namespace, Labels: map[string]string{owned.LabelOrg: scope.Org, owned.LabelProject: scope.Project, owned.LabelEnvironment: scope.Environment, owned.LabelInstance: scope.Instance}}, Data: map[string][]byte{"TOKEN": []byte("private-do-not-save")}}
			if _, err = client.Kube.CoreV1().Secrets(scope.Namespace).Create(t.Context(), secret, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			if calls != 2 || len(journal.issued) != 2 || journal.outcomes["2"].State != "accepted" {
				t.Fatalf("retry journal: calls=%d outcomes=%v", calls, journal.outcomes)
			}
			if _, known := journal.outcomes["1"]; known != (code == 429) {
				t.Fatal("ambiguous request resolved by unrelated retry")
			}
			safe, _ := json.Marshal(journal.issued)
			if strings.Contains(string(safe), "private-do-not-save") || strings.Contains(string(safe), "TOKEN") {
				t.Fatal("secret entered journal")
			}
			journal.denied = true
			if _, err = client.Kube.CoreV1().Secrets(scope.Namespace).Create(t.Context(), secret, metav1.CreateOptions{}); !errors.Is(err, ErrManagedAuthority) {
				t.Fatalf("revocation: %v", err)
			}
			if calls != 2 {
				t.Fatal("revoked write reached network")
			}
		})
	}
}
func TestManagedResponseRequiresExactOutcome(t *testing.T) {
	request := ManagedRequest{Method: "PUT", Name: "owned", Namespace: "apps", UID: "old"}
	for _, body := range []string{`{}`, `{"kind":"Deployment","metadata":{"name":"owned","namespace":"apps","uid":"new","resourceVersion":"1"}}`, `{"kind":"Status","status":"Success"}`} {
		if _, ok := managedResponse(request, 200, []byte(body)); ok {
			t.Fatal("unbound response settled issuance")
		}
	}
	if _, ok := managedResponse(request, 503, []byte(`{"kind":"Status","status":"Failure","code":503,"reason":"ServiceUnavailable"}`)); ok {
		t.Fatal("server uncertainty closed")
	}
	request.Method = "DELETE"
	if outcome, ok := managedResponse(request, 200, []byte(`{"kind":"Status","status":"Success","details":{"uid":"old"}}`)); !ok || outcome.UID != "old" {
		t.Fatal("exact delete acceptance lost")
	}
}

func TestManagedTransportDeleteResourceVersion(t *testing.T) {
	scope := transportScope()
	iid, _ := id.ParseWithPrefix(scope.Instance, id.PrefixInstance)
	name := WorkloadName(iid)
	transport := &managedTransport{scope: scope}
	for _, tc := range []struct {
		name, preconditions, version string
		denied                       bool
	}{
		{name: "historical", preconditions: `{"uid":"owned"}`},
		{name: "versioned", preconditions: `{"uid":"owned","resourceVersion":"42"}`, version: "42"},
		{name: "empty", preconditions: `{"uid":"owned","resourceVersion":""}`, denied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"preconditions":` + tc.preconditions + `}`
			req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/apis/apps/v1/namespaces/"+scope.Namespace+"/deployments/"+name, strings.NewReader(body))
			got, err := transport.request(req)
			if tc.denied {
				if !errors.Is(err, ErrManagedAuthority) {
					t.Fatalf("invalid precondition: %v", err)
				}
				return
			}
			if err != nil || got.UID != "owned" || got.ResourceVersion != tc.version {
				t.Fatalf("preconditions lost: %+v %v", got, err)
			}
		})
	}
}

func TestManagedRequestHistoricalJSON(t *testing.T) {
	const historical = `{"Group":"apps","Version":"v1","Resource":"deployments","Namespace":"apps","Name":"owned","Method":"DELETE","UID":"uid"}`
	var request ManagedRequest
	if err := json.Unmarshal([]byte(historical), &request); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(request)
	if err != nil || string(raw) != historical {
		t.Fatalf("historical request changed: %s %v", raw, err)
	}
	request.ResourceVersion = "42"
	raw, err = json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var recovered ManagedRequest
	if err = json.Unmarshal(raw, &recovered); err != nil || recovered != request {
		t.Fatalf("versioned request changed: %+v %v", recovered, err)
	}
}
