package cloudinfra

import (
	"context"
	"errors"
	"net"
	"slices"

	"github.com/xraph/loomplane/pkg/provider"
)

func (s *Service) zoneCredential(ctx context.Context, zone *Zone) (provider.DNSAdapter, provider.InfrastructureCredential, error) {
	return s.zoneCredentialFor(ctx, zone, false)
}

func (s *Service) zoneCredentialFor(ctx context.Context, zone *Zone, cleanup bool) (provider.DNSAdapter, provider.InfrastructureCredential, error) {
	conn, raw, err := s.Connections.Credential(ctx, zone.OrgID, zone.ConnectionID)
	if err != nil {
		return nil, provider.InfrastructureCredential{}, err
	}
	if conn.OrgID != zone.OrgID || (!cleanup && conn.Generation < zone.ConnectionGeneration) || conn.Cloud != zone.Provider {
		return nil, provider.InfrastructureCredential{}, ErrConflict
	}
	// A renewed credential keeps the zone, but only once the provider shows
	// it the same hosted zone in the same account, as cleanup always checks.
	verify := cleanup || conn.Generation != zone.ConnectionGeneration
	credential, err := Credential(conn, raw)
	if err != nil {
		return nil, credential, err
	}
	if credential.AccountID != zone.AccountID {
		return nil, credential, provider.ErrInfrastructureIdentity
	}
	adapter, err := s.Adapters.DNS(zone.Provider)
	if err == nil && verify {
		current, checkErr := adapter.GetZone(ctx, credential, zone.ProviderZone.Name)
		if checkErr != nil {
			return nil, credential, checkErr
		}
		if current.ID != zone.ProviderZone.ID || current.Name != zone.ProviderZone.Name || current.AccountID != zone.AccountID {
			return nil, credential, provider.ErrInfrastructureIdentity
		}
	}
	return adapter, credential, err
}

func routeDNSRecord(zone *Zone, hostname, address, owner string) (provider.DNSRecord, error) {
	relative, err := zoneRelative(hostname, zone.Name)
	if err != nil {
		return provider.DNSRecord{}, err
	}
	if zone.Mode == "managed" {
		relative, err = zoneRelative(hostname, zone.ProviderZone.Name)
		if err != nil {
			return provider.DNSRecord{}, err
		}
	}
	kind, value := "A", ""
	if ip := net.ParseIP(address); ip != nil {
		value = ip.String()
		if ip.To4() == nil {
			kind = "AAAA"
		}
	} else {
		// A load balancer hostname takes a CNAME, which can't sit at the apex.
		target, hostErr := NormalizeHostname(address)
		if hostErr != nil || relative == "" {
			return provider.DNSRecord{}, ErrInvalid
		}
		kind, value = "CNAME", target
	}
	token := ""
	if zone.RecordOwnership {
		token = routeRecordOwner(owner, 0)
	}
	return provider.DNSRecord{ZoneID: zone.ProviderZone.ID, Name: relative, Type: kind, Value: value, TTL: 60, Owner: token}, nil
}

// preflightRouteDNS refuses records whose names are taken, reading the zone
// once for all of them.
func (s *Service) preflightRouteDNS(ctx context.Context, zone *Zone, desired ...provider.DNSRecord) error {
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
	for _, want := range desired {
		for _, record := range records {
			// A CNAME can't share its name with any other record.
			if record.Name == want.Name && (record.Type == "A" || record.Type == "AAAA" || record.Type == "CNAME" || want.Type == "CNAME") {
				return provider.ErrInfrastructureDrift
			}
		}
	}
	return nil
}

func (s *Service) applyRouteDNS(ctx context.Context, st DomainStore, op *RouteOperation, zone *Zone) error {
	if zone.Mode == "external" {
		return nil
	}
	adapter, cred, err := s.zoneCredentialFor(ctx, zone, op.Kind == "remove")
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

// applyRouteRecords writes each desired record the operation holds no
// receipt for, saving each receipt before the next write so a crash never
// loses one. An external zone's owner writes its records.
func (s *Service) applyRouteRecords(ctx context.Context, st DomainStore, op *RouteOperation, zone *Zone) error {
	if zone.Mode == "external" {
		return nil
	}
	adapter, cred, err := s.zoneCredential(ctx, zone)
	if err != nil {
		return err
	}
	for _, want := range op.desiredRecords() {
		receipts := op.receipts()
		if slices.ContainsFunc(receipts, func(r provider.DNSRecord) bool {
			return r.Name == want.Name && r.Type == want.Type && sameAddress(r.Value, want.Value)
		}) {
			continue
		}
		// DNSAttempted marks a write whose receipt isn't saved yet.
		reconcile := op.DNSAttempted
		op.DNSAttempted = true
		if checkErr := st.SaveRouteOperation(ctx, op); checkErr != nil {
			return checkErr
		}
		receipt, upsertErr := adapter.UpsertRecord(ctx, cred, provider.DNSUpsertRequest{Zone: zone.ProviderZone, Record: want, ReconcileOnly: reconcile, Siblings: receipts})
		if upsertErr != nil {
			return upsertErr
		}
		op.Receipts = append(slices.Clone(receipts), receipt)
		op.DNSAttempted = false
		if checkErr := st.SaveRouteOperation(ctx, op); checkErr != nil {
			return checkErr
		}
	}
	return nil
}

// deleteRouteRecords deletes every record the operation holds a receipt
// for. One already gone counts as deleted.
func (s *Service) deleteRouteRecords(ctx context.Context, op *RouteOperation, zone *Zone) error {
	receipts := op.receipts()
	if zone.Mode == "external" || len(receipts) == 0 {
		return nil
	}
	adapter, cred, err := s.zoneCredentialFor(ctx, zone, true)
	if err != nil {
		return err
	}
	for _, receipt := range receipts {
		deleteErr := adapter.DeleteRecord(ctx, cred, provider.DNSDeleteRequest{Zone: zone.ProviderZone, Expected: receipt})
		if deleteErr != nil && !errors.Is(deleteErr, provider.ErrInfrastructureNotFound) {
			return deleteErr
		}
	}
	return nil
}
