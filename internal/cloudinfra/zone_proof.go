package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// A managed zone's connection can write DNS in its hosting zone, so
// Loomplane publishes the TXT proof there itself. Verification still reads
// public DNS: a hosting zone that isn't authoritative for the name never
// shows the record, and the zone stays unverified.

// zoneProofTTL keeps a renewed value quick to reach resolvers. It is the
// route records' TTL and every supported provider's minimum or above.
const zoneProofTTL = 60

// zoneProofCheckEvery spaces background checks of one zone. It is also the
// grace window of a write that went unanswered: inside it the write may
// still be landing, so nothing else writes the zone's proof.
const zoneProofCheckEvery = 2 * time.Minute

// zoneProofRelistAfter is how long a written record has to show in public
// DNS before a check looks for it at the provider.
const zoneProofRelistAfter = 5 * time.Minute

// zoneProofBatch bounds the zones one background pass checks.
const zoneProofBatch = 50

// The background pass shares its goroutine with route and infrastructure
// recovery, so it is bounded. Zones it doesn't reach wait for the next tick.
var (
	zoneProofPassTimeout = 20 * time.Second
	zoneProofZoneTimeout = 8 * time.Second
)

// zoneProofWriteTimeout bounds a publish from the page well inside the
// grace window, so no other check writes the zone while it may still land.
var zoneProofWriteTimeout = zoneProofCheckEvery - 30*time.Second

// zoneProofSystemUser is who the background check acts as in the audit log.
const zoneProofSystemUser = "system:zone-proof"

// zoneProofReceiptRetries bounds re-reads after a receipt save loses the
// zone's revision to another writer.
const zoneProofReceiptRetries = 3

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

// proofWriteInFlight is an unanswered write still inside its grace window.
func (z *Zone) proofWriteInFlight(now time.Time) bool {
	return z.ProofPublishAttempted && now.Sub(z.ProofPublishAttemptedAt) < zoneProofCheckEvery
}

// publishZoneProof writes or updates a managed zone's TXT proof through its
// own connection. The attempt is saved before the write, and the receipt
// before anything treats the proof as published. A write whose answer was
// lost is settled from the provider's records once its grace window ends.
// The whole publish has a deadline inside that window.
// A provider failure is recorded on the zone as ProofPublishError and isn't
// returned: the zone still exists, and the next check tries again. Only a
// failed save is returned.
func (s *Service) publishZoneProof(ctx context.Context, st DomainStore, zone *Zone) error {
	ctx, cancel := context.WithTimeout(ctx, zoneProofWriteTimeout)
	defer cancel()
	now := s.now().UTC()
	if zone.Mode != "managed" || !zone.hasControlProof() || zone.proofPublished() || !now.Before(zone.ProofExpiresAt) || zone.proofWriteInFlight(now) {
		return nil
	}
	if !zone.VerifiedAt.IsZero() && zone.ProofRecord == nil && zone.ProofPublishAttemptedAt.IsZero() {
		// Verified from a record its owner added before Loomplane published
		// proofs. That record is theirs, and it already does the job.
		return nil
	}
	adapter, cred, err := s.zoneCredential(ctx, zone)
	if err != nil {
		return s.proofPublishFailed(ctx, st, zone, err)
	}
	reconcile := false
	if zone.ProofPublishAttempted {
		done, settleReconcile, settleErr := s.settleProofAttempt(ctx, adapter, cred, zone)
		switch {
		case settleErr != nil:
			return s.proofPublishFailed(ctx, st, zone, settleErr)
		case done:
			return s.saveProofReceipt(ctx, st, zone, *zone.ProofRecord)
		}
		reconcile = settleReconcile
	}
	desired, err := zoneProofRecord(zone)
	if err != nil {
		return s.proofPublishFailed(ctx, st, zone, err)
	}
	attempted, attemptedAt := zone.ProofPublishAttempted, zone.ProofPublishAttemptedAt
	zone.ProofPublishAttempted, zone.ProofPublishAttemptedAt = true, now
	if err = st.SaveZone(ctx, zone); err != nil {
		zone.ProofPublishAttempted, zone.ProofPublishAttemptedAt = attempted, attemptedAt
		return err
	}
	receipt, err := adapter.UpsertRecord(ctx, cred, provider.DNSUpsertRequest{Zone: zone.ProviderZone, Record: desired, Expected: zone.ProofRecord, ReconcileOnly: reconcile})
	if err != nil {
		return s.proofPublishFailed(ctx, st, zone, err)
	}
	return s.saveProofReceipt(ctx, st, zone, receipt)
}

// settleProofAttempt reads the provider's records to learn what an
// unanswered write did. It leaves zone.ProofRecord as the record the next
// write should update, or nil for a create. done means the record already
// holds the current value, and reconcile that only the adapter can tell
// whether an ownerless create landed. A listing that fails, or several
// records carrying this zone's owner token, stay unknown.
func (s *Service) settleProofAttempt(ctx context.Context, adapter provider.DNSAdapter, cred provider.InfrastructureCredential, zone *Zone) (done, reconcile bool, err error) {
	desired, err := zoneProofRecord(zone)
	if err != nil {
		return false, false, err
	}
	records, err := adapter.ListRecords(ctx, cred, zone.ProviderZone)
	if err != nil {
		return false, false, errors.Join(provider.ErrInfrastructureAmbiguous, err)
	}
	if zone.ProofRecord != nil {
		for _, r := range records {
			if r.ID != zone.ProofRecord.ID {
				continue
			}
			wanted := desired
			wanted.ID = r.ID
			switch r {
			case wanted:
				// The update landed.
				zone.ProofRecord = &r
				return true, false, nil
			case *zone.ProofRecord:
				// It didn't. An update by ID can't make a second record.
				return false, false, nil
			}
			return false, false, provider.ErrInfrastructureDrift
		}
	}
	owned, err := ownedProof(zone, records, desired.Name)
	if err != nil {
		return false, false, err
	}
	if owned != nil {
		// This zone's owner token marks the record as its own, whatever the
		// provider did to its ID or whichever value it holds.
		zone.ProofRecord = owned
		wanted := desired
		wanted.ID = owned.ID
		return *owned == wanted, false, nil
	}
	// Nothing of this zone's is there. A proof TXT is harmless to repeat,
	// unlike a route record, so the next write creates it. Without owner
	// tokens a record holding the value may be the lost create, and the
	// adapter only reconciles it.
	zone.ProofRecord = nil
	for _, r := range records {
		if desired.Owner == "" && r.Name == desired.Name && r.Type == desired.Type && r.Value == desired.Value {
			return false, true, nil
		}
	}
	return false, false, nil
}

// ownedProof is the one record at the proof name carrying this zone's owner
// token, nil when there is none or the provider keeps no owner tokens.
func ownedProof(zone *Zone, records []provider.DNSRecord, name string) (*provider.DNSRecord, error) {
	owner := zoneProofOwner(zone)
	if owner == "" {
		return nil, nil
	}
	var found *provider.DNSRecord
	for _, r := range records {
		if r.Name != name || r.Type != "TXT" || r.Owner != owner {
			continue
		}
		if found != nil {
			return nil, provider.ErrInfrastructureAmbiguous
		}
		found = &r
	}
	return found, nil
}

// saveProofReceipt saves the record a write returned. A save lost to
// another writer re-reads the zone and saves the receipt on top, as long
// as the zone still wants the value the record holds. The save outlives
// the caller's deadline: the record is already written.
func (s *Service) saveProofReceipt(ctx context.Context, st DomainStore, zone *Zone, receipt provider.DNSRecord) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	zone.ProofRecord, zone.ProofPublishAttempted, zone.ProofPublishError = &receipt, false, ""
	err := st.SaveZone(ctx, zone)
	for range zoneProofReceiptRetries {
		if !errors.Is(err, ErrConflict) {
			return err
		}
		current, readErr := st.Zone(ctx, zone.OrgID, zone.ID)
		if readErr != nil {
			return readErr
		}
		if current.ProofValue != receipt.Value {
			// Renewed meanwhile; that renewal's write settles the record.
			return err
		}
		current.ProofRecord, current.ProofPublishAttempted, current.ProofPublishError = &receipt, false, ""
		*zone = *current
		err = st.SaveZone(ctx, zone)
	}
	return err
}

// proofPublishFailed records why the proof isn't published. Only an
// ambiguous write keeps the attempt open: anything else was refused before
// a write, so the next try may write.
func (s *Service) proofPublishFailed(ctx context.Context, st DomainStore, zone *Zone, err error) error {
	if !errors.Is(err, provider.ErrInfrastructureAmbiguous) {
		zone.ProofPublishAttempted = false
	}
	zone.ProofPublishError = proofPublishMessage(zone, err)
	// A write cut short by its deadline still needs its attempt saved.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return st.SaveZone(ctx, zone)
}

func proofPublishMessage(zone *Zone, err error) string {
	host := dnsProviderName(zone.Provider)
	switch {
	case errors.Is(err, provider.ErrInfrastructureAmbiguous):
		return fmt.Sprintf("%s didn't confirm the TXT record for %s. Loomplane will look for it before writing again.", host, zone.ProofName)
	case errors.Is(err, provider.ErrInfrastructureDrift):
		return fmt.Sprintf("A TXT record Loomplane didn't create already uses %s, so Loomplane left it alone. Remove it, or add the value under Record details as another TXT record yourself, then check again.", zone.ProofName)
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
// say when the record was last looked for. wrote reports a publish in this
// pass, for the audit log.
func (s *Service) checkZoneProof(ctx context.Context, st DomainStore, zone *Zone) (wrote bool, err error) {
	before := zone.ProofRecord
	if publishErr := s.publishZoneProof(ctx, st, zone); publishErr != nil {
		return false, publishErr
	}
	wrote = zone.ProofRecord != before && zone.proofPublished()
	checkErr := s.verifyZoneControl(ctx, zone)
	now := s.now().UTC()
	// A record written a while ago that public DNS still doesn't show may
	// be gone at the provider. One written in this pass is just new.
	if errors.Is(checkErr, ErrConflict) && !wrote && zone.Mode == "managed" && zone.proofPublished() && now.Before(zone.ProofExpiresAt) && now.Sub(zone.ProofPublishAttemptedAt) >= zoneProofRelistAfter {
		republished, reclaimErr := s.republishMissingProof(ctx, st, zone)
		switch {
		case reclaimErr != nil:
			checkErr = reclaimErr
		case republished:
			wrote = true
			checkErr = s.verifyZoneControl(ctx, zone)
		}
	}
	zone.ProofCheckedAt = s.now().UTC()
	if checkErr == nil {
		zone.VerifiedAt = zone.ProofCheckedAt
	}
	saveErr := st.SaveZone(ctx, zone)
	if checkErr != nil {
		return wrote, checkErr
	}
	return wrote, saveErr
}

// republishMissingProof looks for the receipt's record in the hosting zone.
// One deleted at the provider loses its receipt and is published afresh;
// one still there is left alone, and one found by its owner token under a
// rotated ID moves the receipt to that ID. A listing that fails keeps the
// receipt, records why on the zone and returns the error.
func (s *Service) republishMissingProof(ctx context.Context, st DomainStore, zone *Zone) (bool, error) {
	adapter, cred, err := s.zoneCredential(ctx, zone)
	var records []provider.DNSRecord
	if err == nil {
		records, err = adapter.ListRecords(ctx, cred, zone.ProviderZone)
	}
	if err != nil {
		zone.ProofPublishError = fmt.Sprintf("Loomplane couldn't read the DNS records at %s to confirm the TXT record for %s is still there. Check again to retry.", dnsProviderName(zone.Provider), zone.ProofName)
		return false, err
	}
	for _, r := range records {
		if r.ID == zone.ProofRecord.ID {
			zone.ProofPublishError = ""
			return false, nil
		}
	}
	owned, err := ownedProof(zone, records, zone.ProofRecord.Name)
	if err != nil {
		return false, err
	}
	if owned != nil && owned.Value == zone.ProofRecord.Value {
		// The same record under the ID the provider rotated it to.
		zone.ProofRecord, zone.ProofPublishError = owned, ""
		return false, st.SaveZone(ctx, zone)
	}
	zone.ProofRecord = nil
	if publishErr := s.publishZoneProof(ctx, st, zone); publishErr != nil {
		return false, publishErr
	}
	return zone.proofPublished(), nil
}

// zoneProofDue is a managed zone the background still checks: unverified,
// with an unexpired proof, not checked since checkedBefore, and with no
// write inside its grace window.
func zoneProofDue(zone *Zone, now, checkedBefore time.Time) bool {
	inFlight := zone.ProofPublishAttempted && zone.ProofPublishAttemptedAt.After(checkedBefore)
	return zone.Mode == "managed" && zone.VerifiedAt.IsZero() && zone.hasControlProof() && now.Before(zone.ProofExpiresAt) && !zone.ProofCheckedAt.After(checkedBefore) && !inFlight
}

// CheckZoneProofs retries pending publishes and checks public DNS for
// managed zones still waiting on their proof, so a zone becomes usable
// without anyone pressing Check again. Each zone is checked at most once
// every zoneProofCheckEvery, a pass checks at most zoneProofBatch, and the
// pass and each zone have a deadline.
func (s *Service) CheckZoneProofs(ctx context.Context) error {
	st, err := s.domainStore()
	if err != nil {
		return err
	}
	pass, cancel := context.WithTimeout(ctx, zoneProofPassTimeout)
	defer cancel()
	now := s.now().UTC()
	checkedBefore := now.Add(-zoneProofCheckEvery)
	zones, err := st.ZoneProofsDue(pass, now, checkedBefore, zoneProofBatch)
	if err != nil {
		return err
	}
	var errs []error
	for _, zone := range zones {
		if pass.Err() != nil {
			break
		}
		if !zoneProofDue(zone, now, checkedBefore) {
			continue
		}
		if checkErr := s.checkZoneProofInBackground(pass, st, zone); checkErr != nil && ctx.Err() == nil {
			errs = append(errs, fmt.Errorf("zone %s: %w", zone.ID, checkErr))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) checkZoneProofInBackground(ctx context.Context, st DomainStore, zone *Zone) error {
	ctx, cancel := context.WithTimeout(ctx, zoneProofZoneTimeout)
	defer cancel()
	system := security.Principal{OrgID: zone.OrgID, UserID: zoneProofSystemUser}
	wrote, err := s.checkZoneProof(ctx, st, zone)
	if wrote {
		s.domainAudit(ctx, system, "domain.zone.write", zone.ID, "")
	}
	if err == nil {
		s.domainAudit(ctx, system, "domain.zone.write", zone.ID, "")
	}
	// A record not visible yet is the usual answer while DNS propagates, a
	// save lost to a check from the page is that check's to make, and a
	// zone out of time waits for the next pass.
	if errors.Is(err, ErrConflict) || errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	return err
}

// removeZoneProof deletes a managed zone's proof record before the zone
// goes: the receipt's record, or the record an unanswered write may have
// made. A record already gone is done. When Loomplane can't tell whether
// an unanswered write landed, or the delete fails, the zone is kept, so the
// record is never left behind unseen.
func (s *Service) removeZoneProof(ctx context.Context, st DomainStore, zone *Zone) error {
	if zone.Mode != "managed" || (zone.ProofRecord == nil && !zone.ProofPublishAttempted) {
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
	if err != nil {
		return proofDeleteFailed(zone)
	}
	target := zone.ProofRecord
	if zone.ProofPublishAttempted {
		target, err = s.unconfirmedProof(ctx, adapter, cred, zone)
		if err != nil {
			return err
		}
		if target == nil {
			return nil
		}
	}
	err = adapter.DeleteRecord(ctx, cred, provider.DNSDeleteRequest{Zone: zone.ProviderZone, Expected: *target})
	if err != nil && !errors.Is(err, provider.ErrInfrastructureNotFound) {
		return proofDeleteFailed(zone)
	}
	return nil
}

// unconfirmedProof is the record to delete when a write went unanswered:
// the receipt's record by ID, or this zone's one owner-token record. nil
// means nothing landed and the write's grace window is over.
func (s *Service) unconfirmedProof(ctx context.Context, adapter provider.DNSAdapter, cred provider.InfrastructureCredential, zone *Zone) (*provider.DNSRecord, error) {
	unknown := fmt.Errorf("%w: Loomplane can't confirm whether its TXT record landed. Check again first, then remove the domain", ErrConflict)
	desired, err := zoneProofRecord(zone)
	if err != nil {
		return nil, unknown
	}
	records, err := adapter.ListRecords(ctx, cred, zone.ProviderZone)
	if err != nil {
		return nil, unknown
	}
	if zone.ProofRecord != nil {
		for _, r := range records {
			if r.ID == zone.ProofRecord.ID {
				return &r, nil
			}
		}
	}
	if zone.RecordOwnership {
		owned, ownedErr := ownedProof(zone, records, desired.Name)
		if ownedErr != nil {
			return nil, unknown
		}
		if owned != nil {
			return owned, nil
		}
		if !zone.proofWriteInFlight(s.now().UTC()) {
			return nil, nil
		}
	}
	// Without owner tokens, or inside the grace window, nothing here says
	// what the write did.
	return nil, unknown
}

func proofDeleteFailed(zone *Zone) error {
	return fmt.Errorf("%w: the TXT record %s couldn't be deleted from %s, so %s was kept. Try again, or delete that record there yourself first", ErrConflict, zone.ProofName, dnsProviderName(zone.Provider), zone.Name)
}
