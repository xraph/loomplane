package kubernetes

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"

	"k8s.io/client-go/rest"
)

// InClusterContext is the context an in-cluster connection shows, where a
// kubeconfig connection shows the context it chose.
const InClusterContext = "in-cluster"

// inClusterType is the type of the in-cluster credential's JSON.
const inClusterType = "loomplane_in_cluster"

// serviceAccountDir is where the kubelet mounts a pod's service account.
// It's fixed, as client-go's own in-cluster loader has it, so nothing a
// connection carries can point Loomplane at another token.
const serviceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// ErrNotInCluster is a Loomplane that can't see the cluster it runs in:
// not in a pod, or in one without a service account token.
var ErrNotInCluster = errors.New("not running in a Kubernetes pod with a service account token")

// reservedNamespaces belong to Kubernetes itself. Nothing Loomplane
// deploys belongs there, whatever its service account may do.
var reservedNamespaces = []string{"kube-system", "kube-public", "kube-node-lease"}

// InCluster is the cluster Loomplane runs in, as its pod's service account
// sees it.
type InCluster struct {
	// Host is the API server, from the KUBERNETES_SERVICE_ variables every
	// pod gets.
	Host string
	// CAData is the cluster's CA, from the pod's service account.
	CAData []byte
	// TokenFile is the service account token. Clients read it again as
	// the kubelet rotates it, so no copy of the token is ever kept.
	TokenFile string
	// Namespace is Loomplane's own, which no connection may deploy into.
	Namespace string
}

var inCluster atomic.Pointer[InCluster]

// DetectInCluster reads the service account the kubelet mounted in
// Loomplane's pod.
func DetectInCluster() (*InCluster, error) {
	return detectInCluster(os.Getenv, serviceAccountDir)
}

func detectInCluster(getenv func(string) string, dir string) (*InCluster, error) {
	host, port := strings.TrimSpace(getenv("KUBERNETES_SERVICE_HOST")), strings.TrimSpace(getenv("KUBERNETES_SERVICE_PORT"))
	if host == "" || port == "" {
		return nil, ErrNotInCluster
	}
	tokenFile := filepath.Join(dir, "token")
	token, err := os.ReadFile(tokenFile) // #nosec G304 -- the kubelet's fixed mount, never a caller's path.
	if err != nil || len(bytes.TrimSpace(token)) == 0 {
		return nil, fmt.Errorf("%w: mount the pod's service account token (automountServiceAccountToken)", ErrNotInCluster)
	}
	ca, err := os.ReadFile(filepath.Join(dir, "ca.crt")) // #nosec G304 -- as above.
	if err != nil || !x509.NewCertPool().AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("%w: the pod's service account has no usable CA certificate", ErrNotInCluster)
	}
	namespace, err := os.ReadFile(filepath.Join(dir, "namespace")) // #nosec G304 -- as above.
	if err != nil || !namespaceRE.Match(bytes.TrimSpace(namespace)) {
		return nil, fmt.Errorf("%w: the pod's service account doesn't name its namespace", ErrNotInCluster)
	}
	endpoint := &url.URL{Scheme: "https", Host: net.JoinHostPort(host, port)}
	return &InCluster{Host: endpoint.String(), CAData: ca, TokenFile: tokenFile, Namespace: string(bytes.TrimSpace(namespace))}, nil
}

// UseInCluster lets in-cluster credentials reach ic. Nil, the default,
// refuses every one, so a forged credential is worthless wherever the
// operator never turned the feature on.
func UseInCluster(ic *InCluster) { inCluster.Store(ic) }

// CurrentInCluster is the cluster in-cluster credentials reach, or nil.
func CurrentInCluster() *InCluster { return inCluster.Load() }

// InClusterCredential is an in-cluster connection's credential. It holds
// nothing secret, and nothing that could send the token elsewhere: where
// the API server is and which token to present both come from the pod.
func InClusterCredential() []byte { return []byte(`{"type":"` + inClusterType + `"}`) }

// IsInClusterCredential reports whether raw is an in-cluster credential.
func IsInClusterCredential(raw []byte) bool {
	var probe struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(raw, &probe) == nil && probe.Type == inClusterType
}

// parseInCluster is ParseConfig for the in-cluster credential.
func parseInCluster(settings map[string]string) (*Config, error) {
	ic := inCluster.Load()
	if ic == nil {
		return nil, fmt.Errorf("%w: this Loomplane doesn't connect the cluster it runs in", ErrInvalid)
	}
	namespace := strings.TrimSpace(settings["namespace"])
	if err := checkInClusterNamespace(namespace, ic.Namespace); err != nil {
		return nil, err
	}
	rc := &rest.Config{
		Host:            ic.Host,
		BearerTokenFile: ic.TokenFile,
		TLSClientConfig: rest.TLSClientConfig{CAData: ic.CAData},
		// As for a kubeconfig, HTTP_PROXY must not reroute the connection.
		Proxy:     func(*http.Request) (*url.URL, error) { return nil, nil },
		UserAgent: "loomplane",
	}
	return configFor(rc, InClusterContext, namespace, ic.CAData, settings)
}

// checkInClusterNamespace decides where an in-cluster connection may
// deploy. Its service account is Loomplane's own, so its own namespace,
// with its database password and secret key, is never a target.
func checkInClusterNamespace(namespace, own string) error {
	switch {
	case namespace == "":
		return fmt.Errorf("%w: choose the namespace to deploy into", ErrInvalid)
	case !namespaceRE.MatchString(namespace):
		return fmt.Errorf("%w: namespace must be a DNS label of at most 63 characters", ErrInvalid)
	case namespace == own:
		return fmt.Errorf("%w: Loomplane runs in namespace %s, so it can't deploy there; choose another", ErrInvalid, own)
	case slices.Contains(reservedNamespaces, namespace):
		return fmt.Errorf("%w: %s belongs to Kubernetes; choose another namespace", ErrInvalid, namespace)
	}
	return nil
}
