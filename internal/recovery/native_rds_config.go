package recovery

import (
	"encoding/json"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws/arn"

	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/planning"
)

func nativeRDSCopyConfig(source planning.Binding, point NativePoint, target NativeRestoreTarget) (*planning.ResourceConfig, error) {
	var cfg planning.ResourceConfig
	profile, err := manageddb.Postgres("rds", "17")
	if err != nil || source.Subject != planning.ResourceSubject || source.Provider != "rds" || source.PhysicalID == "" || source.ExternalID == "" || json.Unmarshal(source.Config, &cfg) != nil || cfg.Provider != source.Provider || cfg.Kind != "postgres" || cfg.Version != "17" || cfg.Region != source.Region || cfg.Managed == nil || cfg.Managed.Profile != *profile || cfg.Managed.ConnectionID != source.ConnectionID || cfg.Managed.AccountIdentity == "" || point.Engine != "postgres" || (point.Version != "17" && !strings.HasPrefix(point.Version, "17.")) || !point.Encrypted || point.KMSKey == "" || point.CreatedAt.IsZero() || (point.Kind != "manual" && point.Kind != "automated" && point.Kind != "pitr") || target.Provider != "rds" || target.ConnectionID == "" || target.AccountIdentity != cfg.Managed.AccountIdentity || target.Region != source.Region || target.NativeAccount == "" || target.NativeProject != "" || !target.RDSNetwork.Valid() {
		return nil, ErrInvalid
	}
	resource := "snapshot:" + point.Name
	if point.Kind == "pitr" {
		if _, selectionErr := point.SelectTime("rds", point.RestoreTime); selectionErr != nil || point.Name != source.ExternalID {
			return nil, ErrInvalid
		}
		resource = "db:" + source.ExternalID
	} else if point.PITR != nil || point.RestoreTime != nil {
		return nil, ErrInvalid
	}
	selected, err := arn.Parse(point.ProviderID)
	if err != nil || selected.Partition != "aws" || selected.Service != "rds" || selected.Region != target.Region || selected.AccountID != target.NativeAccount || selected.Resource != resource {
		return nil, ErrInvalid
	}
	return &cfg, nil
}
