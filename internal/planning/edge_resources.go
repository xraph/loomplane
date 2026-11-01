package planning

import (
	"encoding/json"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

func (p *planner) providerResourceSettings(name, base string, r manifest.Resource, offer provider.Provisions) map[string]string {
	if offer.ImportOnly {
		p.problem(Finding{Code: "resource-worker-lifecycle", Subject: ResourceSubject, Name: name, Path: base, Message: "Create this resource through its Worker class declaration, then sync the namespace to attach it."})
	}
	values := map[string]string{}
	for _, key := range slices.Sorted(maps.Keys(r.Settings)) {
		if !slices.ContainsFunc(offer.Settings, func(s provider.ResourceSetting) bool { return s.Name == key }) {
			p.problem(Finding{Code: "catalog-setting-unknown", Subject: ResourceSubject, Name: name, Path: base + ".settings." + key, Message: name + " has no setting " + key + "."})
		}
	}
	for _, s := range offer.Settings {
		value := r.Settings[s.Name]
		if value == "" {
			value = s.Default
		}
		if value == "" && !s.Required {
			continue
		}
		pattern, err := regexp.Compile("^(?:" + s.Pattern + ")$")
		if err != nil || value == "" || !pattern.MatchString(value) {
			p.problem(Finding{Code: "resource-setting-invalid", Subject: ResourceSubject, Name: name, Path: base + ".settings." + s.Name, Message: "Supply a valid " + s.Title + " for " + name + "."})
			continue
		}
		if r.Kind == "vercel-integration" && s.Name == "metadata" {
			metadata, err := vercel.ParseProvisioningMetadata(value)
			if err != nil {
				p.problem(Finding{Code: "resource-setting-invalid", Subject: ResourceSubject, Name: name, Path: base + ".settings.metadata", Message: "Use a flat JSON object of non-secret provisioning options."})
				continue
			}
			canonical, err := json.Marshal(metadata)
			if err != nil {
				p.problem(Finding{Code: "resource-setting-invalid", Subject: ResourceSubject, Name: name, Path: base + ".settings.metadata", Message: "Provisioning options could not be reviewed."})
				continue
			}
			value = string(canonical)
		}
		values[s.Name] = value
	}
	if len(values) == 0 {
		return nil
	}
	return values
}

func (p *planner) workerSettings(name, base string, set manifest.Settings, prov *provider.Provider) *provider.WorkerSettings {
	if prov.Name != "cloudflare-workers" {
		if set.Worker != nil {
			p.problem(Finding{Code: "worker-target-required", Subject: ServiceSubject, Name: name, Path: base + ".worker", Message: "Worker module settings require Cloudflare Workers."})
		}
		return nil
	}
	cfg := provider.WorkerSettings{Main: "worker.js", CompatibilityDate: "2026-11-01"}
	if set.Worker != nil {
		cfg = *set.Worker
		if cfg.Main == "" {
			cfg.Main = "worker.js"
		}
		if cfg.CompatibilityDate == "" {
			cfg.CompatibilityDate = "2026-11-01"
		}
		cfg.CompatibilityFlags = slices.Clone(cfg.CompatibilityFlags)
		cfg.DurableObjects = slices.Clone(cfg.DurableObjects)
	}
	_, dateErr := time.Parse("2006-01-02", cfg.CompatibilityDate)
	if path.IsAbs(cfg.Main) || path.Clean(cfg.Main) != cfg.Main || strings.Contains(cfg.Main, "\\") || strings.HasPrefix(cfg.Main, "../") || (!strings.HasSuffix(cfg.Main, ".js") && !strings.HasSuffix(cfg.Main, ".mjs")) || dateErr != nil {
		p.problem(Finding{Code: "worker-module-invalid", Subject: ServiceSubject, Name: name, Path: base + ".worker", Message: "Choose a source-relative prebundled .js or .mjs module and a valid compatibility date."})
		return nil
	}
	names := map[string]bool{}
	valid := regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]{0,127}$`)
	for _, c := range cfg.DurableObjects {
		if !valid.MatchString(c.Name) || !valid.MatchString(c.ClassName) || names[c.Name] {
			p.problem(Finding{Code: "worker-class-invalid", Subject: ServiceSubject, Name: name, Path: base + ".worker.durable_objects", Message: "Durable Object classes need distinct binding names and exported JavaScript class names."})
		}
		names[c.Name] = true
	}
	return &cfg
}

func (p *planner) configureEdgeResource(name, base string, r manifest.Resource, cfg *ResourceConfig, a *Action, prov *provider.Provider) {
	if prov.Cloud != "cloudflare" && prov.Cloud != "vercel" {
		return
	}
	target := r.For(p.in.Environment, p.in.Manifest.Defaults).Target
	if target == "" {
		target = p.in.Chosen[prov.Cloud]
	}
	if target == "" {
		if c, ok := p.connection(prov.Cloud); ok {
			target = c.ID
		}
	}
	c := p.targetConnection(name, base+".target", "target", prov.Cloud, target)
	if c == nil {
		return
	}
	cfg.Target, cfg.TargetSettings, cfg.AccountIdentity = c.ID, ReviewedConnectionSettings(c.Cloud, c.Details), connections.AccountIdentity(c.Cloud, c.Account, c.Details)
	for _, setting := range []string{"origin_endpoint", "application_endpoint"} {
		id := cfg.Settings[setting]
		if id == "" {
			continue
		}
		pin, found := p.in.ResourceEndpoints[id]
		if !found {
			p.problem(Finding{Code: "resource-endpoint-unverified", Subject: ResourceSubject, Name: name, Path: base + ".settings." + setting, Message: "Review this application's current credential endpoint with secret management access before creating the resource."})
			continue
		}
		if cfg.EndpointPins == nil {
			cfg.EndpointPins = map[string]EndpointPin{}
		}
		cfg.EndpointPins[setting] = pin
	}
	a.ConnectionID = c.ID
	if cfg.AccountIdentity == "" {
		p.problem(Finding{Code: "resource-account-unverified", Subject: ResourceSubject, Name: name, Path: base + ".target", Message: "Check this provider account before creating resources."})
	}
}

func edgeResourceKind(cfg *ResourceConfig) string {
	if cfg == nil {
		return ""
	}
	if cfg.Kind == "object-storage" && cfg.Provider == "cloudflare-workers" {
		return "cloudflare-r2"
	}
	return cfg.Kind
}

// linkEdgeResource handles managed provider outputs. Imported resources still
// pass through their project grant and endpoint verification path.
func (p *planner) linkEdgeResource(name string, a *Action, need, base string, dep *Action) bool {
	if dep.Resource == nil || dep.Resource.ExistingResource != nil {
		return false
	}
	kind := edgeResourceKind(dep.Resource)
	if !catalogconnect.IsAPIEngine(kind) {
		return false
	}
	p.pinExternalResourceTarget(name, a, base)
	p.linkEdgeOutputs(name, a, need, base, dep, kind, nil)
	a.DependsOn = append(a.DependsOn, dep.ID)
	return true
}

func (p *planner) linkEdgeOutputs(name string, a *Action, need, base string, dep *Action, kind string, endpoint *catalogconnect.Endpoint) {
	if a.Service.Workload == provider.StaticSite || a.Service.Execution == provider.Static {
		p.problem(Finding{Code: "resource-runtime-required", Subject: ServiceSubject, Name: name, Path: base, Message: "Resource access is available only to a running application."})
		return
	}
	if strings.HasPrefix(kind, "cloudflare-") && a.Provider == "cloudflare-workers" {
		if a.Service.TargetAccountIdentity == "" || a.Service.TargetAccountIdentity != dep.Resource.AccountIdentity {
			p.problem(Finding{Code: "resource-across-accounts", Subject: ServiceSubject, Name: name, Path: base, Message: "Cloudflare native bindings require the Worker and resource in the same reviewed account."})
			return
		}
		a.Service.Env = append(a.Service.Env, EnvLink{Key: envName(need), From: need, Subject: ResourceSubject, Field: "output:CLOUDFLARE_BINDING"})
		return
	}
	if strings.HasPrefix(kind, "cloudflare-") && (kind == "cloudflare-hyperdrive" || kind == "cloudflare-durable-object" || endpoint != nil && endpoint.Username == "native-binding" || endpoint == nil && dep.Resource.Settings["application_endpoint"] == "") {
		p.problem(Finding{Code: "resource-native-binding-required", Subject: ServiceSubject, Name: name, Path: base, Message: "Use this resource through a Cloudflare Worker native binding, or configure verified application API access for a portable resource."})
		return
	}
	keys := catalogconnect.APIEnvironment(kind)
	if kind == "cloudflare-r2" {
		keys = catalogconnect.BucketEnvironment(catalogconnect.EngineR2)
	}
	if kind == "vercel-integration" {
		keys = catalogconnect.APIEndpointOutputs(endpoint)
		if endpoint == nil {
			keys = strings.Split(dep.Resource.Settings["output_keys"], ",")
			if len(keys) == 1 && keys[0] == "" {
				keys = []string{"DATABASE_URL"}
			}
		}
	}

	for _, key := range keys {
		if key == "CLOUDFLARE_BINDING" {
			continue
		}
		a.Service.Env = append(a.Service.Env, EnvLink{Key: edgeOutputKey(a, need, key), From: need, Subject: ResourceSubject, Field: "output:" + key})
	}
}

func edgeOutputKey(a *Action, need, key string) string {
	collision := false
	for i := range a.Service.Env {
		e := &a.Service.Env[i]
		if e.Subject == ResourceSubject && e.From != need && (e.Key == key || e.Field == "output:"+key) {
			e.Key = envName(e.From) + "_" + key
			collision = true
		}
	}
	if collision {
		return envName(need) + "_" + key
	}
	return key
}
