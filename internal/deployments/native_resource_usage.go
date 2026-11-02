package deployments

import (
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providerresources"
)

// NativeProviderResourceBindings projects only persisted native ownership receipts.
// It never reads credentials, calls a cloud API or adopts inventory by a matching name.
func NativeProviderResourceBindings(b *planning.Binding, c *connections.Connection) []providerresources.ResourceBinding {
	if b == nil || c == nil || c.Status != connections.StatusReady || b.OrgID != c.OrgID || b.ConnectionID != c.ID {
		return nil
	}
	for _, value := range []string{b.ID, b.OrgID, b.ProjectID, b.EnvironmentID, b.Name, b.ConnectionID, b.ExternalID, b.PhysicalID} {
		if value == "" || strings.ContainsAny(value, "\r\n\x00") {
			return nil
		}
	}
	identity := connections.AccountIdentity(c.Cloud, c.Account, c.Details)
	if identity == "" {
		return nil
	}
	if b.Subject == planning.ServiceSubject && b.Provider == "cloudflare-workers" && c.Cloud == connections.Cloudflare {
		return nativeWorkerNamespaceBindings(b, c, identity)
	}
	if b.Subject != planning.ResourceSubject {
		return nil
	}
	var cfg planning.ResourceConfig
	if json.Unmarshal(b.Config, &cfg) != nil || cfg.ExistingResource != nil || cfg.Provider != b.Provider || cfg.Target != c.ID || cfg.AccountIdentity != identity || cfg.TargetSettings != nil && !reflect.DeepEqual(cfg.TargetSettings, planning.ReviewedConnectionSettings(c.Cloud, c.Details)) {
		return nil
	}
	switch {
	case b.Provider == "cloudflare-workers" && c.Cloud == connections.Cloudflare:
		var receipt CloudflareResourceOwnedReceipt
		if json.Unmarshal(b.OwnedResources["cloudflare-resource"], &receipt) != nil || receipt.AccountID != c.Details["account_id"] || receipt.AccountIdentity != identity || receipt.ConnectionID != c.ID || receipt.OrgID != b.OrgID || receipt.ProjectID != b.ProjectID || receipt.EnvironmentID != b.EnvironmentID || receipt.Name != b.Name || receipt.Kind != cfg.Kind || receipt.ResourceID != b.ExternalID || receipt.ConfigDigest != cloudflareConfigurationDigest(b.Name, &cfg) {
			return nil
		}
		kind, err := cloudflareResourceKind(cfg.Kind)
		if err != nil || kind == cloudflare.ResourceDurableObject {
			return nil
		}
		jurisdiction := cloudflareResourceJurisdiction(kind, cfg.Settings)
		if !nativeCloudflareResourceID(kind, b.ExternalID, jurisdiction) {
			return nil
		}
		externalID, incarnation := receipt.ResourceID, receipt.Incarnation
		if kind == cloudflare.ResourceR2 || kind == cloudflare.ResourceVectorize {
			if !nativeCreationTimestamp(incarnation) || b.PhysicalID != cloudflarePhysical(receipt.ResourceID, incarnation) {
				return nil
			}
		} else if incarnation != b.ExternalID || b.PhysicalID != b.ExternalID {
			return nil
		}
		if kind == cloudflare.ResourceR2 {
			externalID = "r2/" + jurisdiction + "/" + receipt.ResourceID
		}
		return []providerresources.ResourceBinding{nativeResourceUsage(b, c, identity, externalID, incarnation)}
	case b.Provider == "vercel" && c.Cloud == connections.Vercel:
		receipt, err := vercelOwnedResource(b)
		if err != nil {
			return nil
		}
		account := c.Details["team_id"]
		if account == "" {
			account = c.Details["user_id"]
		}
		if account == "" {
			return nil
		}
		job := &Job{Connection: c, Deployment: &Deployment{OrgID: b.OrgID, ProjectID: b.ProjectID, EnvironmentID: b.EnvironmentID}}
		if verifyVercelReceipt(job, &cfg, receipt, account, vercelResourceDigest(&cfg, b.Name)) != nil {
			return nil
		}
		return []providerresources.ResourceBinding{nativeResourceUsage(b, c, identity, receipt.ResourceID, receipt.ResourceID)}
	}
	return nil
}

func nativeResourceUsage(b *planning.Binding, c *connections.Connection, identity, externalID, incarnation string) providerresources.ResourceBinding {
	return providerresources.ResourceBinding{ID: b.ID, ProjectID: b.ProjectID, EnvironmentID: b.EnvironmentID, Name: b.Name, Provider: c.Cloud, ConnectionID: c.ID, AccountIdentity: identity, ExternalID: externalID, Incarnation: incarnation, OwnershipVerified: true}
}

func nativeWorkerNamespaceBindings(b *planning.Binding, c *connections.Connection, identity string) []providerresources.ResourceBinding {
	var cfg planning.ServiceConfig
	var receipt CloudflareWorkerOwnedReceipt
	if json.Unmarshal(b.Config, &cfg) != nil || cfg.Provider != b.Provider || cfg.Target != c.ID || cfg.TargetAccountIdentity != identity || cfg.Worker == nil || cfg.TargetSettings != nil && !reflect.DeepEqual(cfg.TargetSettings, planning.ReviewedConnectionSettings(c.Cloud, c.Details)) || json.Unmarshal(b.OwnedResources["cloudflare-worker"], &receipt) != nil {
		return nil
	}
	job := &Job{Deployment: &Deployment{OrgID: b.OrgID, ProjectID: b.ProjectID, EnvironmentID: b.EnvironmentID}, Action: planning.Action{Name: b.Name}}
	if receipt.AccountID != c.Details["account_id"] || receipt.ScriptName != b.ExternalID || receipt.ScriptName != CloudflareWorkerName(job) || !nativeCreationTimestamp(receipt.ScriptCreatedAt) || b.PhysicalID != cloudflarePhysical(receipt.ScriptName, receipt.ScriptCreatedAt) || receipt.OwnerTag != cloudflareOwner(b.OrgID, b.ProjectID, b.EnvironmentID, b.Name) || receipt.ConfigDigest != cloudflareConfigurationDigest(b.Name, &cfg) {
		return nil
	}
	seenIDs, seenClasses := map[string]bool{}, map[string]bool{}
	rows := make([]providerresources.ResourceBinding, 0, len(receipt.Namespaces))
	for _, namespace := range receipt.Namespaces {
		if namespace.CreatedInRelease == "" || strings.ContainsAny(namespace.CreatedInRelease, "\r\n\x00") || seenIDs[namespace.ID] || seenClasses[namespace.ClassName] || !nativeCloudflareResourceID(cloudflare.ResourceDurableObject, namespace.ID, "") || cloudflare.ValidateWorkerBinding(cloudflare.WorkerBinding{Type: "durable_object_namespace", Name: "RESOURCE", ClassName: namespace.ClassName, ScriptName: receipt.ScriptName}) != nil {
			return nil
		}
		seenIDs[namespace.ID], seenClasses[namespace.ClassName] = true, true
		rows = append(rows, nativeResourceUsage(b, c, identity, namespace.ID, namespace.ID))
	}
	return rows
}

func nativeCreationTimestamp(value string) bool {
	if value == "" {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func nativeCloudflareResourceID(kind cloudflare.ResourceKind, id, jurisdiction string) bool {
	binding := cloudflare.WorkerBinding{Name: "RESOURCE"}
	switch kind {
	case cloudflare.ResourceD1:
		binding.Type = "d1"
		binding.ID = id
	case cloudflare.ResourceKV, cloudflare.ResourceQueue, cloudflare.ResourceDurableObject:
		binding.Type = "kv_namespace"
		binding.NamespaceID = id
	case cloudflare.ResourceR2:
		binding.Type = "r2_bucket"
		binding.BucketName = id
		binding.Jurisdiction = jurisdiction
	case cloudflare.ResourceVectorize:
		binding.Type = "vectorize"
		binding.IndexName = id
	case cloudflare.ResourceHyperdrive:
		binding.Type = "hyperdrive"
		binding.ID = id
	default:
		return false
	}
	return cloudflare.ValidateWorkerBinding(binding) == nil
}
