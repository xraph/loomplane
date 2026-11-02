package octopusloom

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/recipes"
)

// farpTokenPlaceholder is the only reference to the FARP token the config
// holds. Octopus fills it from its environment when it starts.
const farpTokenPlaceholder = "${OCTOPUS_FARP_TOKEN}" //nolint:gosec // An env var reference Octopus fills in, not a credential.

// The structs below are Octopus's config.yaml. Their field order is the order
// of the output, which keeps the bytes the same for the same Desired.
type (
	octopusConfig struct {
		Gateway   listenConfig     `yaml:"gateway"`
		Admin     adminConfig      `yaml:"admin"`
		Farp      farpConfig       `yaml:"farp"`
		Upstreams []upstreamConfig `yaml:"upstreams"`
		Routes    []routeConfig    `yaml:"routes"`
	}
	listenConfig struct {
		Listen string `yaml:"listen"`
	}
	adminConfig struct {
		Listen      string `yaml:"listen"`
		RequireAuth bool   `yaml:"require_auth"`
	}
	farpConfig struct {
		Enabled           bool             `yaml:"enabled"`
		RegistrationToken string           `yaml:"registration_token"`
		Gateway           *farpGateway     `yaml:"gateway,omitempty"`
		Discovery         *discoveryConfig `yaml:"discovery,omitempty"`
	}
	farpGateway struct {
		Hostnames []string `yaml:"hostnames"`
	}
	discoveryConfig struct {
		Backends []backendConfig `yaml:"backends"`
	}
	backendConfig struct {
		Type    string               `yaml:"type"`
		Enabled bool                 `yaml:"enabled"`
		Config  kubernetesBackendCfg `yaml:"config"`
	}
	kubernetesBackendCfg struct {
		Namespace         string `yaml:"namespace"`
		LabelSelector     string `yaml:"label_selector"`
		WatchInterval     string `yaml:"watch_interval"`
		UseEndpointSlices bool   `yaml:"use_endpoint_slices"`
		IncludeNotReady   bool   `yaml:"include_not_ready"`
	}
	upstreamConfig struct {
		Name      string           `yaml:"name"`
		LBPolicy  string           `yaml:"lb_policy"`
		Instances []instanceConfig `yaml:"instances"`
	}
	instanceConfig struct {
		ID     string `yaml:"id"`
		Host   string `yaml:"host"`
		Port   int    `yaml:"port"`
		Weight int    `yaml:"weight"`
	}
	routeConfig struct {
		ID          string   `yaml:"id"`
		Hosts       []string `yaml:"hosts,omitempty"`
		Path        string   `yaml:"path"`
		Methods     []string `yaml:"methods"`
		Upstream    string   `yaml:"upstream"`
		StripPrefix string   `yaml:"strip_prefix,omitempty"`
		Priority    int      `yaml:"priority"`
	}
)

// RenderConfig renders Octopus's config.yaml for d. The admin API always
// requires authentication, and the only secret it refers to is the FARP token,
// by placeholder. The output is deterministic: it is hashed into a revision
// label, so the same Desired always gives the same bytes.
func RenderConfig(d Desired) ([]byte, error) {
	cfg := octopusConfig{
		Gateway: listenConfig{Listen: fmt.Sprintf("0.0.0.0:%d", recipes.OctopusGatewayPort)},
		Admin:   adminConfig{Listen: fmt.Sprintf("0.0.0.0:%d", recipes.OctopusAdminPort), RequireAuth: true},
		Farp:    farpConfig{Enabled: true, RegistrationToken: farpTokenPlaceholder},
	}
	if len(d.FarpHostnames) > 0 {
		cfg.Farp.Gateway = &farpGateway{Hostnames: d.FarpHostnames}
	}
	if d.Provider == ProviderKubernetes {
		cfg.Farp.Discovery = &discoveryConfig{Backends: []backendConfig{{
			Type: "kubernetes", Enabled: true,
			Config: kubernetesBackendCfg{
				Namespace: d.Namespace,
				LabelSelector: owned.FarpEnabledLabel + "=true," +
					owned.LabelEnvironment + "=" + d.EnvironmentID + "," +
					owned.FarpGatewayLabel + "=" + d.Resource,
				WatchInterval:     "30s",
				UseEndpointSlices: true,
			},
		}}}
	}
	cfg.Upstreams = make([]upstreamConfig, 0, len(d.Upstreams))
	for _, u := range d.Upstreams {
		uc := upstreamConfig{Name: u.Name, LBPolicy: "round_robin", Instances: make([]instanceConfig, 0, len(u.Instances))}
		for _, i := range u.Instances {
			uc.Instances = append(uc.Instances, instanceConfig{ID: i.ID, Host: i.Host, Port: i.Port, Weight: 1})
		}
		cfg.Upstreams = append(cfg.Upstreams, uc)
	}
	cfg.Routes = make([]routeConfig, 0, len(d.Routes))
	for _, r := range d.Routes {
		methods := r.Methods
		if methods == nil {
			methods = []string{}
		}
		cfg.Routes = append(cfg.Routes, routeConfig{
			ID: r.ID, Hosts: r.Hosts, Path: r.Path, Methods: methods,
			Upstream: r.Upstream, StripPrefix: r.StripPrefix, Priority: r.Priority,
		})
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		return nil, fmt.Errorf("render the Octopus config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("render the Octopus config: %w", err)
	}
	return buf.Bytes(), nil
}
