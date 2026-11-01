package planning

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/manageddb"
	plan "github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providerresources"
)

type resourceUsageConnections interface {
	All(context.Context, string) ([]*connections.Connection, error)
}

// providerResourceUsage projects real deployment bindings without reading Vault.
func providerResourceUsage(db *pgdriver.PgDB, conns resourceUsageConnections) func(context.Context, string) ([]providerresources.ResourceBinding, error) {
	return func(ctx context.Context, org string) ([]providerresources.ResourceBinding, error) {
		if db == nil || conns == nil || org == "" {
			return nil, errors.New("resource deployment usage is unavailable")
		}
		connectionRows, err := conns.All(ctx, org)
		if err != nil {
			return nil, errors.New("resource deployment accounts could not be read")
		}
		accounts := map[string]*connections.Connection{}
		for _, c := range connectionRows {
			if c != nil && c.OrgID == org {
				accounts[c.ID] = c
			}
		}
		rows, err := db.Query(ctx, `SELECT id,org_id,project_id,environment_id,subject,name,provider,region,external_id,physical_id,connection_id,plan_id,config FROM loomplane_bindings WHERE org_id=$1 AND subject='resource' ORDER BY id`, org)
		if err != nil {
			return nil, errors.New("resource deployment usage could not be read")
		}
		defer rows.Close()
		result := []providerresources.ResourceBinding{}
		for rows.Next() {
			var b plan.Binding
			if err := rows.Scan(&b.ID, &b.OrgID, &b.ProjectID, &b.EnvironmentID, &b.Subject, &b.Name, &b.Provider, &b.Region, &b.ExternalID, &b.PhysicalID, &b.ConnectionID, &b.PlanID, &b.Config); err != nil {
				return nil, errors.New("resource deployment usage could not be read")
			}
			if b.OrgID != org {
				continue
			}
			if binding, ok := providerResourceBinding(&b, accounts[b.ConnectionID]); ok {
				result = append(result, binding)
			}
		}
		if err := rows.Err(); err != nil {
			return nil, errors.New("resource deployment usage could not be read")
		}
		return result, nil
	}
}

func providerResourceBinding(b *plan.Binding, c *connections.Connection) (providerresources.ResourceBinding, bool) {
	var out providerresources.ResourceBinding
	if b == nil || b.Subject != plan.ResourceSubject || b.ID == "" || b.OrgID == "" || b.ProjectID == "" || b.EnvironmentID == "" {
		return out, false
	}
	var cfg plan.ResourceConfig
	if json.Unmarshal(b.Config, &cfg) != nil {
		return out, false
	}
	cloud := resourceBindingCloud(b.Provider)
	out = providerresources.ResourceBinding{ID: b.ID, ProjectID: b.ProjectID, EnvironmentID: b.EnvironmentID, Name: b.Name, Provider: cloud, ConnectionID: b.ConnectionID}
	if plan.ExistingResourceBinding(b) {
		pin := cfg.ExistingResource
		if pin == nil || pin.ResourceID == "" {
			return out, false
		}
		out.InventoryResourceID, out.AccountIdentity, out.ExternalID, out.Incarnation = pin.ResourceID, pin.AccountIdentity, pin.ExternalID, pin.Incarnation
		out.Revision, out.EndpointID, out.EndpointRevision = pin.Revision, pin.EndpointID, pin.EndpointRevision
		return out, true
	}
	if cloud == "" || c == nil || c.ID != b.ConnectionID || c.OrgID != b.OrgID || c.Cloud != cloud || (cfg.Provider != "" && cfg.Provider != b.Provider) {
		return out, false
	}
	identity := connections.AccountIdentity(c.Cloud, c.Account, c.Details)
	if identity == "" || (cfg.AccountIdentity != "" && cfg.AccountIdentity != identity) || (cfg.Managed != nil && (cfg.Managed.ConnectionID != c.ID || cfg.Managed.AccountIdentity != identity)) {
		return out, false
	}
	out.AccountIdentity = providerresources.AccountIdentity(c)
	validName := func(s string) bool {
		return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\:?#%\x00\r\n\t ")
	}
	region := b.Region
	partition := "aws"
	if strings.HasPrefix(region, "cn-") {
		partition = "aws-cn"
	}
	if strings.HasPrefix(region, "us-gov-") {
		partition = "aws-us-gov"
	}
	switch b.Provider {
	case "digitalocean":
		id, ok := strings.CutPrefix(b.ExternalID, "db/")
		if !ok || !validName(id) {
			return out, false
		}
		out.ExternalID = id
	case "azure-database", "azure-managed-redis":
		group := c.Details["resource_group"]
		if group == "" {
			group = azure.DefaultResourceGroup
		}
		prefix := "/subscriptions/" + c.Details["subscription_id"] + "/resourceGroups/" + group + "/providers/"
		collection := "Microsoft.Cache/redisEnterprise/"
		if b.Provider == "azure-database" {
			switch cfg.Kind {
			case "postgres":
				collection = "Microsoft.DBforPostgreSQL/flexibleServers/"
			case "mysql":
				collection = "Microsoft.DBforMySQL/flexibleServers/"
			default:
				return out, false
			}
		}
		full := prefix + collection
		if !strings.HasPrefix(strings.ToLower(b.ExternalID), strings.ToLower(full)) || !validName(b.ExternalID[len(full):]) {
			return out, false
		}
		out.ExternalID = strings.ToLower(b.ExternalID)
	case "rds", "elasticache", "s3":
		if region == "" || region != c.Details["region"] || !validName(b.ExternalID) {
			return out, false
		}
		switch b.Provider {
		case "rds":
			out.ExternalID = "arn:" + partition + ":rds:" + region + ":" + c.Account + ":db:" + b.ExternalID
			if validName(b.PhysicalID) && strings.HasPrefix(b.PhysicalID, "db-") {
				out.Incarnation = b.PhysicalID
			}
		case "elasticache":
			out.ExternalID = "arn:" + partition + ":elasticache:" + region + ":" + c.Account + ":replicationgroup:" + b.ExternalID
		case "s3":
			out.ExternalID = "arn:" + partition + ":s3:::" + b.ExternalID
		}
	case "cloud-sql":
		parts := strings.Split(b.ExternalID, ":")
		if len(parts) < 3 || strings.Join(parts[:len(parts)-2], ":") != c.Details["project_id"] || parts[len(parts)-2] != region || !validName(parts[len(parts)-1]) {
			return out, false
		}
		out.ExternalID = "projects/" + c.Details["project_id"] + "/instances/" + parts[len(parts)-1]
		if suffix, ok := strings.CutPrefix(b.PhysicalID, "cloud-sql:"); ok && len(suffix) == 64 {
			if _, err := hex.DecodeString(suffix); err == nil {
				out.Incarnation = b.PhysicalID
			}
		}
	case "memorystore":
		parts := strings.Split(b.ExternalID, "/")
		if len(parts) != 2 || parts[0] != region || !validName(parts[0]) || !validName(parts[1]) {
			return out, false
		}
		out.ExternalID = "projects/" + c.Details["project_id"] + "/locations/" + parts[0] + "/instances/" + parts[1]
	case "cloud-storage":
		if !validName(b.ExternalID) {
			return out, false
		}
		out.ExternalID = "projects/" + c.Details["project_id"] + "/buckets/" + b.ExternalID
	case "render":
		parts := strings.Split(b.ExternalID, "/")
		if len(parts) != 2 || (parts[0] != "postgres" && parts[0] != "key-value") || !validName(parts[1]) {
			return out, false
		}
		out.ExternalID = parts[1]
	default:
		return out, false
	}
	if (b.Provider == "rds" || b.Provider == "cloud-sql") && b.PlanID != "" && out.Incarnation != "" && cfg.Managed != nil && cfg.Region == b.Region {
		profile, err := manageddb.PostgresSource(b.Provider, cfg.Version, cfg.Region, cfg.Managed.CloudSQLPSC)
		out.OwnershipVerified = err == nil && cfg.Kind == "postgres" && cfg.Managed.Profile == *profile
	}
	return out, true
}

func resourceBindingCloud(provider string) string {
	switch provider {
	case "digitalocean", "render":
		return provider
	case "azure-database", "azure-managed-redis":
		return connections.Azure
	case "rds", "elasticache", "s3":
		return connections.AWS
	case "cloud-sql", "memorystore", "cloud-storage":
		return connections.GCP
	}
	return ""
}
