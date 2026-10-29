package cloudinfra

import (
	"context"
	"errors"
	"fmt"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/provider"
)

func (s *Service) reconcileRegistryDNS(ctx context.Context, st RegistryStore, state *RegistryState, connection *connections.Connection, remove bool) error {
	if remove && state.DNS == nil {
		return nil
	}
	domains, err := s.domainStore()
	if err != nil {
		return err
	}
	zones, err := domains.Zones(ctx, state.OrgID)
	if err != nil {
		return err
	}
	LinkZoneParents(zones)
	var zone *Zone
	for _, candidate := range zones {
		if state.DNS != nil {
			if candidate.ID == state.DNS.ZoneID {
				zone = candidate
			}
			continue
		}
		if candidate.Mode != "managed" || len(candidate.ProjectIDs) != 0 {
			continue
		}
		if _, relativeErr := zoneRelative(state.Hostname, candidate.Name); relativeErr != nil {
			continue
		}
		if zone == nil || len(candidate.Name) > len(zone.Name) {
			zone = candidate
		}
	}
	if zone == nil {
		if remove && state.DNS == nil {
			return nil
		}
		return fmt.Errorf("%w: connect and verify an organization DNS zone for %s", ErrPending, state.Hostname)
	}
	if !remove {
		if zone.Mode != "managed" || len(zone.ProjectIDs) != 0 {
			return ErrConflict
		}
		if err = s.verifyZoneControl(ctx, zone); err != nil {
			return fmt.Errorf("%w: verify the registry's DNS zone before automation can use it", ErrPending)
		}
	}
	adapter, credential, err := s.zoneCredentialFor(ctx, zone, remove)
	if err != nil {
		return err
	}
	if state.DNS == nil {
		desired, buildErr := routeDNSRecord(zone, state.Hostname, connection.Details["ingress_address"], "registry-"+state.ConnectionID)
		if buildErr != nil {
			return buildErr
		}
		if operationErr := s.preflightRouteDNS(ctx, zone, desired); operationErr != nil {
			return operationErr
		}
		state.DNS = &RegistryDNS{ZoneID: zone.ID, ZoneRevision: zone.Revision, Desired: desired}
		if operationErr := st.SaveRegistryState(ctx, state); operationErr != nil {
			return operationErr
		}
	}
	dns := state.DNS
	// Resolve an uncertain write before selecting a different destination or
	// deleting anything. Provider adapters refuse adoption without ownership proof.
	if dns.Attempted {
		receipt, reconcileErr := adapter.UpsertRecord(ctx, credential, provider.DNSUpsertRequest{Zone: zone.ProviderZone, Record: dns.Desired, Expected: dns.Previous, ReconcileOnly: true})
		if reconcileErr != nil {
			return reconcileErr
		}
		dns.Receipt = &receipt
		dns.Previous = nil
		dns.Attempted = false
		if operationErr := st.SaveRegistryState(ctx, state); operationErr != nil {
			return operationErr
		}
	}
	if remove {
		if dns.Receipt != nil {
			err = adapter.DeleteRecord(ctx, credential, provider.DNSDeleteRequest{Zone: zone.ProviderZone, Expected: *dns.Receipt})
			if err != nil && !errors.Is(err, provider.ErrInfrastructureNotFound) {
				return err
			}
		}
		state.DNS = nil
		return st.SaveRegistryState(ctx, state)
	}
	desired, err := routeDNSRecord(zone, state.Hostname, connection.Details["ingress_address"], "registry-"+state.ConnectionID)
	if err != nil {
		return err
	}
	if dns.Receipt != nil && dns.Desired == desired {
		records, listErr := adapter.ListRecords(ctx, credential, zone.ProviderZone)
		if listErr != nil {
			return listErr
		}
		for _, record := range records {
			if record.ID == dns.Receipt.ID {
				return provider.MatchDNSPrior(*dns.Receipt, record)
			}
		}
		return provider.ErrInfrastructureDrift
	}
	dns.Previous = dns.Receipt
	dns.Receipt = nil
	dns.Desired = desired
	dns.Attempted = true
	dns.ZoneRevision = zone.Revision
	if operationErr := st.SaveRegistryState(ctx, state); operationErr != nil {
		return operationErr
	}
	receipt, err := adapter.UpsertRecord(ctx, credential, provider.DNSUpsertRequest{Zone: zone.ProviderZone, Record: desired, Expected: dns.Previous})
	if err != nil {
		return err
	}
	dns.Receipt = &receipt
	dns.Previous = nil
	dns.Attempted = false
	return st.SaveRegistryState(ctx, state)
}
