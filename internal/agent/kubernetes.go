package agent

import (
	"net/http"
	"net/url"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
	discoveryclient "k8s.io/client-go/kubernetes/typed/discovery/v1"
	"k8s.io/client-go/rest"

	"github.com/xraph/loomplane/internal/runtimecatalog"
)

func inClusterSlices(namespace string) (runtimecatalog.EndpointSliceClient, func(), error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, nil, err
	}
	return scopedSlices(cfg, namespace)
}
func scopedSlices(cfg *rest.Config, namespace string) (runtimecatalog.EndpointSliceClient, func(), error) {
	if cfg == nil || len(validation.IsDNS1123Label(namespace)) != 0 {
		return nil, nil, ErrConfiguration
	}
	endpoint, err := url.Parse(cfg.Host)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.RawPath != "" || endpoint.Opaque != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") || cfg.Insecure || (cfg.CAFile == "" && len(cfg.CAData) == 0) || cfg.ExecProvider != nil || cfg.AuthProvider != nil || cfg.Username != "" || cfg.Password != "" || cfg.Impersonate.UserName != "" || cfg.Impersonate.UID != "" || len(cfg.Impersonate.Groups) != 0 || len(cfg.Impersonate.Extra) != 0 || cfg.Transport != nil || cfg.WrapTransport != nil {
		return nil, nil, ErrConfiguration
	}
	local := rest.CopyConfig(cfg)
	local.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
	local.Timeout, local.QPS, local.Burst = 15*time.Second, 5, 10
	local.ContentType, local.AcceptContentTypes = "application/json", "application/json"
	local.UserAgent = "loomplane-agent"
	client, err := rest.HTTPClientFor(local)
	if err != nil {
		return nil, nil, err
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	discovery, err := discoveryclient.NewForConfigAndClient(local, client)
	if err != nil {
		client.CloseIdleConnections()
		return nil, nil, err
	}
	return discovery.EndpointSlices(namespace), client.CloseIdleConnections, nil
}
