package planning

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/providerresources"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// ExistingResourceCheck is authorized provider metadata and its access contract.
// Secrets never enter planning input or the saved review.
type ExistingResourceCheck struct {
	Resource *providerresources.ProviderResource
	Endpoint *catalogconnect.Endpoint
	Error    string
	// Unshared is a workspace service the workspace hasn't shared with the
	// project. Nothing about it is checked.
	Unshared bool
}

// WorkspaceProvider is the provider an attached workspace service is
// planned on: none Loomplane runs, since the service runs outside it.
const WorkspaceProvider = "workspace"

// workspaceService reports whether a check is of a workspace service: an
// endpoint registered by its connection string, with no provider resource
// behind it.
func (c ExistingResourceCheck) workspaceService() bool {
	return c.Resource == nil && c.Endpoint != nil && c.Endpoint.Registered()
}

// ExistingResourceConfig pins a non-owning attachment to physical identity and
// an immutable Vault-backed endpoint. It never authorizes provider mutations.
type ExistingResourceConfig struct {
	ResourceID       string `json:"resource_id"`
	EndpointID       string `json:"endpoint_id"`
	Revision         int64  `json:"revision"`
	EndpointRevision int    `json:"endpoint_revision"`
	AccountIdentity  string `json:"account_identity"`
	ExternalID       string `json:"external_id"`
	Incarnation      string `json:"incarnation"`
}

// ExistingResourceBinding recognizes the marker even when its value cannot be
// decoded. Malformed attachment metadata must not fall through to native delete.
func ExistingResourceBinding(b *Binding) bool {
	if b == nil || b.Subject != ResourceSubject {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(b.Config, &fields) != nil {
		return false
	}
	_, found := fields["existing_resource"]
	return found
}

func (p *planner) planExistingResource(name, base string, r manifest.Resource) {
	problem := func(code, message string) {
		p.problem(Finding{Code: code, Subject: ResourceSubject, Name: name, Path: base + ".existing_resource", Message: message})
	}
	if catalogconnect.IsEndpointID(r.ExistingResource) {
		p.planWorkspaceService(name, base, r)
		return
	}
	check := p.in.ExistingResources[r.ExistingResource]
	record := check.Resource
	if check.Error != "" {
		problem("existing-resource-unverified", check.Error)
		return
	}
	if record == nil || record.ID != r.ExistingResource || record.Status == "missing" || !providerresources.AttachableIdentity(record) || record.AccountIdentity == "" || record.ConnectionID == "" {
		problem("existing-resource-unverified", "Sync this resource and verify its physical identity before using it.")
		return
	}
	engine := record.Kind
	if record.Kind == "object-storage" {
		switch record.Provider {
		case "aws":
			engine = catalogconnect.EngineS3
		case "gcp":
			engine = catalogconnect.EngineGCS
		case "cloudflare":
			engine = catalogconnect.EngineR2
			if check.Endpoint != nil && check.Endpoint.Engine == "cloudflare-r2" {
				engine = check.Endpoint.Engine
			}
		}
	}
	if engine == "valkey" {
		engine = "redis"
	}
	requested := r.Kind
	if requested == "valkey" {
		requested = "redis"
	}
	endpoint := check.Endpoint
	expectedKind := record.Kind
	if expectedKind == "valkey" {
		expectedKind = "redis"
	}
	if requested != expectedKind || endpoint == nil || endpoint.ID != record.EndpointID || endpoint.Revision != record.EndpointRevision || endpoint.Engine != engine {
		problem("existing-resource-connection", "Add matching application credentials to this resource before using it.")
		return
	}
	var target *provider.Provider
	for _, candidate := range p.in.Providers.List() {
		if candidate.Cloud != record.Provider || !candidate.Available {
			continue
		}
		if _, ok := candidate.Provisioning(expectedKind); ok {
			target = candidate
			break
		}
	}
	if target == nil {
		problem("existing-resource-unsupported", "This resource has no installed deployment access adapter.")
		return
	}
	if !slices.ContainsFunc(p.in.Connections, func(c ConnectionOption) bool {
		return c.ID == record.ConnectionID && c.Cloud == record.Provider && c.Ready
	}) {
		problem("existing-resource-account", "Check this resource's provider account before using it.")
		return
	}
	id := actionID(ResourceSubject, name)
	old := p.bindings[id]
	if old != nil && !ExistingResourceBinding(old) {
		problem("resource-ownership-change", "Create a separate attachment name. An existing managed resource cannot change lifecycle ownership in place.")
		return
	}
	pin := &ExistingResourceConfig{ResourceID: record.ID, EndpointID: endpoint.ID, Revision: record.Revision, EndpointRevision: endpoint.Revision, AccountIdentity: record.AccountIdentity, ExternalID: record.ExternalID, Incarnation: record.Incarnation}
	cfg := &ResourceConfig{Provider: target.Name, Region: record.Region, Kind: r.Kind, Version: record.Version, AccountIdentity: record.AccountIdentity, ExistingResource: pin}
	if bridge := catalogconnect.APIEndpointBridge(endpoint); bridge != "" {
		cfg.Settings = map[string]string{"project_id": bridge, "output_keys": strings.Join(catalogconnect.APIEndpointOutputs(endpoint), ",")}
	}
	a := &Action{ID: id, Op: Create, Subject: ResourceSubject, Name: name, Provider: target.Name, Region: record.Region, ConnectionID: record.ConnectionID, Engine: provider.ResourceEngine, Creates: "External resource attachment", Resource: cfg, Step: 1, Reason: fmt.Sprintf("Connect %s. Its provider resource remains externally managed.", name)}
	if old != nil {
		a.Changes = changes(old.Config, cfg)
		a.Op = Update
		if len(a.Changes) == 0 && old.ConnectionID == record.ConnectionID && old.Drift == "" {
			a.Op = Keep
			a.Reason = "The external resource attachment is unchanged."
		}
	}
	if record.Kind == "object-storage" && record.Provider == "aws" {
		p.warn(Finding{Code: "external-bucket-logical-identity", Subject: ResourceSubject, Name: name, Path: base + ".existing_resource", Message: "This S3 attachment follows the verified account and bucket name. Recreating that name in the same account preserves its logical identity; no physical bucket incarnation is available."})
	}
	p.resources[name] = a
	p.actions = append(p.actions, a)
}

// planWorkspaceService attaches a workspace service: a database or cache
// registered by its connection string. Its credentials stay in Vault until
// the deploy, pinned to the revision this plan saw; the planner only
// checks the workspace shares it with the project and that it's the
// engine the manifest says.
func (p *planner) planWorkspaceService(name, base string, r manifest.Resource) {
	path := base + ".existing_resource"
	problem := func(code, message string) {
		p.problem(Finding{Code: code, Subject: ResourceSubject, Name: name, Path: path, Message: message})
	}
	check := p.in.ExistingResources[r.ExistingResource]
	endpoint := check.Endpoint
	switch {
	case check.Error != "":
		problem("workspace-service-unavailable", check.Error)
		return
	case check.Unshared:
		label := r.ExistingResource
		if endpoint != nil {
			label = endpoint.Name
		}
		problem("workspace-service-unshared", fmt.Sprintf("%s isn't shared with this project. Ask a workspace admin to share it, or use another service.", label))
		return
	case endpoint == nil || endpoint.ID != r.ExistingResource || !endpoint.Registered():
		problem("workspace-service-unavailable", "This workspace service isn't in the workspace any more. Register it again, or use another service.")
		return
	}
	if want := catalogconnect.EngineForKind(r.Kind); want == "" || want != endpoint.Engine {
		problem("workspace-service-kind", fmt.Sprintf("%s is a %s service, and this resource is %s. Use a %s workspace service, or change kind.", endpoint.Name, catalogconnect.EngineTitle(endpoint.Engine), r.Kind, r.Kind))
		return
	}
	id := actionID(ResourceSubject, name)
	old := p.bindings[id]
	if old != nil && !ExistingResourceBinding(old) {
		problem("resource-ownership-change", "Create a separate attachment name. An existing managed resource cannot change lifecycle ownership in place.")
		return
	}
	pin := &ExistingResourceConfig{EndpointID: endpoint.ID, EndpointRevision: endpoint.Revision}
	cfg := &ResourceConfig{Provider: WorkspaceProvider, Kind: r.Kind, ExistingResource: pin}
	a := &Action{
		ID: id, Op: Create, Subject: ResourceSubject, Name: name, Provider: WorkspaceProvider,
		Engine: provider.ResourceEngine, Creates: "Workspace service attachment", Resource: cfg, Step: 1,
		Reason: fmt.Sprintf("Connect %s to the workspace service %s. It keeps running outside Loomplane.", name, endpoint.Name),
	}
	if old != nil {
		a.Changes = changes(old.Config, cfg)
		a.Op = Update
		a.Reason = fmt.Sprintf("Connect %s to %s at its current credentials.", name, endpoint.Name)
		if len(a.Changes) == 0 && old.Drift == "" {
			a.Op = Keep
			a.Reason = "The workspace service attachment is unchanged."
		}
	}
	p.warn(Finding{Code: "catalog-endpoint-unmanaged", Subject: ResourceSubject, Name: name, Path: path, Message: "Not managed by Loomplane: no backups or restores"})
	if endpoint.TLS == catalogconnect.TLSDisable {
		p.warn(Finding{Code: "workspace-service-plaintext", Subject: ResourceSubject, Name: name, Path: path, Message: fmt.Sprintf("%s is reached without TLS. Use it only on a private network.", endpoint.Name)})
	}
	p.resources[name] = a
	p.actions = append(p.actions, a)
}

func (p *planner) linkExistingResource(name string, a *Action, need, key, path string, dep *Action) {
	if a.Service.Workload == provider.StaticSite || a.Service.Execution == provider.Static {
		p.problem(Finding{Code: "external-resource-runtime-required", Subject: ServiceSubject, Name: name, Path: path, Message: "External resource credentials are available only to a running application, not a static site's build."})
		return
	}
	p.pinExternalResourceTarget(name, a, path)
	if note := ResourceCheckNote(p.cloud(a.Provider)); note != "" {
		p.warn(Finding{Code: "external-resource-check-scope", Subject: ServiceSubject, Name: name, Path: path, Message: note})
	}
	check, ok := p.in.ExistingResourceChecks[name+":"+need]
	if !ok || check.Err != "" || check.Report == nil || len(check.Report.Problems) > 0 || len(check.Report.Missing) > 0 {
		message := "Verify this external resource's credentials and network access from the deployment target."
		if check.Err != "" {
			message = check.Err
		}
		if check.Report != nil && len(check.Report.Problems) > 0 {
			message = check.Report.Problems[0]
		}
		p.problem(Finding{Code: "existing-resource-network", Subject: ServiceSubject, Name: name, Path: path, Message: message})
		return
	}
	if catalogconnect.IsAPIEngine(p.in.ExistingResources[p.in.Manifest.Resources[need].ExistingResource].Endpoint.Engine) || a.Provider == "cloudflare-workers" && p.cloud(dep.Provider) == "cloudflare" {
		p.linkEdgeOutputs(name, a, need, path, dep, edgeResourceKind(dep.Resource), p.in.ExistingResources[p.in.Manifest.Resources[need].ExistingResource].Endpoint)
	} else {
		switch dep.Resource.Kind {
		case "object-storage":
			p.linkExistingBucket(name, a, need, key)
		default:
			p.linkResource(name, a, need, key, path)
		}
	}
	a.DependsOn = append(a.DependsOn, dep.ID)
}
