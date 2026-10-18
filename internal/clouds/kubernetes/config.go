// Package kubernetes connects to an explicitly chosen cluster and namespace.
package kubernetes

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// ErrInvalid marks a credential or configuration that cannot be used safely.
var ErrInvalid = errors.New("invalid Kubernetes connection")

var namespaceRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// Config keeps credentials in REST only. Details contains safe display fields.
type Config struct {
	REST      *rest.Config
	Namespace string
	Context   string
	Details   map[string]string
}

// ParseConfig accepts embedded credentials only, with no ambient loading rules.
func ParseConfig(raw []byte, settings map[string]string) (*Config, error) {
	if len(raw) == 0 || len(raw) > 256<<10 {
		return nil, fmt.Errorf("%w: provide a self-contained kubeconfig of at most 256 KiB", ErrInvalid)
	}
	config, err := clientcmd.Load(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: kubeconfig could not be parsed", ErrInvalid)
	}
	for _, cluster := range config.Clusters {
		switch {
		case cluster.CertificateAuthority != "":
			return nil, fmt.Errorf("%w: CA file references are not supported; embed certificate-authority-data", ErrInvalid)
		case cluster.InsecureSkipTLSVerify:
			return nil, fmt.Errorf("%w: certificate validation must remain enabled", ErrInvalid)
		case cluster.ProxyURL != "":
			return nil, fmt.Errorf("%w: kubeconfig proxy URLs are not supported", ErrInvalid)
		}
		u, parseErr := url.Parse(cluster.Server)
		if parseErr != nil || u.User != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("%w: use a cluster endpoint without userinfo, query or fragment", ErrInvalid)
		}
		if u.Scheme != "https" {
			return nil, fmt.Errorf("%w: the cluster endpoint must use https", ErrInvalid)
		}
		pool := x509.NewCertPool()
		if len(cluster.CertificateAuthorityData) == 0 || !pool.AppendCertsFromPEM(cluster.CertificateAuthorityData) {
			return nil, fmt.Errorf("%w: embed a valid certificate authority", ErrInvalid)
		}
	}
	for _, auth := range config.AuthInfos {
		switch {
		case auth.Exec != nil:
			return nil, fmt.Errorf("%w: exec credential plugins are not supported", ErrInvalid)
		case auth.AuthProvider != nil:
			return nil, fmt.Errorf("%w: auth-provider credential plugins are not supported", ErrInvalid)
		case auth.TokenFile != "" || auth.ClientCertificate != "" || auth.ClientKey != "":
			return nil, fmt.Errorf("%w: credential file references are not supported", ErrInvalid)
		case auth.Impersonate != "" || len(auth.ImpersonateGroups) != 0 || len(auth.ImpersonateUserExtra) != 0 || auth.ImpersonateUID != "":
			return nil, fmt.Errorf("%w: impersonation is not supported", ErrInvalid)
		case auth.Username != "" || auth.Password != "":
			return nil, fmt.Errorf("%w: use a token or embedded client certificate", ErrInvalid)
		}
		if len(auth.ClientCertificateData) != 0 || len(auth.ClientKeyData) != 0 {
			if _, keyErr := tls.X509KeyPair(auth.ClientCertificateData, auth.ClientKeyData); keyErr != nil {
				return nil, fmt.Errorf("%w: client certificate and private key do not form a valid pair", ErrInvalid)
			}
		} else if strings.TrimSpace(auth.Token) == "" {
			return nil, fmt.Errorf("%w: provide a token or embedded client certificate", ErrInvalid)
		}
		if auth.Token != "" && strings.ContainsAny(auth.Token, "\r\n\t ") {
			return nil, fmt.Errorf("%w: a token must not contain whitespace", ErrInvalid)
		}
	}
	contextName := strings.TrimSpace(settings["context"])
	if contextName == "" {
		contextName = config.CurrentContext
	}
	chosen := config.Contexts[contextName]
	if contextName == "" || chosen == nil || config.Clusters[chosen.Cluster] == nil || config.AuthInfos[chosen.AuthInfo] == nil {
		return nil, fmt.Errorf("%w: choose a context with an explicit cluster and credentials", ErrInvalid)
	}
	namespace := strings.TrimSpace(settings["namespace"])
	if namespace == "" {
		namespace = chosen.Namespace
	}
	if namespace == "" {
		namespace = "default"
	}
	if !namespaceRE.MatchString(namespace) {
		return nil, fmt.Errorf("%w: namespace must be a DNS label of at most 63 characters", ErrInvalid)
	}
	loader := clientcmd.NewNonInteractiveClientConfig(*config, contextName, &clientcmd.ConfigOverrides{}, nil)
	rc, err := loader.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("%w: the selected context could not be loaded", ErrInvalid)
	}
	// Even HTTP_PROXY must not change the connection the person selected.
	rc.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
	rc.UserAgent = "loomplane"
	ca := sha256.Sum256(config.Clusters[chosen.Cluster].CertificateAuthorityData)
	details := map[string]string{"endpoint": rc.Host, "namespace": namespace, "context": contextName, "cluster_ca": hex.EncodeToString(ca[:])}
	if class := strings.TrimSpace(settings["storage_class"]); class != "" {
		if len(validation.IsDNS1123Subdomain(class)) != 0 {
			return nil, fmt.Errorf("%w: storage_class must be a Kubernetes storage class name", ErrInvalid)
		}
		details["storage_class"] = class
	}
	if rc.ServerName != "" {
		details["tls_server_name"] = rc.ServerName
	}
	if name := strings.TrimSpace(settings["image_pull_secret"]); name != "" {
		if err := validatePullSecret(name); err != nil {
			return nil, err
		}
		details["image_pull_secret"] = name
	}
	return &Config{REST: rc, Namespace: namespace, Context: contextName, Details: details}, nil
}
