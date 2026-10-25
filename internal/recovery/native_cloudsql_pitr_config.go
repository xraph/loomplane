package recovery

import (
	"encoding/json"
	"strings"

	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/planning"
)

func nativeCloudSQLPITRCopyConfig(source planning.Binding, point NativePoint, target NativeRestoreTarget) (*planning.ResourceConfig, error) {
	var cfg planning.ResourceConfig
	profile, err := manageddb.CloudSQLPSCPostgres("17")
	instance := strings.Split(source.ExternalID, ":")
	if err != nil || source.Subject != planning.ResourceSubject || source.Provider != "cloud-sql" || source.PhysicalID == "" || source.ConnectionID == "" || json.Unmarshal(source.Config, &cfg) != nil || cfg.Provider != source.Provider || cfg.Kind != "postgres" || cfg.Version != "17" || cfg.Region != source.Region || cfg.Managed == nil || cfg.Managed.Profile != *profile || cfg.Managed.ConnectionID != source.ConnectionID || cfg.Managed.AccountIdentity == "" || !cfg.Managed.CloudSQLPSC.Valid(source.Region) || len(instance) != 3 || instance[0] == "" || instance[1] != source.Region || instance[2] != point.Name || point.Name == "" || point.ProviderID != source.PhysicalID || point.Kind != "pitr" || point.Engine != "postgres" || point.Version != "17" || !point.Encrypted || point.CreatedAt.IsZero() || point.Status != "available" || target.Provider != "cloud-sql" || target.ConnectionID == "" || target.AccountIdentity != cfg.Managed.AccountIdentity || target.NativeProject != instance[0] || target.NativeAccount != "" || target.Region != source.Region || !target.CloudSQLNetwork.Valid() || target.CloudSQLNetwork.Network == cfg.Managed.CloudSQLPSC.Network {
		return nil, ErrInvalid
	}
	if _, err := point.SelectTime("cloud-sql", point.RestoreTime); err != nil {
		return nil, ErrInvalid
	}
	return &cfg, nil
}
