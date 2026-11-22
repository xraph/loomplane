package manageddb

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"

	"github.com/xraph/loomplane/pkg/provider"
)

// CapacityFields lists the creation settings implemented by each resource adapter.
// DigitalOcean publishes the allowed combinations separately for each account and engine.
func CapacityFields(target, kind string) []provider.ResourceSetting {
	choice := func(key, title string, values ...string) provider.ResourceSetting {
		return provider.ResourceSetting{Name: key, Title: title, Options: values, Default: values[0], Required: true, Capacity: true}
	}
	switch target {
	case "digitalocean":
		if kind != "postgres" && kind != "mysql" && kind != "redis" {
			return nil
		}
		return []provider.ResourceSetting{
			{Name: "size", Title: "Size per node", Default: "db-s-1vcpu-1gb", Pattern: "db-[a-z0-9]+(-[a-z0-9]+)+", Required: true, Capacity: true},
			{Name: "nodes", Title: "Total nodes", Default: "1", Pattern: "[1-3]", Required: true, Capacity: true},
		}
	case "render":
		if kind == "postgres" {
			return []provider.ResourceSetting{choice("plan", "Database plan", "basic_256mb", "basic_1gb", "basic_4gb", "pro_4gb", "pro_8gb", "pro_16gb")}
		}
		return []provider.ResourceSetting{choice("plan", "Cache plan", "starter", "standard", "pro", "pro_plus", "pro_max")}
	case "elasticache":
		return []provider.ResourceSetting{choice("node_type", "Cache node type", "cache.t4g.micro", "cache.t4g.small", "cache.t4g.medium")}
	case "memorystore":
		return []provider.ResourceSetting{choice("memory_gib", "Cache memory (GiB)", "1", "2", "4", "8", "16", "32", "64")}
	case "azure-managed-redis":
		return []provider.ResourceSetting{choice("sku", "Cache size", "Balanced_B0", "Balanced_B1", "Balanced_B3", "Balanced_B5", "Balanced_B10")}
	case "azure-database":
		disk := []string{"32", "64", "128", "256", "512"}
		if kind == "mysql" {
			disk = append([]string{"20"}, disk...)
		}
		return []provider.ResourceSetting{choice("sku", "Burstable compute size", "Standard_B1ms", "Standard_B2s", "Standard_B2ms"), choice("storage_gib", "Storage (GiB)", disk...)}
	case "rds":
		return []provider.ResourceSetting{choice("compute_class", "DB instance class", "db.t4g.micro", "db.t4g.small", "db.t4g.medium", "db.t4g.large"), choice("storage_gib", "Storage (GiB)", "20", "50", "100", "200")}
	case "cloud-sql":
		return []provider.ResourceSetting{choice("compute_class", "Machine type", "db-custom-1-3840", "db-custom-2-7680", "db-custom-4-15360", "db-custom-8-30720")}
	}
	return nil
}

// SizedProfile keeps the engine, backup and access contract pinned while checking
// the selected machine and disk against the capacity choices for that profile.
func SizedProfile(p provider.ManagedDatabaseProfile, settings map[string]string) (provider.ManagedDatabaseProfile, error) {
	for _, field := range CapacityFields(p.Provider, p.Kind) {
		value := cmp.Or(settings[field.Name], field.Default)
		if !slices.Contains(field.Options, value) {
			return p, fmt.Errorf("unsupported %s for %s", field.Title, p.Provider)
		}
		switch field.Name {
		case "compute_class":
			p.ComputeClass = value
		case "storage_gib":
			n, err := strconv.ParseInt(value, 10, 32)
			if err != nil {
				return p, err
			}
			p.StorageGB = int32(n)
		}
	}
	return p, nil
}

func MatchesProfile(got, base provider.ManagedDatabaseProfile, settings map[string]string) bool {
	want, err := SizedProfile(base, settings)
	return err == nil && got == want
}
