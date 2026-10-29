package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// proofDNS is the hosting zone example.com in memory. It writes the way the
// real adapters do, through PlanDNSUpsert, and keeps a record's ID across an
// update the way Cloudflare does.
type proofDNS struct {
	ownership bool
	records   []provider.DNSRecord
	writes    int
	deletes   int
	last      provider.DNSUpsertRequest
	next      int
	// upsertErr refuses a write before anything is sent.
	upsertErr error
	// loseResponse lands the write and loses its answer.
	loseResponse bool
	// dropWrite loses the write and its answer: nothing lands.
	dropWrite bool
	deleteErr error
	listErr   error
	lists     int
	// during runs once, inside the next write, before it lands.
	during func()
	// block holds GetZone until the caller's context ends.
	block bool
	// blockWrite holds a write until the caller's context ends, then
	// answers as the real adapters do for a write cut short.
	blockWrite bool
}

func (a *proofDNS) SupportsDNSRecordOwnership() bool { return a.ownership }

func (*proofDNS) ListZones(_ context.Context, c provider.InfrastructureCredential) ([]provider.DNSZone, error) {
	return []provider.DNSZone{{ID: "parent", Name: "example.com", AccountID: c.AccountID, Verified: true}}, nil
}
func (a *proofDNS) GetZone(ctx context.Context, c provider.InfrastructureCredential, name string) (provider.DNSZone, error) {
	if a.block {
		<-ctx.Done()
		return provider.DNSZone{}, ctx.Err()
	}
	if name != "example.com" {
		return provider.DNSZone{}, provider.ErrInfrastructureNotFound
	}
	return provider.DNSZone{ID: "parent", Name: name, AccountID: c.AccountID, Verified: true}, nil
}
func (*proofDNS) CreateZone(context.Context, provider.InfrastructureCredential, provider.DNSZoneSpec) (provider.DNSZone, error) {
	return provider.DNSZone{}, provider.ErrInfrastructureUnsupported
}
func (a *proofDNS) ListRecords(context.Context, provider.InfrastructureCredential, provider.DNSZone) ([]provider.DNSRecord, error) {
	a.lists++
	if a.listErr != nil {
		return nil, a.listErr
	}
	return slices.Clone(a.records), nil
}
func (a *proofDNS) UpsertRecord(ctx context.Context, _ provider.InfrastructureCredential, req provider.DNSUpsertRequest) (provider.DNSRecord, error) {
	a.last = req
	if a.blockWrite {
		<-ctx.Done()
		return provider.DNSRecord{}, errors.Join(provider.ErrInfrastructureAmbiguous, ctx.Err())
	}
	if a.upsertErr != nil {
		return provider.DNSRecord{}, a.upsertErr
	}
	if a.listErr != nil {
		return provider.DNSRecord{}, a.listErr
	}
	if a.during != nil {
		during := a.during
		a.during = nil
		during()
	}
	if a.dropWrite {
		return provider.DNSRecord{}, errors.Join(provider.ErrInfrastructureAmbiguous, errors.New("connection reset"))
	}
	if req.ReconcileOnly && req.Expected != nil {
		for _, r := range a.records {
			if r.ID != req.Expected.ID {
				continue
			}
			switch r {
			case req.Record:
				return r, nil
			case *req.Expected:
				return provider.DNSRecord{}, provider.ErrInfrastructureAmbiguous
			}
			return provider.DNSRecord{}, provider.ErrInfrastructureDrift
		}
		return provider.DNSRecord{}, provider.ErrInfrastructureDrift
	}
	prior, done, err := provider.PlanDNSUpsert(req, a.records, a.ownership)
	if err != nil || done {
		return prior, err
	}
	a.writes++
	record := req.Record
	if req.Expected == nil {
		a.next++
		record.ID = fmt.Sprintf("rec-%d", a.next)
		a.records = append(a.records, record)
	} else {
		for i := range a.records {
			if a.records[i].ID == prior.ID {
				a.records[i] = record
			}
		}
	}
	if a.loseResponse {
		return provider.DNSRecord{}, errors.Join(provider.ErrInfrastructureAmbiguous, errors.New("connection reset"))
	}
	return record, nil
}
func (a *proofDNS) DeleteRecord(_ context.Context, _ provider.InfrastructureCredential, req provider.DNSDeleteRequest) error {
	if a.deleteErr != nil {
		return a.deleteErr
	}
	for i, r := range a.records {
		if r.ID == req.Expected.ID {
			if err := provider.MatchDNSPrior(req.Expected, r); err != nil {
				return err
			}
			a.deletes++
			a.records = append(a.records[:i], a.records[i+1:]...)
			return nil
		}
	}
	return provider.ErrInfrastructureNotFound
}

// proofs is every record at a _loomplane name.
func (a *proofDNS) proofs() []provider.DNSRecord {
	out := []provider.DNSRecord{}
	for _, r := range a.records {
		if strings.HasPrefix(r.Name, "_loomplane") {
			out = append(out, r)
		}
	}
	return out
}

// proofLookups is public DNS: it holds values, or nothing at all.
type proofLookups struct {
	values  []string
	lookups int
}

func (r *proofLookups) LookupTXT(_ context.Context, name string) ([]string, error) {
	r.lookups++
	if len(r.values) == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	return r.values, nil
}
func (*proofLookups) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) { return nil, nil }

func (s *overviewStore) RemoveZone(_ context.Context, org, id string, revision int64) error {
	for i, zone := range s.zones {
		if zone.OrgID == org && zone.ID == id {
			if zone.Revision != revision {
				return ErrConflict
			}
			s.zones = append(s.zones[:i], s.zones[i+1:]...)
			s.removed = append(s.removed, id)
			return nil
		}
	}
	return ErrNotFound
}

func (s *overviewStore) ZoneProofsDue(_ context.Context, now, checkedBefore, recheckBefore time.Time, limit int) ([]*Zone, error) {
	out := []*Zone{}
	for _, zone := range s.zones {
		if zoneProofDue(zone, now, checkedBefore, recheckBefore) && len(out) < limit {
			v := *zone
			out = append(out, &v)
		}
	}
	return out, nil
}

var proofManager = security.Principal{OrgID: "org", UserID: "manager"}

type proofEnv struct {
	svc      *Service
	store    *overviewStore
	dns      *proofDNS
	resolver *proofLookups
	now      time.Time
}

// newProofEnv hosts example.com through cloud. Vercel keeps owner tokens on
// records; DigitalOcean doesn't, so its records are Loomplane's by receipt.
func newProofEnv(t *testing.T, cloud string) *proofEnv {
	t.Helper()
	env := &proofEnv{
		store:    &overviewStore{projects: map[string]map[string]string{"org": {"shop": "Shop"}}},
		dns:      &proofDNS{ownership: cloud == "vercel", records: []provider.DNSRecord{{ID: "mail", ZoneID: "parent", Name: "mail", Type: "TXT", Value: "keep", TTL: 300}}},
		resolver: &proofLookups{},
		now:      time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC),
	}
	registry := NewAdapters()
	if err := registry.RegisterDNS(cloud, env.dns); err != nil {
		t.Fatal(err)
	}
	env.svc = NewService(Deps{Domains: env.store, Authz: cleanupTestAuth{}, Connections: routeDNSCredentials{cloud}, Adapters: registry, Resolver: env.resolver})
	env.svc.now = func() time.Time { return env.now }
	return env
}

func (env *proofEnv) create(t *testing.T, mode string) *Zone {
	t.Helper()
	in := ZoneInput{Name: "shop.example.com", Mode: mode, ProjectIDs: []string{"shop"}}
	if mode == "managed" {
		in.ConnectionID = "dns"
	}
	zone, err := env.svc.CreateZone(t.Context(), proofManager, in)
	if err != nil {
		t.Fatal(err)
	}
	return zone
}

func (env *proofEnv) stored(t *testing.T, id string) *Zone {
	t.Helper()
	zone, err := env.store.Zone(t.Context(), "org", id)
	if err != nil {
		t.Fatal(err)
	}
	return zone
}

func TestManagedZoneCreatePublishesOwnedProof(t *testing.T) {
	for _, cloud := range []string{"vercel", "digitalocean"} {
		t.Run(cloud, func(t *testing.T) {
			env := newProofEnv(t, cloud)
			zone := env.create(t, "managed")
			proofs := env.dns.proofs()
			if env.dns.writes != 1 || len(proofs) != 1 {
				t.Fatalf("writes=%d proofs=%+v", env.dns.writes, proofs)
			}
			owner := ""
			if cloud == "vercel" {
				owner = "loomplane:zone-proof:" + zone.ID
			}
			// The zone is a namespace inside the hosting zone example.com.
			want := provider.DNSRecord{ID: proofs[0].ID, ZoneID: "parent", Name: "_loomplane.shop", Type: "TXT", Value: zone.ProofValue, TTL: 60, Owner: owner}
			if proofs[0] != want {
				t.Fatalf("published %+v, want %+v", proofs[0], want)
			}
			stored := env.stored(t, zone.ID)
			if stored.ProofRecord == nil || *stored.ProofRecord != want || stored.ProofPublishAttempted || stored.ProofPublishError != "" {
				t.Fatalf("receipt not saved: %+v", stored)
			}
			if zone.ProofRecord == nil || zone.Revision != stored.Revision {
				t.Fatalf("returned zone is behind the stored one: %+v", zone)
			}
			if len(env.dns.records) != 2 || env.dns.records[0].Value != "keep" {
				t.Fatal("unrelated record touched", env.dns.records)
			}
		})
	}
}

func TestExternalZoneCreateWritesNothing(t *testing.T) {
	env := newProofEnv(t, "vercel")
	zone := env.create(t, "external")
	if env.dns.writes != 0 || zone.ProofRecord != nil || env.stored(t, zone.ID).ProofRecord != nil {
		t.Fatalf("external zone published: writes=%d %+v", env.dns.writes, zone)
	}
}

func TestZoneProofPublishFailureKeepsZoneAndCheckRetries(t *testing.T) {
	env := newProofEnv(t, "vercel")
	env.dns.upsertErr = errors.New("provider unavailable")
	zone := env.create(t, "managed")
	stored := env.stored(t, zone.ID)
	if stored.ProofRecord != nil || stored.ProofPublishAttempted || stored.ProofPublishError == "" || zone.ProofPublishError == "" {
		t.Fatalf("failure not recorded: %+v", stored)
	}

	env.dns.upsertErr = nil
	env.resolver.values = []string{zone.ProofValue}
	verified, err := env.svc.VerifyZone(t.Context(), proofManager, zone.ID)
	if err != nil {
		t.Fatal(err)
	}
	if env.dns.writes != 1 || verified.ProofRecord == nil || verified.ProofPublishError != "" || verified.VerifiedAt.IsZero() {
		t.Fatalf("check again didn't publish and verify: writes=%d %+v", env.dns.writes, verified)
	}
}

func TestZoneProofLostResponseNeverWritesTwice(t *testing.T) {
	for _, cloud := range []string{"vercel", "digitalocean"} {
		t.Run(cloud, func(t *testing.T) {
			env := newProofEnv(t, cloud)
			env.dns.loseResponse = true
			zone := env.create(t, "managed")
			stored := env.stored(t, zone.ID)
			if !stored.ProofPublishAttempted || stored.ProofRecord != nil || stored.ProofPublishError == "" {
				t.Fatalf("unknown outcome not durable: %+v", stored)
			}
			env.dns.loseResponse = false
			// Inside the grace window the write may still be landing.
			_, _ = env.svc.VerifyZone(t.Context(), proofManager, zone.ID)
			if env.dns.lists != 0 || env.stored(t, zone.ID).ProofRecord != nil {
				t.Fatal("unknown write settled inside the grace window")
			}
			env.now = env.now.Add(zoneProofCheckEvery + time.Second)
			_, _ = env.svc.VerifyZone(t.Context(), proofManager, zone.ID)
			if env.dns.writes != 1 || len(env.dns.proofs()) != 1 {
				t.Fatalf("lost response replayed: writes=%d proofs=%+v", env.dns.writes, env.dns.proofs())
			}
			stored = env.stored(t, zone.ID)
			if cloud == "vercel" {
				// The owner token finds the record the lost answer described.
				if stored.ProofRecord == nil || stored.ProofRecord.ID != env.dns.proofs()[0].ID || stored.ProofPublishAttempted || stored.ProofPublishError != "" {
					t.Fatalf("owned record not recovered: %+v", stored)
				}
			} else if stored.ProofRecord != nil || stored.ProofPublishError == "" {
				// Without owner tokens a record nobody has a receipt for stays unclaimed.
				t.Fatalf("ownerless record adopted: %+v", stored)
			}
		})
	}
}

func TestZoneProofLeavesAForeignRecordAlone(t *testing.T) {
	env := newProofEnv(t, "vercel")
	foreign := provider.DNSRecord{ID: "user", ZoneID: "parent", Name: "_loomplane.shop", Type: "TXT", Value: "set-by-hand", TTL: 300}
	env.dns.records = append(env.dns.records, foreign)
	zone := env.create(t, "managed")
	stored := env.stored(t, zone.ID)
	if env.dns.writes != 0 || stored.ProofRecord != nil || !strings.Contains(stored.ProofPublishError, "didn't create") || !strings.Contains(stored.ProofPublishError, "_loomplane.shop.example.com") || !strings.Contains(stored.ProofPublishError, "Record details") {
		t.Fatalf("foreign record: writes=%d %+v", env.dns.writes, stored)
	}
	if _, err := env.svc.VerifyZone(t.Context(), proofManager, zone.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("unproven zone verified", err)
	}
	if env.dns.writes != 0 || env.dns.deletes != 0 || !slices.Contains(env.dns.records, foreign) {
		t.Fatal("foreign record overwritten", env.dns.records)
	}
	// The owner can still publish the value next to their own record.
	env.resolver.values = []string{"set-by-hand", zone.ProofValue}
	if verified, err := env.svc.VerifyZone(t.Context(), proofManager, zone.ID); err != nil || verified.VerifiedAt.IsZero() {
		t.Fatal("hand-published proof refused", err)
	}
}

func TestZoneProofCheckThenRenewUpdatesInPlace(t *testing.T) {
	env := newProofEnv(t, "vercel")
	zone := env.create(t, "managed")
	first := env.dns.proofs()[0]
	if _, err := env.svc.VerifyZone(t.Context(), proofManager, zone.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("verified before DNS showed the record", err)
	}
	checked := env.stored(t, zone.ID)
	if checked.ProofCheckedAt.IsZero() || env.dns.writes != 1 {
		t.Fatalf("check: writes=%d %+v", env.dns.writes, checked)
	}

	renewed, err := env.svc.UpdateZone(t.Context(), proofManager, zone.ID, ZoneUpdate{Revision: checked.Revision, ProjectIDs: checked.ProjectIDs, RenewProof: true})
	if err != nil {
		t.Fatal(err)
	}
	proofs := env.dns.proofs()
	if renewed.ProofValue == zone.ProofValue || env.dns.writes != 2 || len(proofs) != 1 || proofs[0].ID != first.ID || proofs[0].Value != renewed.ProofValue {
		t.Fatalf("renew didn't update in place: writes=%d %+v", env.dns.writes, proofs)
	}
	stored := env.stored(t, zone.ID)
	if stored.ProofRecord == nil || *stored.ProofRecord != proofs[0] || stored.Revision != renewed.Revision {
		t.Fatalf("renewed receipt: %+v", stored)
	}

	env.resolver.values = []string{zone.ProofValue}
	if _, err = env.svc.VerifyZone(t.Context(), proofManager, zone.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("old value verified the renewed proof", err)
	}
	env.resolver.values = []string{renewed.ProofValue}
	verified, err := env.svc.VerifyZone(t.Context(), proofManager, zone.ID)
	if err != nil || verified.VerifiedAt.IsZero() || env.dns.writes != 2 {
		t.Fatalf("renewed value: writes=%d %v", env.dns.writes, err)
	}
}

func TestRemoveZoneDeletesItsProofByReceipt(t *testing.T) {
	t.Run("deleted", func(t *testing.T) {
		env := newProofEnv(t, "digitalocean")
		zone := env.create(t, "managed")
		if err := env.svc.RemoveZone(t.Context(), proofManager, zone.ID, zone.Revision); err != nil {
			t.Fatal(err)
		}
		if env.dns.deletes != 1 || len(env.dns.proofs()) != 0 || len(env.store.zones) != 0 || len(env.dns.records) != 1 {
			t.Fatalf("deletes=%d records=%+v zones=%d", env.dns.deletes, env.dns.records, len(env.store.zones))
		}
	})
	t.Run("already gone", func(t *testing.T) {
		env := newProofEnv(t, "vercel")
		zone := env.create(t, "managed")
		env.dns.records = env.dns.records[:1]
		if err := env.svc.RemoveZone(t.Context(), proofManager, zone.ID, zone.Revision); err != nil || len(env.store.zones) != 0 {
			t.Fatal("missing record blocked removal", err)
		}
	})
	t.Run("delete fails", func(t *testing.T) {
		env := newProofEnv(t, "vercel")
		zone := env.create(t, "managed")
		env.dns.deleteErr = errors.New("provider unavailable")
		err := env.svc.RemoveZone(t.Context(), proofManager, zone.ID, zone.Revision)
		if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "TXT record") || len(env.store.zones) != 1 || len(env.dns.proofs()) != 1 {
			t.Fatalf("failed delete: %v zones=%d", err, len(env.store.zones))
		}
		if stored := env.stored(t, zone.ID); stored.ProofRecord == nil {
			t.Fatal("receipt dropped although the record is still there")
		}
	})
	t.Run("routes still use it", func(t *testing.T) {
		env := newProofEnv(t, "vercel")
		zone := env.create(t, "managed")
		env.store.routes = append(env.store.routes, &Route{ID: "route", OrgID: "org", ProjectID: "shop", Status: "ready", Input: RouteInput{ZoneID: zone.ID}})
		if err := env.svc.RemoveZone(t.Context(), proofManager, zone.ID, zone.Revision); err == nil || env.dns.deletes != 0 || len(env.dns.proofs()) != 1 {
			t.Fatal("proof deleted from a zone that stays", err)
		}
	})
	t.Run("stale revision", func(t *testing.T) {
		env := newProofEnv(t, "vercel")
		zone := env.create(t, "managed")
		if err := env.svc.RemoveZone(t.Context(), proofManager, zone.ID, zone.Revision-1); !errors.Is(err, ErrConflict) || env.dns.deletes != 0 {
			t.Fatal("stale removal deleted the proof", err)
		}
	})
}

func TestZoneProofBackgroundCheckIsRateLimited(t *testing.T) {
	env := newProofEnv(t, "vercel")
	env.dns.upsertErr = errors.New("provider unavailable")
	zone := env.create(t, "managed")
	external := env.create(t, "external")
	env.dns.upsertErr = nil

	if err := env.svc.CheckZoneProofs(t.Context()); err != nil {
		t.Fatal(err)
	}
	// The pending publish was retried, then public DNS was looked at.
	if env.dns.writes != 1 || env.resolver.lookups != 1 || env.stored(t, zone.ID).ProofRecord == nil {
		t.Fatalf("background retry: writes=%d lookups=%d", env.dns.writes, env.resolver.lookups)
	}
	env.now = env.now.Add(time.Minute)
	if err := env.svc.CheckZoneProofs(t.Context()); err != nil {
		t.Fatal(err)
	}
	if env.resolver.lookups != 1 {
		t.Fatal("checked again within two minutes", env.resolver.lookups)
	}
	env.now = env.now.Add(time.Minute)
	env.resolver.values = []string{zone.ProofValue}
	if err := env.svc.CheckZoneProofs(t.Context()); err != nil {
		t.Fatal(err)
	}
	if env.resolver.lookups != 2 || env.stored(t, zone.ID).VerifiedAt.IsZero() {
		t.Fatalf("due check: lookups=%d", env.resolver.lookups)
	}
	if !env.stored(t, external.ID).ProofCheckedAt.IsZero() {
		t.Fatal("external zone checked in the background")
	}
	// A verified zone is done.
	env.now = env.now.Add(time.Hour)
	if err := env.svc.CheckZoneProofs(t.Context()); err != nil || env.resolver.lookups != 2 {
		t.Fatal("verified zone checked again", err, env.resolver.lookups)
	}
}

func TestZoneProofBackgroundSkipsExpiredProofs(t *testing.T) {
	env := newProofEnv(t, "vercel")
	env.create(t, "managed")
	env.now = env.now.Add(25 * time.Hour)
	if err := env.svc.CheckZoneProofs(t.Context()); err != nil || env.resolver.lookups != 0 || env.dns.writes != 1 {
		t.Fatal("expired proof checked", err, env.resolver.lookups)
	}
}

func TestZoneProofLostCreateThatNeverLandedIsWrittenAgain(t *testing.T) {
	for _, cloud := range []string{"vercel", "digitalocean"} {
		t.Run(cloud, func(t *testing.T) {
			env := newProofEnv(t, cloud)
			env.dns.dropWrite = true
			zone := env.create(t, "managed")
			if stored := env.stored(t, zone.ID); !stored.ProofPublishAttempted || stored.ProofRecord != nil {
				t.Fatalf("unknown outcome not durable: %+v", stored)
			}
			env.dns.dropWrite = false
			env.now = env.now.Add(zoneProofCheckEvery + time.Second)
			_, _ = env.svc.VerifyZone(t.Context(), proofManager, zone.ID)
			// The provider shows nothing at the name, so the same attempt writes.
			proofs := env.dns.proofs()
			if env.dns.writes != 1 || len(proofs) != 1 || env.dns.last.ReconcileOnly {
				t.Fatalf("writes=%d reconcile=%v proofs=%+v", env.dns.writes, env.dns.last.ReconcileOnly, proofs)
			}
			stored := env.stored(t, zone.ID)
			if stored.ProofRecord == nil || *stored.ProofRecord != proofs[0] || stored.ProofPublishAttempted || stored.ProofPublishError != "" {
				t.Fatalf("receipt: %+v", stored)
			}
		})
	}
}

func TestZoneProofLostCreateStaysReconcileOnlyWhenTheListingFails(t *testing.T) {
	env := newProofEnv(t, "vercel")
	env.dns.dropWrite = true
	zone := env.create(t, "managed")
	env.dns.dropWrite = false
	env.dns.listErr = errors.New("provider unavailable")
	env.now = env.now.Add(zoneProofCheckEvery + time.Second)
	_, _ = env.svc.VerifyZone(t.Context(), proofManager, zone.ID)
	stored := env.stored(t, zone.ID)
	if env.dns.writes != 0 || !stored.ProofPublishAttempted || stored.ProofRecord != nil || !strings.Contains(stored.ProofPublishError, "didn't confirm") {
		t.Fatalf("writes=%d %+v", env.dns.writes, stored)
	}
	env.dns.listErr = nil
	_, _ = env.svc.VerifyZone(t.Context(), proofManager, zone.ID)
	if env.dns.writes != 1 || env.stored(t, zone.ID).ProofRecord == nil {
		t.Fatal("no write once the listing answered", env.dns.writes)
	}
}

func TestZoneProofRecordDeletedAtTheProviderIsPublishedAgain(t *testing.T) {
	env := newProofEnv(t, "vercel")
	zone := env.create(t, "managed")
	first := *env.stored(t, zone.ID).ProofRecord

	// Still there: keep waiting, write nothing.
	env.now = env.now.Add(zoneProofRelistAfter)
	if _, err := env.svc.VerifyZone(t.Context(), proofManager, zone.ID); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if env.dns.writes != 1 || *env.stored(t, zone.ID).ProofRecord != first {
		t.Fatal("present record rewritten", env.dns.writes)
	}

	// Someone deletes it at the provider. The record is only looked for
	// once it has had time to show.
	env.dns.records = env.dns.records[:1]
	env.now = env.now.Add(zoneProofRelistAfter)
	if _, err := env.svc.VerifyZone(t.Context(), proofManager, zone.ID); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	proofs := env.dns.proofs()
	stored := env.stored(t, zone.ID)
	if env.dns.writes != 2 || len(proofs) != 1 || proofs[0].ID == first.ID || stored.ProofRecord == nil || *stored.ProofRecord != proofs[0] {
		t.Fatalf("writes=%d proofs=%+v receipt=%+v", env.dns.writes, proofs, stored.ProofRecord)
	}
	// The fresh record is checked on the same pass.
	if env.resolver.lookups != 3 {
		t.Fatal("not checked again after publishing", env.resolver.lookups)
	}
}

func TestZoneProofRecordWithARotatedIDStillCounts(t *testing.T) {
	env := newProofEnv(t, "vercel")
	zone := env.create(t, "managed")
	// The provider rotated the record's ID; owner, name, type and value hold.
	env.dns.records[1].ID = "rotated"
	env.now = env.now.Add(zoneProofRelistAfter)
	if _, err := env.svc.VerifyZone(t.Context(), proofManager, zone.ID); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if env.dns.writes != 1 || len(env.dns.proofs()) != 1 {
		t.Fatal("rotated record replaced", env.dns.writes)
	}
}

func TestZoneProofListingFailureKeepsTheReceipt(t *testing.T) {
	env := newProofEnv(t, "vercel")
	zone := env.create(t, "managed")
	receipt := *env.stored(t, zone.ID).ProofRecord
	env.dns.listErr = errors.New("provider unavailable")
	env.now = env.now.Add(zoneProofRelistAfter)
	_, err := env.svc.VerifyZone(t.Context(), proofManager, zone.ID)
	if err == nil || errors.Is(err, ErrConflict) {
		t.Fatal("listing failure hidden", err)
	}
	stored := env.stored(t, zone.ID)
	if stored.ProofRecord == nil || *stored.ProofRecord != receipt || stored.ProofPublishError == "" || stored.ProofCheckedAt.IsZero() || env.dns.writes != 1 {
		t.Fatalf("receipt not kept: %+v", stored)
	}
	env.dns.listErr = nil
	if _, err = env.svc.VerifyZone(t.Context(), proofManager, zone.ID); !errors.Is(err, ErrConflict) || env.stored(t, zone.ID).ProofPublishError != "" {
		t.Fatal("error not cleared once the record was found", err)
	}
}

func TestCreateZoneUnderADomainHasNoProofOfItsOwn(t *testing.T) {
	env := newProofEnv(t, "vercel")
	env.store.zones = append(env.store.zones, &Zone{ID: "zone_root", OrgID: "org", Name: "example.com", Mode: "external", ProjectIDs: []string{}, VerifiedAt: env.now, Revision: 1,
		ProofName: "_loomplane.example.com", ProofValue: "root-proof", ProofExpiresAt: env.now.Add(24 * time.Hour)})
	env.resolver.values = []string{"root-proof"}
	managed, err := env.svc.CreateZone(t.Context(), proofManager, ZoneInput{Name: "app.example.com", Mode: "managed", ConnectionID: "dns", ProjectIDs: []string{"shop"}})
	if err != nil {
		t.Fatal(err)
	}
	if managed.ProofName != "" || managed.ProofValue != "" || !managed.ProofExpiresAt.IsZero() || env.stored(t, managed.ID).ProofName != "" {
		t.Fatalf("managed subdomain proof: %+v", managed)
	}
	if env.dns.writes != 0 || env.dns.last.Record.Name != "" || len(env.dns.proofs()) != 0 {
		t.Fatalf("a subdomain's proof was published: writes=%d last=%+v", env.dns.writes, env.dns.last)
	}
	external, err := env.svc.CreateZone(t.Context(), proofManager, ZoneInput{Name: "www.example.com", Mode: "external", ProjectIDs: []string{"shop"}})
	if err != nil {
		t.Fatal(err)
	}
	if external.ProofName != "" || external.ProofValue != "" {
		t.Fatalf("external subdomain proof: %+v", external)
	}
	// The domain's own record is rechecked; the subdomains have none.
	due, _ := env.store.ZoneProofsDue(t.Context(), env.now, env.now, env.now, 10)
	for _, z := range due {
		if z.ID != "zone_root" {
			t.Fatalf("a subdomain is background-checked: %s", z.Name)
		}
	}
	zones, err := env.svc.ProjectZones(t.Context(), proofManager, "shop")
	if err != nil || len(zones) != 2 || !zones[0].Verified || !zones[1].Verified {
		t.Fatalf("subdomains read as verified through example.com: %+v %v", zones, err)
	}
}

func TestVerifiedZoneProofDeletedAfterItsWindowIsPublishedAgain(t *testing.T) {
	env := newProofEnv(t, "vercel")
	zone := env.create(t, "managed")
	env.resolver.values = []string{zone.ProofValue}
	if _, err := env.svc.VerifyZone(t.Context(), proofManager, zone.ID); err != nil {
		t.Fatal(err)
	}
	first := *env.stored(t, zone.ID).ProofRecord

	// Weeks later someone deletes it at the provider. The window a pending
	// proof has to verify is long over, but this one already did.
	env.dns.records = env.dns.records[:1]
	env.resolver.values = nil
	env.now = env.now.Add(30 * 24 * time.Hour)
	if _, err := env.svc.VerifyZone(t.Context(), proofManager, zone.ID); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	proofs := env.dns.proofs()
	stored := env.stored(t, zone.ID)
	if env.dns.writes != 2 || len(proofs) != 1 || proofs[0].ID == first.ID || stored.ProofRecord == nil || *stored.ProofRecord != proofs[0] {
		t.Fatalf("writes=%d proofs=%+v receipt=%+v", env.dns.writes, proofs, stored.ProofRecord)
	}
}

func TestMissingProofSaysWhichRecordAndWhatToDo(t *testing.T) {
	for _, mode := range []string{"managed", "external"} {
		t.Run(mode, func(t *testing.T) {
			env := newProofEnv(t, "vercel")
			zone := env.create(t, mode)
			env.resolver.values = []string{zone.ProofValue}
			if _, err := env.svc.VerifyZone(t.Context(), proofManager, zone.ID); err != nil {
				t.Fatal(err)
			}
			env.resolver.values = []string{"someone-else"}
			err := env.svc.verifyZoneControl(t.Context(), env.stored(t, zone.ID))
			var missing *ProofMissingError
			if !errors.Is(err, ErrConflict) || !errors.As(err, &missing) {
				t.Fatalf("got %v", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "_loomplane.shop.example.com") || !strings.Contains(msg, "shop.example.com under Domains") || !strings.Contains(msg, "Check again") {
				t.Fatal(msg)
			}
			if wantsHost := strings.Contains(msg, "DNS host"); wantsHost != (mode == "external") {
				t.Fatal("wrong remedy for", mode, msg)
			}
		})
	}
}

func TestAddingASubdomainChecksItsDomainsRecord(t *testing.T) {
	env := newProofEnv(t, "vercel")
	env.store.zones = append(env.store.zones, &Zone{ID: "zone_root", OrgID: "org", Name: "example.com", Mode: "external", ProjectIDs: []string{}, VerifiedAt: env.now.Add(-30 * 24 * time.Hour), Revision: 1,
		ProofName: "_loomplane.example.com", ProofValue: "root-proof", ProofExpiresAt: env.now.Add(-29 * 24 * time.Hour)})
	in := ZoneInput{Name: "app.example.com", Mode: "managed", ConnectionID: "dns", ProjectIDs: []string{"shop"}}
	// The domain's record is gone, so the subdomain isn't proved by it.
	_, err := env.svc.CreateZone(t.Context(), proofManager, in)
	var missing *ProofMissingError
	if !errors.As(err, &missing) || missing.Zone != "example.com" || len(env.store.zones) != 1 {
		t.Fatalf("added without its domain's record: %v zones=%d", err, len(env.store.zones))
	}
	env.resolver.values = []string{"root-proof"}
	if _, err = env.svc.CreateZone(t.Context(), proofManager, in); err != nil {
		t.Fatal(err)
	}
}

func TestVerifiedZoneWithAMissingRecordReadsProofMissing(t *testing.T) {
	env := newProofEnv(t, "vercel")
	zone := env.create(t, "managed")
	env.resolver.values = []string{zone.ProofValue}
	if _, err := env.svc.VerifyZone(t.Context(), proofManager, zone.ID); err != nil {
		t.Fatal(err)
	}
	sub := &Zone{ID: "zone_sub", OrgID: "org", Name: "app.shop.example.com", Mode: "managed", ProjectIDs: []string{}, Revision: 1}
	state := func() (DomainState, DomainState) {
		zones := []*Zone{env.stored(t, zone.ID), sub}
		LinkZoneParents(zones)
		return zoneState(zones[0]), zoneState(zones[1])
	}
	if own, inherited := state(); own != DomainReady || inherited != DomainReady {
		t.Fatal(own, inherited)
	}

	env.resolver.values = nil
	if _, err := env.svc.VerifyZone(t.Context(), proofManager, zone.ID); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if env.stored(t, zone.ID).ProofMissingAt.IsZero() {
		t.Fatal("missing record not recorded")
	}
	// The subdomain is proved by the same record, so it says so too.
	if own, inherited := state(); own != DomainProofMissing || inherited != DomainProofMissing {
		t.Fatal(own, inherited)
	}

	env.resolver.values = []string{zone.ProofValue}
	if _, err := env.svc.VerifyZone(t.Context(), proofManager, zone.ID); err != nil {
		t.Fatal(err)
	}
	if own, _ := state(); own != DomainReady || !env.stored(t, zone.ID).ProofMissingAt.IsZero() {
		t.Fatal("found record still reads missing", own)
	}
}

func TestVerifiedZoneProofIsRecheckedDailyAndOftenWhileMissing(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	checkedBefore := now.Add(-zoneProofCheckEvery)
	zone := &Zone{Mode: "managed", Name: "acme.dev", ProofName: "_loomplane.acme.dev", ProofValue: "v", ProofExpiresAt: now.Add(-30 * 24 * time.Hour), VerifiedAt: now.Add(-31 * 24 * time.Hour)}
	for _, c := range []struct {
		checked time.Duration
		missing bool
		due     bool
	}{
		{time.Hour, false, false},
		{25 * time.Hour, false, true},
		{time.Hour, true, true},
		{time.Minute, true, false},
	} {
		zone.ProofCheckedAt, zone.ProofMissingAt = now.Add(-c.checked), time.Time{}
		if c.missing {
			zone.ProofMissingAt = now.Add(-2 * time.Hour)
		}
		if got := zoneProofDue(zone, now, checkedBefore, now.Add(-zoneProofRecheckEvery)); got != c.due {
			t.Errorf("checked %v ago, missing=%v: due=%v", c.checked, c.missing, got)
		}
	}
	// One whose DNS its owner runs is checked the same way once verified.
	external := *zone
	external.Mode, external.ProofCheckedAt, external.ProofMissingAt = "external", now.Add(-25*time.Hour), time.Time{}
	if !zoneProofDue(&external, now, checkedBefore, now.Add(-zoneProofRecheckEvery)) {
		t.Error("verified external zone not rechecked")
	}
	external.VerifiedAt = time.Time{}
	external.ProofExpiresAt = now.Add(time.Hour)
	if zoneProofDue(&external, now, checkedBefore, now.Add(-zoneProofRecheckEvery)) {
		t.Error("pending external zone checked in the background")
	}
	// A domain verified before proofs were required has no record to check.
	legacy := &Zone{Mode: "managed", Name: "old.dev", VerifiedAt: now.Add(-time.Hour * 24 * 400)}
	if zoneProofDue(legacy, now, checkedBefore, now.Add(-zoneProofRecheckEvery)) {
		t.Error("legacy zone due")
	}
}

func TestVerifiedZonesAreRecheckedOnceAfterAStart(t *testing.T) {
	env := newProofEnv(t, "vercel")
	zone := env.create(t, "managed")
	env.resolver.values = []string{zone.ProofValue}
	if _, err := env.svc.VerifyZone(t.Context(), proofManager, zone.ID); err != nil {
		t.Fatal(err)
	}
	// The record goes while Loomplane is down, and it starts an hour after
	// the last check: well inside the day the daily recheck waits.
	env.dns.records = env.dns.records[:1]
	env.resolver.values = nil
	env.now = env.now.Add(time.Hour)
	env.svc.started = env.now
	env.now = env.now.Add(time.Minute)
	if err := env.svc.CheckZoneProofs(t.Context()); err != nil {
		t.Fatal(err)
	}
	stored := env.stored(t, zone.ID)
	if !stored.ProofCheckedAt.Equal(env.now) || stored.ProofMissingAt.IsZero() || len(env.dns.proofs()) != 1 {
		t.Fatalf("not rechecked after the start: %+v", stored)
	}
	// Checked since the start, it waits for the next pass's two minutes,
	// then the day, as before.
	checked := stored.ProofCheckedAt
	env.resolver.values = []string{zone.ProofValue}
	if err := env.svc.CheckZoneProofs(t.Context()); err != nil || !env.stored(t, zone.ID).ProofCheckedAt.Equal(checked) {
		t.Fatal("rechecked twice in one pass window", err)
	}
}
