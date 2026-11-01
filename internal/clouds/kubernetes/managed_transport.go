package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/xraph/ctrlplane/id"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/clouds/owned"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// managedTransport surrounds the shared client-go transport, so each REST retry
// receives a separate issuance. It never persists a request or response body.
type managedTransport struct {
	next    http.RoundTripper
	journal ManagedJournal
	scope   ManagedScope
	cleanup bool
}

func (t *managedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodGet || req.Method == http.MethodHead {
		return t.next.RoundTrip(req)
	}
	// Access reviews are authorization probes, not persisted application writes.
	if req.Method == http.MethodPost && req.URL.Path == "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews" {
		return t.next.RoundTrip(req)
	}
	if t.journal == nil || !managedScopeValid(t.scope) {
		return nil, ErrManagedAuthority
	}
	mutation, err := t.request(req)
	if err != nil {
		return nil, err
	}
	issuance, err := t.journal.Issue(req.Context(), t.scope, mutation)
	if err != nil {
		return nil, err
	}
	response, err := t.next.RoundTrip(req)
	if err != nil || response == nil {
		return response, err
	}
	// Read a bounded response before recording authoritative metadata. A short
	// read, oversized response, timeout or malformed response remains unknown.
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 16<<20+1))
	closeErr := response.Body.Close()
	response.Body = io.NopCloser(bytes.NewReader(data))
	if readErr != nil || closeErr != nil || len(data) > 16<<20 {
		return response, errors.Join(readErr, closeErr, ErrManagedAuthority)
	}
	outcome, known := managedResponse(mutation, response.StatusCode, data)
	if !known {
		return response, nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), 15*time.Second)
	defer cancel()
	var checkErr error
	if store, ok := t.journal.(*ControllerStore); ok {
		checkErr = store.resolveApplicationResponse(ctx, issuance, t.scope, mutation, outcome, data)
	} else {
		checkErr = t.journal.Resolve(ctx, issuance, t.scope, outcome)
	}
	if checkErr != nil {
		return nil, checkErr
	}
	return response, nil
}

func (t *managedTransport) request(req *http.Request) (ManagedRequest, error) {
	out := ManagedRequest{Method: req.Method}
	parts := strings.Split(strings.TrimPrefix(req.URL.Path, "/"), "/")
	switch {
	case len(parts) >= 5 && parts[0] == "api":
		out.Version = parts[1]
		parts = parts[2:]
	case len(parts) >= 6 && parts[0] == "apis":
		out.Group, out.Version = parts[1], parts[2]
		parts = parts[3:]
	default:
		return out, ErrManagedAuthority
	}
	if len(parts) < 3 || len(parts) > 4 || parts[0] != "namespaces" || parts[1] != t.scope.Namespace {
		return out, ErrManagedAuthority
	}
	out.Namespace, out.Resource = parts[1], parts[2]
	if t.scope.GroupID != "" && (t.scope.GroupOperation == "prepare" && out.Resource == "ingresses" || t.scope.GroupOperation == "promote" && out.Resource != "ingresses" || req.Method == http.MethodDelete) {
		return out, ErrManagedAuthority
	}
	if len(parts) == 4 {
		out.Name = parts[3]
	}
	allowed := map[string]string{"secrets": "", "services": "", "serviceaccounts": "", "deployments": "apps", "horizontalpodautoscalers": "autoscaling", "ingresses": "networking.k8s.io", "roles": "rbac.authorization.k8s.io", "rolebindings": "rbac.authorization.k8s.io"}
	if hasLifecycleScope(t.scope) {
		allowed = map[string]string{"jobs": "batch", "secrets": ""}
	}
	descendant := t.cleanup && (out.Resource == "pods" || out.Resource == "replicasets")
	query := req.URL.Query()
	delete(query, "timeout")
	if hasLifecycleScope(t.scope) && len(query) > 0 {
		return out, ErrManagedAuthority
	}
	if t.cleanup && (req.Method != http.MethodDelete || len(query) > 0) {
		return out, ErrManagedAuthority
	}
	if descendant {
		allowed["pods"] = ""
		allowed["replicasets"] = "apps"
	}
	group, ok := allowed[out.Resource]
	validVersion := out.Version == "v1" || out.Resource == "horizontalpodautoscalers" && out.Version == "v2"
	if !ok || group != out.Group || !validVersion {
		return out, ErrManagedAuthority
	}
	if req.Body == nil {
		return out, ErrManagedAuthority
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, 16<<20+1))
	if err != nil {
		return out, err
	}
	if closeErr := req.Body.Close(); closeErr != nil {
		return out, closeErr
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	if len(body) > 16<<20 {
		return out, ErrManagedAuthority
	}
	switch req.Method {
	case http.MethodPost, http.MethodPut:
		var object struct {
			Metadata metav1.ObjectMeta `json:"metadata"`
			Spec     struct {
				Template struct {
					Spec struct {
						NodeSelector map[string]string `json:"nodeSelector"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		}
		if json.Unmarshal(body, &object) != nil {
			return out, ErrManagedAuthority
		}
		if out.Resource == "deployments" {
			out.SchedulingArchitecture = object.Spec.Template.Spec.NodeSelector["kubernetes.io/arch"]
		}
		m := object.Metadata
		groupIngress := t.scope.GroupID != "" && out.Resource == "ingresses"
		if m.Namespace != out.Namespace || m.Name == "" || out.Name != "" && out.Name != m.Name || m.Labels[owned.LabelOrg] != t.scope.Org || m.Labels[owned.LabelProject] != t.scope.Project || m.Labels[owned.LabelEnvironment] != t.scope.Environment || !groupIngress && m.Labels[owned.LabelInstance] != t.scope.Instance {
			return out, ErrManagedAuthority
		}
		if groupIngress {
			var receipt domain.ServingMemberReceipt
			if m.Labels[groupIngressLabel] != t.scope.GroupID || m.Labels[groupMemberLabel] != t.scope.MemberID || json.Unmarshal([]byte(m.Annotations[groupSwitchAnnotation]), &receipt) != nil || receipt.Scope != (domain.Scope{OrgID: t.scope.Org, ProjectID: t.scope.Project, EnvironmentID: t.scope.Environment}) || receipt.GroupID != t.scope.GroupID || receipt.MemberID != t.scope.MemberID || receipt.Generation != t.scope.GroupGeneration || receipt.Namespace != t.scope.Namespace || receipt.NamespaceUID != t.scope.NamespaceUID {
				return out, ErrManagedAuthority
			}
			out.GroupSwitch = &receipt
		}
		out.Name, out.UID = m.Name, string(m.UID)
		out.ResourceVersion = m.ResourceVersion
	case http.MethodDelete:
		var options struct {
			metav1.TypeMeta
			Preconditions     *metav1.Preconditions       `json:"preconditions,omitempty"`
			PropagationPolicy *metav1.DeletionPropagation `json:"propagationPolicy,omitempty"`
		}
		decoder := json.NewDecoder(bytes.NewReader(body))
		if t.cleanup || hasLifecycleScope(t.scope) {
			decoder.DisallowUnknownFields()
		}
		if out.Name == "" || decoder.Decode(&options) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID == "" {
			return out, ErrManagedAuthority
		}
		out.UID = string(*options.Preconditions.UID)
		if hasLifecycleScope(t.scope) && options.PropagationPolicy != nil {
			out.PropagationPolicy = string(*options.PropagationPolicy)
		}
		if t.cleanup {
			if options.PropagationPolicy != nil {
				out.PropagationPolicy = string(*options.PropagationPolicy)
			}
			if out.PropagationPolicy != string(metav1.DeletePropagationOrphan) {
				return out, ErrManagedAuthority
			}
		}
		if options.Preconditions.ResourceVersion != nil {
			out.ResourceVersion = *options.Preconditions.ResourceVersion
			if out.ResourceVersion == "" {
				return out, ErrManagedAuthority
			}
		}
	default:
		// No current application path uses PATCH or subresources. Refuse unknown
		// mutations until their ownership and outcome semantics are implemented.
		return out, ErrManagedAuthority
	}
	if hasLifecycleScope(t.scope) {
		if validationErr := validateLifecycleRequest(t.scope, out, body); validationErr != nil {
			return out, validationErr
		}
		return out, nil
	}
	if t.cleanup && out.ResourceVersion == "" {
		return out, ErrManagedAuthority
	}
	if descendant {
		return out, nil
	}
	iid, err := id.ParseWithPrefix(t.scope.Instance, id.PrefixInstance)
	if err != nil {
		return out, ErrManagedAuthority
	}
	name := WorkloadName(iid)
	if t.scope.GroupID != "" && out.Resource == "ingresses" {
		name = GroupIngressName(domain.Scope{OrgID: t.scope.Org, ProjectID: t.scope.Project, EnvironmentID: t.scope.Environment}, t.scope.GroupID, t.scope.MemberID)
	}
	validName := out.Name == name || out.Resource == "secrets" && (out.Name == name+"-runtime" || out.Name == name+"-pull")
	if !validName {
		return out, ErrManagedAuthority
	}
	return out, nil
}

func managedResponse(req ManagedRequest, code int, body []byte) (ManagedOutcome, bool) {
	out := ManagedOutcome{StatusCode: code}
	var result struct {
		Kind     string                `json:"kind"`
		Metadata metav1.ObjectMeta     `json:"metadata"`
		Status   json.RawMessage       `json:"status"`
		Code     int                   `json:"code"`
		Reason   metav1.StatusReason   `json:"reason"`
		Details  *metav1.StatusDetails `json:"details"`
	}
	if json.Unmarshal(body, &result) != nil {
		return out, false
	}
	var status string
	if result.Kind == "Status" && json.Unmarshal(result.Status, &status) != nil {
		return out, false
	}
	if code >= 200 && code <= 299 {
		if result.Kind == "Status" {
			if req.Method != http.MethodDelete || status != "Success" || req.UID == "" || result.Details == nil || string(result.Details.UID) != req.UID {
				return out, false
			}
			out.UID = req.UID
		} else {
			m := result.Metadata
			if m.Name != req.Name || m.Namespace != req.Namespace || m.UID == "" || m.ResourceVersion == "" || req.UID != "" && req.UID != string(m.UID) {
				return out, false
			}
			out.UID, out.ResourceVersion, out.Generation = string(m.UID), m.ResourceVersion, m.Generation
		}
		out.State = "accepted"
		return out, true
	}
	// Only exact API Status refusals close a request. Generic proxy errors,
	// server errors and transport failures retain uncertainty.
	reasons := map[int]metav1.StatusReason{400: metav1.StatusReasonBadRequest, 401: metav1.StatusReasonUnauthorized, 403: metav1.StatusReasonForbidden, 404: metav1.StatusReasonNotFound, 409: metav1.StatusReasonConflict, 422: metav1.StatusReasonInvalid, 429: metav1.StatusReasonTooManyRequests}
	reason, ok := reasons[code]
	if code == 409 && result.Reason == metav1.StatusReasonAlreadyExists {
		reason = metav1.StatusReasonAlreadyExists
	}
	if ok && result.Kind == "Status" && status == "Failure" && result.Code == code && result.Reason == reason {
		out.State = "rejected"
		return out, true
	}
	return out, false
}
