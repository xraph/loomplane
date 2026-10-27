package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/provider"
)

// A managed zone's connection can write DNS in its hosting zone, so
// Loomplane publishes the TXT proof there itself. Verification still reads
// public DNS: a hosting zone that isn't authoritative for the name never
// shows the record, and the zone stays unverified.

// zoneProofTTL keeps a renewed value quick to reach resolvers. It is the
// route records' TTL and every supported provider's minimum or above.
const zoneProofTTL = 60

// zoneProofCheckEvery spaces background checks of one zone.
const zoneProofCheckEvery = 2 * time.Minute

// zoneProofBatch bounds the zones one background pass checks.
const zoneProofBatch = 50

// zoneProofOwner marks the record as this zone's on providers that keep an
// owner token. DigitalOcean keeps none, so there the receipt alone is proof.
func zoneProofOwner(zone *Zone) string {
	if !zone.RecordOwnership {
		return ""
	}
	return "loomplane:zone-proof:" + zone.ID
}

// zoneProofRecord is the record the zone's current proof needs, named
// relative to the hosting zone, which may be an ancestor of the zone.
func zoneProofRecord(zone *Zone) (provider.DNSRecord, error) {
	name, err := zoneRelative(zone.ProofName, zone.ProviderZone.Name)
	if err != nil {
		return provider.DNSRecord{}, err
	}
	record := provider.DNSRecord{ZoneID: zone.ProviderZone.ID, Name: name, Type: "TXT", Value: zone.ProofValue, TTL: zoneProofTTL, Owner: zoneProofOwner(zone)}
	if zone.ProofRecord != nil {
		// An update names the exact record the receipt holds.
		record.ID = zone.ProofRecord.ID
	}
	return record, nil
}

// proofPublished is a managed zone whose current proof value is written,
// with its receipt saved.
func (z *Zone) proofPublished() bool {
	return z.ProofRecord != nil && !z.ProofPublishAttempted && z.ProofRecord.Value == z.ProofValue
}

// publishZoneProof writes or updates a managed zone's TXT proof through its
// own connection. The attempt is saved before the write, so an answer lost
// on the way back is only ever reconciled, never written again, and the
// receipt is saved before anything treats the proof as published. A
// provider failure is recorded on the zone as ProofPublishError and isn't
// returned: the zone still exists, and the next check tries again. Only a
// failed save is returned.
func (s *Service) publishZoneProof(ctx context.Context, st DomainStore, zone *Zone) error {
	if zone.Mode != "managed" || !zone.hasControlProof() || zone.proofPublished() {
		return nil
	}
	desired, err := zoneProofRecord(zone)
	if err != nil {
		return s.proofPublishFailed(ctx, st, zone, err)
	}
	adapter, cred, err := s.zoneCredential(ctx, zone)
	if err != nil {
		return s.proofPublishFailed(ctx, st, zone, err)
	}
	attempted := zone.ProofPublishAttempted
	reconcile := attempted
	if reconcile && zone.ProofRecord == nil {
		// A create whose answer was lost. A duplicate proof TXT is harmless,
		// unlike a route record, so when the provider definitely shows
		// nothing of ours at the name, this attempt writes it again.
		records, listErr := adapter.ListRecords(ctx, cred, zone.ProviderZone)
		if listErr != nil {
			// Unknown still: stay reconcile-only.
			return s.proofPublishFailed(ctx, st, zone, errors.Join(provider.ErrInfrastructureAmbiguous, listErr))
		}
		reconcile = proofCreateLanded(records, desired)
	}
	zone.ProofPublishAttempted = true
	if err = st.SaveZone(ctx, zone); err != nil {
		zone.ProofPublishAttempted = attempted
		return err
	}
	receipt, err := adapter.UpsertRecord(ctx, cred, provider.DNSUpsertRequest{Zone: zone.ProviderZone, Record: desired, Expected: zone.ProofRecord, ReconcileOnly: reconcile})
	if err != nil {
		return s.proofPublishFailed(ctx, st, zone, err)
	}
	zone.ProofRecord, zone.ProofPublishAttempted, zone.ProofPublishError = &receipt, false, ""
	return st.SaveZone(ctx, zone)
}

// proofCreateLanded reports whether a create of desired may be at the
// provider: a TXT record at its name that carries its owner token or, on a
// provider without owner tokens, its value. Reconciling settles which.
func proofCreateLanded(records []provider.DNSRecord, desired provider.DNSRecord) bool {
	for _, r := range records {
		if r.Name != desired.Name || r.Type != desired.Type {
			continue
		}
		if (desired.Owner != "" && r.Owner == desired.Owner) || (desired.Owner == "" && r.Value == desired.Value) {
			return true
		}
	}
	return false
}

// proofPublishFailed records why the proof isn't published. Only an
// ambiguous write keeps the attempt open: anything else was refused before
// a write, so the next try may write.
func (s *Service) proofPublishFailed(ctx context.Context, st DomainStore, zone *Zone, err error) error {
	if !errors.Is(err, provider.ErrInfrastructureAmbiguous) {
		zone.ProofPublishAttempted = false
	}
	zone.ProofPublishError = proofPublishMessage(zone, err)
	return st.SaveZone(ctx, zone)
}

func proofPublishMessage(zone *Zone, err error) string {
	host := dnsProviderName(zone.Provider)
	switch {
	case errors.Is(err, provider.ErrInfrastructureAmbiguous):
		return fmt.Sprintf("%s didn't confirm the TXT record for %s. Loomplane will look for it before writing again.", host, zone.ProofName)
	case errors.Is(err, provider.ErrInfrastructureDrift):
		return fmt.Sprintf("A TXT record Loomplane didn't create already uses %s, so Loomplane left it alone. Remove it, or add the value below as another TXT record yourself, then check again.", zone.ProofName)
	case errors.Is(err, provider.ErrInfrastructureScope):
		return fmt.Sprintf("The %s connection can't write DNS records for %s. Give its token DNS edit access, then check again.", host, zone.ProviderZone.Name)
	case errors.Is(err, ErrConflict), errors.Is(err, provider.ErrInfrastructureIdentity):
		return fmt.Sprintf("The %s connection changed since %s was added, so Loomplane didn't write the TXT record.", host, zone.Name)
	}
	return fmt.Sprintf("Loomplane couldn't write the TXT record through %s. Check again to retry.", host)
}

func dnsProviderName(cloud string) string {
	switch cloud {
	case connections.Cloudflare:
		return "Cloudflare"
	case connections.DigitalOcean:
		return "DigitalOcean"
	case connections.Vercel:
		return "Vercel"
	}
	return cloud
}

// checkZoneProof publishes a managed zone's proof if it still needs it, then
// looks for the proof in public DNS. Every check is saved, so the page can
// say when the record was last looked for.
func (s *Service) checkZoneProof(ctx context.Context, st DomainStore, zone *Zone) error {
	if err := s.publishZoneProof(ctx, st, zone); err != nil {
		return err
	}
	checkErr := s.verifyZoneControl(ctx, zone)
	zone.ProofCheckedAt = s.now().UTC()
	if checkErr == nil {
		zone.VerifiedAt = zone.ProofCheckedAt
	}
	saveErr := st.SaveZone(ctx, zone)
	if checkErr != nil {
		return checkErr
	}
	return saveErr
}

// zoneProofDue is a managed zone the background still checks: unverified,
// with an unexpired proof, and not checked since checkedBefore.
func zoneProofDue(zone *Zone, now, checkedBefore time.Time) bool {
	return zone.Mode == "managed" && zone.VerifiedAt.IsZero() && zone.hasControlProof() && now.Before(zone.ProofExpiresAt) && !zone.ProofCheckedAt.After(checkedBefore)
}

// CheckZoneProofs retries pending publishes and checks public DNS for
// managed zones still waiting on their proof, so a zone becomes usable
// without anyone pressing Check again. Each zone is checked at most once
// every zoneProofCheckEvery, and a pass checks at most zoneProofBatch.
func (s *Service) CheckZoneProofs(ctx context.Context) error {
	st, err := s.domainStore()
	if err != nil {
		return err
	}
	now := s.now().UTC()
	checkedBefore := now.Add(-zoneProofCheckEvery)
	zones, err := st.ZoneProofsDue(ctx, now, checkedBefore, zoneProofBatch)
	if err != nil {
		return err
	}
	var errs []error
	for _, zone := range zones {
		if !zoneProofDue(zone, now, checkedBefore) {
			continue
		}
		// A record not visible yet is the usual answer while DNS propagates,
		// and a save lost to a check from the page is that check's to make.
		if checkErr := s.checkZoneProof(ctx, st, zone); checkErr != nil && !errors.Is(checkErr, ErrConflict) && ctx.Err() == nil {
			errs = append(errs, fmt.Errorf("zone %s: %w", zone.ID, checkErr))
		}
	}
	return errors.Join(errs...)
}

// removeZoneProof deletes a managed zone's published proof by its exact
// receipt before the zone goes. A record already gone is done. A delete
// that fails keeps the zone, so the record is never left behind unseen.
func (s *Service) removeZoneProof(ctx context.Context, st DomainStore, zone *Zone) error {
	if zone.Mode != "managed" || zone.ProofRecord == nil {
		return nil
	}
	// The record is the zone's control proof. While routes use the zone,
	// the zone stays, and so must the record.
	routes, err := st.OrgRoutes(ctx, zone.OrgID)
	if err != nil {
		return err
	}
	for _, route := range routes {
		if route.Input.ZoneID == zone.ID {
			return fmt.Errorf("%w: remove the hostnames that use %s first", ErrConflict, zone.Name)
		}
	}
	adapter, cred, err := s.zoneCredentialFor(ctx, zone, true)
	if err == nil {
		err = adapter.DeleteRecord(ctx, cred, provider.DNSDeleteRequest{Zone: zone.ProviderZone, Expected: *zone.ProofRecord})
	}
	if err != nil && !errors.Is(err, provider.ErrInfrastructureNotFound) {
		return fmt.Errorf("%w: the TXT record %s couldn't be deleted from %s, so %s was kept. Try again, or delete that record there yourself first", ErrConflict, zone.ProofName, dnsProviderName(zone.Provider), zone.Name)
	}
	return nil
}
