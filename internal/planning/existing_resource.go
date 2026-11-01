package planning

import (
	"encoding/json"
	"fmt"
	"slices"

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
	if dep.Resource.Kind == "object-storage" {
		p.linkExistingBucket(name, a, need, key)
	} else {
		p.linkResource(name, a, need, key, path)
	}
	a.DependsOn = append(a.DependsOn, dep.ID)
}
