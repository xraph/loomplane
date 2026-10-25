package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"

	"github.com/xraph/loomplane/internal/manageddb"
)

// checkPITRBase is the isolation and capacity contract shared by the inherited
// clone and its prepared recovery network. Backup and endpoint states differ.
func (in *SQLInstance) checkPITRBase(spec SQLInstanceSpec) error {
	if in == nil || in.Kind != "sql#instance" || in.DatabaseVersion != "POSTGRES_17" || spec.DatabaseVersion != in.DatabaseVersion || in.Name != spec.Name || in.Region != spec.Region || in.MasterInstance != "" || len(in.IPAddresses) != 0 {
		return ErrSQLPITRUnavailable
	}
	settings := in.Settings
	version, err := strconv.ParseInt(settings.Version, 10, 64)
	if err != nil || version <= 0 || settings.Edition != "ENTERPRISE" || settings.Availability != "ZONAL" || settings.Tier != spec.Tier || settings.ConnectorEnforcement != "REQUIRED" || !slices.Contains([]string{"", "DATA_API_ACCESS_UNSPECIFIED", "DISALLOW_DATA_API"}, settings.DataAPIAccess) {
		return ErrSQLPITRUnavailable
	}
	disk := spec.Storage
	if disk == nil || disk.SizeGB < 10 || disk.Type != "PD_SSD" || settings.DataDiskSizeGB != int64(disk.SizeGB) || settings.DataDiskType != disk.Type || settings.StorageAutoResize == nil || *settings.StorageAutoResize {
		return ErrSQLPITRUnavailable
	}
	network := settings.IPConfiguration
	if network == nil || network.IPv4Enabled == nil || *network.IPv4Enabled || len(network.AuthorizedNetworks) != 0 || network.PrivateNetwork != "" || network.PrivateGooglePath || network.PSC == nil || !network.PSC.Enabled || network.PSC.NetworkAttachment != "" || network.PSC.AutoPolicy || network.PSC.WriteEndpointDNS {
		return ErrSQLPITRUnavailable
	}
	return nil
}

type sqlPSCEndpoint struct {
	ConsumerProject string `json:"consumerProject"`
	Network         string `json:"consumerNetwork"`
	Status          string `json:"status"`
	NetworkStatus   string `json:"consumerNetworkStatus"`
	DNSStatus       string `json:"instanceAutoDnsStatus"`
	Address         string `json:"ipAddress"`
	Policy          string `json:"serviceConnectionPolicy"`
}

// CheckPITRCopyNetwork returns readiness only for the reviewed consumer network.
// Missing or pending endpoint/DNS status never authorizes a SQL connection.
func (in *SQLInstance) CheckPITRCopyNetwork(spec SQLInstanceSpec, route manageddb.CloudSQLRecoveryNetwork) (bool, error) {
	if !route.Valid() || spec.Intent == "" {
		return false, ErrSQLPITRUnavailable
	}
	if err := in.checkPITRBase(spec); err != nil {
		return false, err
	}
	settings := in.Settings
	if !slices.Contains([]string{"RUNNABLE", "MAINTENANCE"}, in.State) || settings.UserLabels[SQLIntentLabel] != spec.Intent || settings.UserLabels["managed-by"] != "loomplane" || settings.DeletionProtection || settings.Backup == nil || settings.Backup.Enabled || settings.Backup.PITREnabled {
		return false, ErrSQLPITRUnavailable
	}
	psc := settings.IPConfiguration.PSC
	if !psc.AutoDNS || len(psc.AllowedProjects) != 1 || psc.AllowedProjects[0] != route.ConsumerProject || len(psc.AutoConnections) != 1 {
		return false, ErrSQLPITRUnavailable
	}
	var endpoint sqlPSCEndpoint
	if err := json.Unmarshal(psc.AutoConnections[0], &endpoint); err != nil {
		return false, ErrSQLPITRUnavailable
	}
	// A service project override is only meaningful for shared VPC. This
	// protocol uses the host project, including responses that omit the default.
	if endpoint.Network != route.Network || endpoint.ConsumerProject != "" && endpoint.ConsumerProject != route.ConsumerProject || endpoint.Policy != "" {
		return false, ErrSQLPITRUnavailable
	}
	if !slices.Contains([]string{"", "ACTIVE", "PENDING"}, endpoint.Status) || !slices.Contains([]string{"", "VALID"}, endpoint.NetworkStatus) || !slices.Contains([]string{"", "AUTO_DNS_STATUS_UNSPECIFIED", "AUTO_DNS_UNKNOWN", "AUTO_DNS_OK"}, endpoint.DNSStatus) {
		return false, errors.New("cloud sql recovery endpoint or DNS provisioning failed")
	}
	if endpoint.Address != "" {
		address, err := netip.ParseAddr(endpoint.Address)
		if err != nil || !address.Is4() || !address.IsPrivate() {
			return false, ErrSQLPITRUnavailable
		}
	}
	return in.State == "RUNNABLE" && endpoint.Status == "ACTIVE" && endpoint.NetworkStatus == "VALID" && endpoint.DNSStatus == "AUTO_DNS_OK" && endpoint.Address != "", nil
}

// PrepareSQLPITRCopy replaces the clone's inherited consumer allowlist with the
// reviewed recovery VPC. The original CLONE operation remains its ownership
// witness even after this separate UPDATE operation is checkpointed.
func (s *Session) PrepareSQLPITRCopy(ctx context.Context, spec SQLInstanceSpec, physical, cloneOperation string, route manageddb.CloudSQLRecoveryNetwork, track SQLMutation) (*SQLInstance, error) {
	if !route.Valid() || spec.Intent == "" || len(spec.Intent) > 63 || spec.Existing || spec.Inspect == nil || track.Check == nil {
		return nil, ErrSQLPITRUnavailable
	}
	if err := s.checkSQLBackupSource(spec.Region, spec.Name, physical); err != nil {
		return nil, err
	}
	checkPin := func(in *SQLInstance) error {
		pin, err := in.Incarnation(s.key.ProjectID, spec.Region, spec.Name)
		if err != nil {
			return err
		}
		if pin != physical {
			return errors.New("cloud sql PITR copy no longer matches its original creation")
		}
		return nil
	}
	witness := spec
	witness.Inspect = func(in *SQLInstance) error {
		if err := checkPin(in); err != nil {
			return err
		}
		return spec.Inspect(in)
	}
	original, err := s.inspectSQLPITRCopy(ctx, witness, cloneOperation, track.Check, false)
	if err != nil {
		return nil, err
	}
	if original == nil {
		return nil, errors.New("cloud sql PITR copy is absent")
	}
	callerGuard := track.Check
	readCopy := func(ctx context.Context) (*SQLInstance, error) {
		if err := callerGuard(ctx); err != nil {
			return nil, err
		}
		in, err := s.GetSQLInstance(ctx, spec.Name)
		if err != nil {
			return nil, err
		}
		if err := checkPin(in); err != nil {
			return nil, err
		}
		if err := callerGuard(ctx); err != nil {
			return nil, err
		}
		return in, nil
	}
	track.Check = func(ctx context.Context) error {
		in, err := readCopy(ctx)
		if err != nil {
			return err
		}
		// Settings can remain inherited while the captured patch is running.
		// Maintenance changes readiness, not permission to observe that operation.
		inherited := *in
		if inherited.State == "MAINTENANCE" {
			inherited.State = "RUNNABLE"
		}
		if _, isolationErr := inherited.PITRIsolation(spec); isolationErr == nil {
			return nil
		}
		_, err = in.CheckPITRCopyNetwork(spec, route)
		return err
	}
	track.strictIdentity = true
	if err := s.mutateSQL(ctx, spec.Name, "Preparing the private recovery connection", track, func(op *sqlOperation) (bool, error) {
		in, err := readCopy(ctx)
		if err != nil {
			return false, err
		}
		if _, err := in.PITRIsolation(spec); err != nil {
			return false, err
		}
		settings := map[string]any{
			"settingsVersion":           in.Settings.Version,
			"deletionProtectionEnabled": false,
			"userLabels":                map[string]string{"managed-by": "loomplane", SQLIntentLabel: spec.Intent},
			"backupConfiguration":       map[string]bool{"enabled": false, "pointInTimeRecoveryEnabled": false},
			"ipConfiguration": map[string]any{
				"ipv4Enabled": false, "authorizedNetworks": []any{}, "privateNetwork": "", "enablePrivatePathForGoogleCloudServices": false,
				"pscConfig": map[string]any{"pscEnabled": true, "allowedConsumerProjects": []string{route.ConsumerProject}, "networkAttachmentUri": "", "pscAutoConnectionPolicyEnabled": false, "pscAutoDnsEnabled": true, "pscWriteEndpointDnsEnabled": false, "pscAutoConnections": []any{map[string]string{"consumerProject": route.ConsumerProject, "consumerNetwork": route.Network}}},
			},
		}
		if err := callerGuard(ctx); err != nil {
			return false, err
		}
		return false, s.do(ctx, http.MethodPatch, s.sqlPath("/instances/"+url.PathEscape(spec.Name)), map[string]any{"settings": settings}, op)
	}, "UPDATE"); err != nil {
		return nil, err
	}
	for {
		in, err := readCopy(ctx)
		if err != nil {
			return nil, err
		}
		ready, err := in.CheckPITRCopyNetwork(spec, route)
		if err != nil {
			return nil, err
		}
		if ready {
			return in, nil
		}
		if err := s.wait(ctx); err != nil {
			return nil, err
		}
	}
}
