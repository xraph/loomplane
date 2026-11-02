package kubernetes

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func lifecycleScopeValid(scope ManagedScope) bool {
	values := []string{scope.LifecycleRunID, scope.LifecycleJobName, scope.LifecycleDigest, scope.LifecycleSpecDigest, scope.LifecycleSecretDigest}
	present := 0
	for _, value := range values {
		if value != "" {
			present++
		}
	}
	if present == 0 {
		return true
	}
	if present != len(values) || hasGroupScope(scope) || len(scope.LifecycleRunID) != 64 || len(scope.LifecycleJobName) != 53 || !strings.HasPrefix(scope.LifecycleJobName, "lp-lifecycle-") {
		return false
	}
	if _, err := hex.DecodeString(scope.LifecycleRunID); err != nil {
		return false
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(scope.LifecycleJobName, "lp-lifecycle-")); err != nil {
		return false
	}
	for _, digest := range values[2:] {
		if len(digest) != 71 || !strings.HasPrefix(digest, "sha256:") {
			return false
		}
		if _, err := hex.DecodeString(digest[7:]); err != nil {
			return false
		}
	}
	return true
}
func validateLifecycleRequest(scope ManagedScope, request ManagedRequest, body []byte) error {
	if !lifecycleScopeValid(scope) || scope.LifecycleRunID == "" {
		return ErrManagedAuthority
	}
	if request.Resource == "jobs" && request.Name != scope.LifecycleJobName || request.Resource == "secrets" && request.Name != scope.LifecycleJobName+"-runtime" || request.Resource != "jobs" && request.Resource != "secrets" {
		return ErrManagedAuthority
	}
	if request.Method == http.MethodDelete {
		if request.Resource == "jobs" && request.PropagationPolicy != string(metav1.DeletePropagationForeground) || request.Resource == "secrets" && request.PropagationPolicy != "" {
			return ErrManagedAuthority
		}
		if request.UID == "" || request.ResourceVersion == "" {
			return ErrManagedAuthority
		}
		return nil
	}
	// Lifecycle objects are immutable. Retrying never updates an existing Job.
	if request.Method != http.MethodPost {
		return ErrManagedAuthority
	}
	var object struct {
		Metadata metav1.ObjectMeta `json:"metadata"`
		Spec     json.RawMessage   `json:"spec"`
		Data     map[string][]byte `json:"data"`
	}
	if json.Unmarshal(body, &object) != nil || object.Metadata.Annotations[lifecycleDigestAnnotation] != scope.LifecycleDigest || object.Metadata.Labels[lifecycleRunLabel] != scope.LifecycleRunID[:40] {
		return ErrManagedAuthority
	}
	var raw []byte
	var err error
	expected := scope.LifecycleSpecDigest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if request.Resource == "secrets" {
		var secret corev1.Secret
		if decoder.Decode(&secret) != nil || len(secret.StringData) != 0 || secret.Type != corev1.SecretTypeOpaque {
			return ErrManagedAuthority
		}
		raw, err = json.Marshal(secret.Data)
		expected = scope.LifecycleSecretDigest
	} else {
		var job batchv1.Job
		if decoder.Decode(&job) != nil {
			return ErrManagedAuthority
		}
		raw, err = json.Marshal(job.Spec)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return ErrManagedAuthority
	}

	if err != nil || fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) != expected {
		return ErrManagedAuthority
	}
	return nil
}

func lifecycleManagedScopeValid(scope ManagedScope) bool {
	if !hasLifecycleScope(scope) || !lifecycleScopeValid(scope) {
		return false
	}
	base := scope
	base.LifecycleRunID, base.LifecycleJobName, base.LifecycleDigest, base.LifecycleSpecDigest, base.LifecycleSecretDigest = "", "", "", "", ""
	return legacyManagedScopeValid(base)
}
