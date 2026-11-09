package cloudinfra

import (
	"context"

	"github.com/xraph/loomplane/internal/platformcloud"
	"github.com/xraph/loomplane/pkg/provider"
)

// PlatformDNS writes the hostnames Loomplane Cloud generates into the
// platform owner's apex zone, through the same DNS adapters managed domains
// use. Org zones can't hold the apex, so platformcloud writes these itself
// with the platform's own credential.
type PlatformDNS struct {
	Adapters *Adapters
}

var _ platformcloud.DNSWriter = PlatformDNS{}

// zone resolves the apex zone at the provider, checking the credential
// reaches it in the account it names.
func (d PlatformDNS) zone(ctx context.Context, z platformcloud.DNSZone) (provider.DNSAdapter, provider.DNSZone, error) {
	if d.Adapters == nil {
		return nil, provider.DNSZone{}, ErrInvalid
	}
	adapter, err := d.Adapters.DNS(z.Provider)
	if err != nil {
		return nil, provider.DNSZone{}, err
	}
	zone, err := adapter.GetZone(ctx, z.Credential, z.Zone)
	if err != nil {
		return nil, provider.DNSZone{}, err
	}
	if zone.Name != z.Zone || (z.Credential.AccountID != "" && zone.AccountID != "" && zone.AccountID != z.Credential.AccountID) {
		return nil, provider.DNSZone{}, provider.ErrInfrastructureIdentity
	}
	return adapter, zone, nil
}

func (d PlatformDNS) Upsert(ctx context.Context, z platformcloud.DNSZone, record provider.DNSRecord, expected *provider.DNSRecord) (provider.DNSRecord, error) {
	adapter, zone, err := d.zone(ctx, z)
	if err != nil {
		return provider.DNSRecord{}, err
	}
	record.ZoneID = zone.ID
	return adapter.UpsertRecord(ctx, z.Credential, provider.DNSUpsertRequest{Zone: zone, Record: record, Expected: expected})
}

func (d PlatformDNS) Delete(ctx context.Context, z platformcloud.DNSZone, expected provider.DNSRecord) error {
	adapter, zone, err := d.zone(ctx, z)
	if err != nil {
		return err
	}
	return adapter.DeleteRecord(ctx, z.Credential, provider.DNSDeleteRequest{Zone: zone, Expected: expected})
}
