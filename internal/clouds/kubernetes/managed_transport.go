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
)

// managedTransport surrounds the shared client-go transport, so each REST retry
// receives a separate issuance. It never persists a request or response body.
type managedTransport struct {
	next    http.RoundTripper
	journal ManagedJournal
	scope   ManagedScope
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
	if checkErr := t.journal.Resolve(ctx, issuance, t.scope, outcome); checkErr != nil {
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
	if len(parts) == 4 {
		out.Name = parts[3]
	}
	allowed := map[string]string{"secrets": "", "services": "", "serviceaccounts": "", "deployments": "apps", "horizontalpodautoscalers": "autoscaling", "ingresses": "networking.k8s.io", "roles": "rbac.authorization.k8s.io", "rolebindings": "rbac.authorization.k8s.io"}
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
		}
		if json.Unmarshal(body, &object) != nil {
			return out, ErrManagedAuthority
		}
		m := object.Metadata
		if m.Namespace != out.Namespace || m.Name == "" || out.Name != "" && out.Name != m.Name || m.Labels[owned.LabelOrg] != t.scope.Org || m.Labels[owned.LabelProject] != t.scope.Project || m.Labels[owned.LabelEnvironment] != t.scope.Environment || m.Labels[owned.LabelInstance] != t.scope.Instance {
			return out, ErrManagedAuthority
		}
		out.Name, out.UID = m.Name, string(m.UID)
		out.ResourceVersion = m.ResourceVersion
	case http.MethodDelete:
		var options metav1.DeleteOptions
		if out.Name == "" || json.Unmarshal(body, &options) != nil || options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID == "" {
			return out, ErrManagedAuthority
		}
		out.UID = string(*options.Preconditions.UID)
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
	iid, err := id.ParseWithPrefix(t.scope.Instance, id.PrefixInstance)
	if err != nil {
		return out, ErrManagedAuthority
	}
	name := WorkloadName(iid)
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
