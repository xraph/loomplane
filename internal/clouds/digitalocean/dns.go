package digitalocean

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/xraph/loomplane/pkg/provider"
)

var _ provider.DNSAdapter = (*Client)(nil)

type infrastructureDomain struct {
	Name string `json:"name"`
}

func (d infrastructureDomain) zone(account string) provider.DNSZone {
	return provider.DNSZone{ID: d.Name, Name: d.Name, AccountID: account, Verified: true}
}

// ListZones discovers DNS zones without changing their delegation or registrar.
func (c *Client) ListZones(ctx context.Context, credential provider.InfrastructureCredential) ([]provider.DNSZone, error) {
	if err := c.infrastructureIdentity(ctx, credential); err != nil {
		return nil, err
	}
	result := []provider.DNSZone{}
	seen := map[string]bool{}
	for page := 1; page <= infrastructureMaxPages; page++ {
		var out struct {
			Domains []infrastructureDomain `json:"domains"`
			Links   infrastructureLinks    `json:"links"`
		}
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(pageSize)}}
		if err := c.do(ctx, credential.Token, http.MethodGet, "/v2/domains", q, nil, &out); err != nil {
			return nil, infrastructureError(err, "domain:read")
		}
		for _, d := range out.Domains {
			if provider.ValidateDNSName(d.Name, false) != nil || seen[d.Name] {
				return nil, provider.ErrInfrastructurePagination
			}
			seen[d.Name] = true
			result = append(result, d.zone(credential.AccountID))
		}
		if out.Links.Pages.Next == "" {
			return result, nil
		}
	}
	return nil, provider.ErrInfrastructurePagination
}

// GetZone reads DNS hosting metadata for the exact domain name.
func (c *Client) GetZone(ctx context.Context, credential provider.InfrastructureCredential, name string) (provider.DNSZone, error) {
	if err := provider.ValidateDNSName(name, false); err != nil {
		return provider.DNSZone{}, err
	}
	if err := c.infrastructureIdentity(ctx, credential); err != nil {
		return provider.DNSZone{}, err
	}
	return c.getInfrastructureZone(ctx, credential, name)
}
func (c *Client) getInfrastructureZone(ctx context.Context, credential provider.InfrastructureCredential, name string) (provider.DNSZone, error) {
	var out struct {
		Domain infrastructureDomain `json:"domain"`
	}
	if err := c.do(ctx, credential.Token, http.MethodGet, "/v2/domains/"+url.PathEscape(name), nil, nil, &out); err != nil {
		return provider.DNSZone{}, infrastructureError(err, "domain:read")
	}
	if out.Domain.Name != name {
		return provider.DNSZone{}, provider.ErrInfrastructureIdentity
	}
	return out.Domain.zone(credential.AccountID), nil
}

// CreateZone adds hosting for an existing domain without an initial A record.
func (c *Client) CreateZone(ctx context.Context, credential provider.InfrastructureCredential, spec provider.DNSZoneSpec) (provider.DNSZone, error) {
	existing, err := c.GetZone(ctx, credential, spec.Name)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, provider.ErrInfrastructureNotFound) {
		return provider.DNSZone{}, err
	}
	if spec.ReconcileOnly {
		return provider.DNSZone{}, provider.ErrInfrastructureAmbiguous
	}
	in := struct {
		Name string `json:"name"`
	}{Name: spec.Name}
	var out struct {
		Domain infrastructureDomain `json:"domain"`
	}
	if err = c.do(ctx, credential.Token, http.MethodPost, "/v2/domains", nil, in, &out); err != nil {
		return provider.DNSZone{}, errors.Join(provider.ErrInfrastructureAmbiguous, infrastructureError(err, "domain:create"))
	}
	if out.Domain.Name != spec.Name {
		return provider.DNSZone{}, provider.ErrInfrastructureAmbiguous
	}
	return out.Domain.zone(credential.AccountID), nil
}

type infrastructureRecord struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	Data string `json:"data"`
	TTL  int    `json:"ttl"`
}

func (r infrastructureRecord) record(zoneID string) provider.DNSRecord {
	name := r.Name
	if name == "@" {
		name = ""
	}
	return provider.DNSRecord{ID: strconv.FormatInt(r.ID, 10), ZoneID: zoneID, Name: name, Type: r.Type, Value: r.Data, TTL: r.TTL}
}
func (c *Client) checkInfrastructureZone(ctx context.Context, credential provider.InfrastructureCredential, zone provider.DNSZone) error {
	if zone.AccountID != credential.AccountID || zone.ID != zone.Name {
		return provider.ErrInfrastructureIdentity
	}
	found, err := c.GetZone(ctx, credential, zone.Name)
	if err != nil {
		return err
	}
	if found.ID != zone.ID {
		return provider.ErrInfrastructureIdentity
	}
	return nil
}

// ListRecords reads all supported and foreign records so callers can detect
// ownership conflicts. Unsupported record types remain visible but immutable.
func (c *Client) ListRecords(ctx context.Context, credential provider.InfrastructureCredential, zone provider.DNSZone) ([]provider.DNSRecord, error) {
	if err := c.checkInfrastructureZone(ctx, credential, zone); err != nil {
		return nil, err
	}
	result := []provider.DNSRecord{}
	seen := map[int64]bool{}
	for page := 1; page <= infrastructureMaxPages; page++ {
		var out struct {
			Records []infrastructureRecord `json:"domain_records"`
			Links   infrastructureLinks    `json:"links"`
		}
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(pageSize)}}
		if err := c.do(ctx, credential.Token, http.MethodGet, "/v2/domains/"+url.PathEscape(zone.Name)+"/records", q, nil, &out); err != nil {
			return nil, infrastructureError(err, "domain:read")
		}
		for _, r := range out.Records {
			if r.ID < 1 || seen[r.ID] {
				return nil, provider.ErrInfrastructurePagination
			}
			seen[r.ID] = true
			result = append(result, r.record(zone.ID))
		}
		if out.Links.Pages.Next == "" {
			return result, nil
		}
	}
	return nil, provider.ErrInfrastructurePagination
}

// UpsertRecord changes only the record whose persisted prior receipt still
// matches. DigitalOcean has no record comments, so unknown creates stay ambiguous.
func (c *Client) UpsertRecord(ctx context.Context, credential provider.InfrastructureCredential, req provider.DNSUpsertRequest) (provider.DNSRecord, error) {
	records, err := c.ListRecords(ctx, credential, req.Zone)
	if err != nil {
		return provider.DNSRecord{}, err
	}
	prior, done, err := provider.PlanDNSUpsert(req, records, false)
	if err != nil || done {
		return prior, err
	}
	method, path := http.MethodPost, "/v2/domains/"+url.PathEscape(req.Zone.Name)+"/records"
	permission := "domain:create"
	if req.Expected != nil {
		if idErr := infrastructureID(prior.ID); idErr != nil {
			return provider.DNSRecord{}, idErr
		}
		method, path, permission = http.MethodPut, path+"/"+prior.ID, "domain:update"
	}
	name := req.Record.Name
	if name == "" {
		name = "@"
	}
	in := struct {
		Name string `json:"name"`
		Type string `json:"type"`
		Data string `json:"data"`
		TTL  int    `json:"ttl"`
	}{Name: name, Type: req.Record.Type, Data: req.Record.Value, TTL: req.Record.TTL}
	var out struct {
		Record infrastructureRecord `json:"domain_record"`
	}
	if err = c.do(ctx, credential.Token, method, path, nil, in, &out); err != nil {
		return provider.DNSRecord{}, errors.Join(provider.ErrInfrastructureAmbiguous, infrastructureError(err, permission))
	}
	actual := out.Record.record(req.Zone.ID)
	desired := req.Record
	if desired.ID == "" {
		desired.ID = actual.ID
	}
	if out.Record.ID < 1 || actual != desired {
		return actual, provider.ErrInfrastructureAmbiguous
	}
	return actual, nil
}

// DeleteRecord refuses drift and is idempotent when the recorded ID is absent.
func (c *Client) DeleteRecord(ctx context.Context, credential provider.InfrastructureCredential, req provider.DNSDeleteRequest) error {
	if err := infrastructureID(req.Expected.ID); err != nil {
		return err
	}
	if req.Expected.ZoneID != req.Zone.ID {
		return provider.ErrInfrastructureDrift
	}
	if err := provider.ValidateDNSRecord(req.Expected); err != nil {
		return err
	}
	records, err := c.ListRecords(ctx, credential, req.Zone)
	if err != nil {
		return err
	}
	found := false
	for _, r := range records {
		if r.ID == req.Expected.ID {
			if priorErr := provider.MatchDNSPrior(req.Expected, r); priorErr != nil {
				return priorErr
			}
			found = true
		}
	}
	if !found {
		return nil
	}
	err = infrastructureError(c.do(ctx, credential.Token, http.MethodDelete, "/v2/domains/"+url.PathEscape(req.Zone.Name)+"/records/"+req.Expected.ID, nil, nil, nil), "domain:delete")
	if errors.Is(err, provider.ErrInfrastructureNotFound) {
		return nil
	}
	if err != nil {
		return errors.Join(provider.ErrInfrastructureAmbiguous, err)
	}
	return nil
}
