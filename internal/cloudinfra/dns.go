package cloudinfra

import (
	"context"
	"errors"
	"net"

	"github.com/xraph/loomplane/pkg/provider"
)

func (s *Service) zoneCredential(ctx context.Context, zone *Zone) (provider.DNSAdapter, provider.InfrastructureCredential, error) {
	conn, raw, err := s.Connections.Credential(ctx, zone.OrgID, zone.ConnectionID)
	if err != nil {
		return nil, provider.InfrastructureCredential{}, err
	}
	if conn.OrgID != zone.OrgID || conn.Generation != zone.ConnectionGeneration || conn.Cloud != zone.Provider {
		return nil, provider.InfrastructureCredential{}, ErrConflict
	}
	credential, err := Credential(conn, raw)
	if err != nil {
		return nil, credential, err
	}
	if credential.AccountID != zone.AccountID {
		return nil, credential, provider.ErrInfrastructureIdentity
	}
	adapter, err := s.Adapters.DNS(zone.Provider)
	return adapter, credential, err
}

func routeDNSRecord(zone *Zone, hostname, address, owner string) (provider.DNSRecord, error) {
	relative, err := zoneRelative(hostname, zone.Name)
	if err != nil {
		return provider.DNSRecord{}, err
	}
	ip := net.ParseIP(address)
	if ip == nil {
		return provider.DNSRecord{}, ErrInvalid
	}
	kind := "A"
	if ip.To4() == nil {
		kind = "AAAA"
	}
	token := ""
	if zone.RecordOwnership {
		token = "loomplane:route:" + owner
	}
	return provider.DNSRecord{ZoneID: zone.ProviderZone.ID, Name: relative, Type: kind, Value: ip.String(), TTL: 60, Owner: token}, nil
}

func (s *Service) preflightRouteDNS(ctx context.Context, zone *Zone, desired provider.DNSRecord) error {
	if zone.Mode == "external" {
		return nil
	}
	adapter, cred, err := s.zoneCredential(ctx, zone)
	if err != nil {
		return err
	}
	records, err := adapter.ListRecords(ctx, cred, zone.ProviderZone)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.Name == desired.Name && (record.Type == "A" || record.Type == "AAAA" || record.Type == "CNAME") {
			return provider.ErrInfrastructureDrift
		}
	}
	return nil
}

func (s *Service) applyRouteDNS(ctx context.Context, st DomainStore, op *RouteOperation, zone *Zone) error {
	if zone.Mode == "external" {
		return nil
	}
	adapter, cred, err := s.zoneCredential(ctx, zone)
	if err != nil {
		return err
	}
	if op.Kind == "remove" {
		if op.Receipt == nil {
			return nil
		}
		err = adapter.DeleteRecord(ctx, cred, provider.DNSDeleteRequest{Zone: zone.ProviderZone, Expected: *op.Receipt})
		if errors.Is(err, provider.ErrInfrastructureNotFound) {
			return nil
		}
		return err
	}
	if op.Receipt != nil {
		return nil
	}
	reconcile := op.DNSAttempted
	op.DNSAttempted = true
	if checkErr := st.SaveRouteOperation(ctx, op); checkErr != nil {
		return checkErr
	}
	receipt, err := adapter.UpsertRecord(ctx, cred, provider.DNSUpsertRequest{Zone: zone.ProviderZone, Record: op.DesiredRecord, Expected: op.PreviousReceipt, ReconcileOnly: reconcile})
	if err != nil {
		return err
	}
	op.Receipt = &receipt
	// Persist the returned record version before another update or deletion.
	return st.SaveRouteOperation(ctx, op)
}
