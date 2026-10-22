package cloudflare

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/pkg/provider"
)

const (
	infrastructureMaxPages = 100
	pageSize               = 100
	// automaticTTL is what Cloudflare calls "Auto". It reads back as
	// defaultTTL, and a record on it was never one Loomplane wrote.
	automaticTTL = 1
	defaultTTL   = 300
)

var _ provider.DNSAdapter = (*Client)(nil)

func infrastructureError(err error, permission string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var api *APIError
	if errors.As(err, &api) {
		switch api.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return &provider.InfrastructureScopeError{Provider: "cloudflare", Permission: permission}
		case http.StatusNotFound:
			return provider.ErrInfrastructureNotFound
		default:
			return &provider.InfrastructureAPIError{Provider: "cloudflare", Status: api.Status}
		}
	}
	return &provider.InfrastructureAPIError{Provider: "cloudflare"}
}

// checkCredential refuses a credential that isn't a Cloudflare account's.
func checkCredential(ctx context.Context, credential provider.InfrastructureCredential) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if credential.Provider != "cloudflare" || credential.AccountID == "" || CheckToken(credential.Token) != nil {
		return provider.ErrInfrastructureInvalid
	}
	return nil
}

type infrastructureZone struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	NameServers []string `json:"name_servers"`
	Account     struct {
		ID string `json:"id"`
	} `json:"account"`
}

// zone is the zone, once it's known to belong to the credential's account.
func (z infrastructureZone) zone(credential provider.InfrastructureCredential) (provider.DNSZone, error) {
	if z.ID == "" || provider.ValidateDNSName(z.Name, false) != nil || z.Account.ID != credential.AccountID {
		return provider.DNSZone{}, provider.ErrInfrastructureIdentity
	}
	return provider.DNSZone{ID: z.ID, Name: z.Name, AccountID: credential.AccountID, Nameservers: z.NameServers, Verified: z.Status == "active"}, nil
}

// eachPage reads a list page by page, handing each page's items to add. It
// stops at the last page and gives up after infrastructureMaxPages.
func eachPage[T any](ctx context.Context, c *Client, token, path string, query url.Values, permission string, add func([]T) error) error {
	for page := 1; page <= infrastructureMaxPages; page++ {
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(pageSize)}}
		for k, v := range query {
			q[k] = v
		}
		var items []T
		info, err := c.send(ctx, token, http.MethodGet, path, q, nil, &items)
		if err != nil {
			return infrastructureError(err, permission)
		}
		if err = add(items); err != nil {
			return err
		}
		if info.Page >= info.TotalPages {
			return nil
		}
	}
	return provider.ErrInfrastructurePagination
}

// ListZones discovers the account's zones without touching delegation or
// the registrar.
func (c *Client) ListZones(ctx context.Context, credential provider.InfrastructureCredential) ([]provider.DNSZone, error) {
	if err := checkCredential(ctx, credential); err != nil {
		return nil, err
	}
	result := []provider.DNSZone{}
	seen := map[string]bool{}
	err := eachPage(ctx, c, credential.Token, "/zones", url.Values{"account.id": {credential.AccountID}}, "Zone:Read", func(zones []infrastructureZone) error {
		for _, z := range zones {
			zone, err := z.zone(credential)
			if err != nil {
				return err
			}
			if seen[zone.ID] {
				return provider.ErrInfrastructurePagination
			}
			seen[zone.ID] = true
			result = append(result, zone)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// GetZone reads the zone with this exact name. Verified means Cloudflare
// reports it active, which is when its nameservers answer for it.
func (c *Client) GetZone(ctx context.Context, credential provider.InfrastructureCredential, name string) (provider.DNSZone, error) {
	if err := provider.ValidateDNSName(name, false); err != nil {
		return provider.DNSZone{}, err
	}
	if err := checkCredential(ctx, credential); err != nil {
		return provider.DNSZone{}, err
	}
	var zones []infrastructureZone
	q := url.Values{"name": {name}, "account.id": {credential.AccountID}}
	if err := c.json(ctx, credential.Token, http.MethodGet, "/zones", q, nil, &zones); err != nil {
		return provider.DNSZone{}, infrastructureError(err, "Zone:Read")
	}
	for _, z := range zones {
		if z.Name == name {
			return z.zone(credential)
		}
	}
	return provider.DNSZone{}, provider.ErrInfrastructureNotFound
}

// CreateZone adds an existing domain as a full zone. It never buys the
// domain or changes its registrar nameservers.
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
	type account struct {
		ID string `json:"id"`
	}
	in := struct {
		Name    string  `json:"name"`
		Account account `json:"account"`
		Type    string  `json:"type"`
	}{Name: spec.Name, Account: account{ID: credential.AccountID}, Type: "full"}
	var out infrastructureZone
	if err = c.json(ctx, credential.Token, http.MethodPost, "/zones", nil, in, &out); err != nil {
		return provider.DNSZone{}, errors.Join(provider.ErrInfrastructureAmbiguous, infrastructureError(err, "Zone:Edit"))
	}
	if out.Name != spec.Name {
		return provider.DNSZone{}, provider.ErrInfrastructureAmbiguous
	}
	zone, err := out.zone(credential)
	if err != nil {
		return provider.DNSZone{}, errors.Join(provider.ErrInfrastructureAmbiguous, err)
	}
	return zone, nil
}

// checkZone confirms the zone is the credential's, by its immutable ID.
func (c *Client) checkZone(ctx context.Context, credential provider.InfrastructureCredential, zone provider.DNSZone) error {
	if err := checkCredential(ctx, credential); err != nil {
		return err
	}
	if zone.AccountID != credential.AccountID || zone.ID == "" {
		return provider.ErrInfrastructureIdentity
	}
	var actual infrastructureZone
	if err := c.json(ctx, credential.Token, http.MethodGet, "/zones/"+url.PathEscape(zone.ID), nil, nil, &actual); err != nil {
		return infrastructureError(err, "Zone:Read")
	}
	if actual.ID != zone.ID || actual.Name != zone.Name {
		return provider.ErrInfrastructureIdentity
	}
	_, err := actual.zone(credential)
	return err
}

type infrastructureRecord struct {
	ID      string `json:"id"`
	ZoneID  string `json:"zone_id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment"`
}

// relativeName turns a record's FQDN into the contract's zone-relative
// name: "" for the apex, "*" for a wildcard.
func relativeName(fqdn, zoneName string) (string, bool) {
	switch {
	case fqdn == zoneName:
		return "", true
	case strings.HasSuffix(fqdn, "."+zoneName):
		return strings.TrimSuffix(fqdn, "."+zoneName), true
	}
	return "", false
}

// fqdn is the provider's name for a zone-relative one.
func fqdn(name, zoneName string) string {
	if name == "" {
		return zoneName
	}
	return name + "." + zoneName
}

// record converts a provider record. It is owned only when its comment is
// the owner token, it isn't on automatic TTL and it isn't proxied. A
// proxied record that is the exact persisted receipt (receiptID) is the
// one exception: Loomplane wrote it unproxied, so it is still its own.
func (r infrastructureRecord) record(zone provider.DNSZone, receiptID string) (provider.DNSRecord, error) {
	if r.ZoneID != "" && r.ZoneID != zone.ID {
		return provider.DNSRecord{}, provider.ErrInfrastructureIdentity
	}
	name, ok := relativeName(r.Name, zone.Name)
	if !ok {
		return provider.DNSRecord{}, provider.ErrInfrastructureIdentity
	}
	ttl, owner := r.TTL, r.Comment
	if ttl == automaticTTL {
		ttl, owner = defaultTTL, ""
	}
	if r.Proxied && (receiptID == "" || r.ID != receiptID) {
		owner = ""
	}
	content := r.Content
	if r.Type == "TXT" && len(content) >= 2 && strings.HasPrefix(content, `"`) && strings.HasSuffix(content, `"`) {
		// Cloudflare reads TXT content back in quotes.
		content = content[1 : len(content)-1]
	}
	return provider.DNSRecord{ID: r.ID, ZoneID: zone.ID, Name: name, Type: r.Type, Value: content, TTL: ttl, Owner: owner}, nil
}

// listRecords reads every record of the zone. receiptID is the one record
// that may be owned although proxied. proxied holds the IDs of every
// proxied record, which the contract's records can't show.
func (c *Client) listRecords(ctx context.Context, credential provider.InfrastructureCredential, zone provider.DNSZone, receiptID string) (result []provider.DNSRecord, proxied map[string]bool, err error) {
	if err = c.checkZone(ctx, credential, zone); err != nil {
		return nil, nil, err
	}
	result = []provider.DNSRecord{}
	proxied = map[string]bool{}
	seen := map[string]bool{}
	err = eachPage(ctx, c, credential.Token, "/zones/"+url.PathEscape(zone.ID)+"/dns_records", nil, "DNS:Read", func(records []infrastructureRecord) error {
		for _, r := range records {
			if r.ID == "" || seen[r.ID] {
				return provider.ErrInfrastructurePagination
			}
			seen[r.ID] = true
			proxied[r.ID] = r.Proxied
			record, err := r.record(zone, receiptID)
			if err != nil {
				return err
			}
			result = append(result, record)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return result, proxied, nil
}

// ListRecords reads all records, foreign and proxied ones included, so
// callers can see what already holds a name.
func (c *Client) ListRecords(ctx context.Context, credential provider.InfrastructureCredential, zone provider.DNSZone) ([]provider.DNSRecord, error) {
	records, _, err := c.listRecords(ctx, credential, zone, "")
	return records, err
}

// reconcileUpdate settles an update that may or may not have landed,
// reading only. Cloudflare keeps a record's ID on PATCH, so the record with
// the receipt's ID is the desired state (done), still the prior state (the
// update didn't land, so unresolved) or something else (drift). A proxied
// record is never the desired state.
func reconcileUpdate(req provider.DNSUpsertRequest, records []provider.DNSRecord, proxied map[string]bool) (provider.DNSRecord, error) {
	if _, _, err := provider.PlanDNSUpsert(req, []provider.DNSRecord{*req.Expected}, true); err != nil {
		return provider.DNSRecord{}, err
	}
	for _, current := range records {
		if current.ID != req.Expected.ID {
			continue
		}
		switch {
		case proxied[current.ID]:
			return provider.DNSRecord{}, provider.ErrInfrastructureDrift
		case current == req.Record:
			return current, nil
		case current == *req.Expected:
			return provider.DNSRecord{}, provider.ErrInfrastructureAmbiguous
		}
		return provider.DNSRecord{}, provider.ErrInfrastructureDrift
	}
	return provider.DNSRecord{}, provider.ErrInfrastructureDrift
}

// UpsertRecord creates or updates one record Loomplane owns. A record is
// Loomplane's only when its comment is the owner token, so a record anyone
// else made is drift, and so is any proxied one but the exact receipt.
func (c *Client) UpsertRecord(ctx context.Context, credential provider.InfrastructureCredential, req provider.DNSUpsertRequest) (provider.DNSRecord, error) {
	receipt := ""
	if req.Expected != nil {
		receipt = req.Expected.ID
	}
	records, proxied, err := c.listRecords(ctx, credential, req.Zone, receipt)
	if err != nil {
		return provider.DNSRecord{}, err
	}
	if req.ReconcileOnly && req.Expected != nil {
		return reconcileUpdate(req, records, proxied)
	}
	prior, done, err := provider.PlanDNSUpsert(req, records, true)
	if err != nil {
		return prior, err
	}
	// Loomplane writes records unproxied; one flipped to proxied is not done.
	if done && !(req.Expected != nil && proxied[prior.ID]) {
		return prior, nil
	}
	in := struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		Content string `json:"content"`
		TTL     int    `json:"ttl"`
		Proxied bool   `json:"proxied"`
		Comment string `json:"comment"`
	}{Type: req.Record.Type, Name: fqdn(req.Record.Name, req.Zone.Name), Content: req.Record.Value, TTL: req.Record.TTL, Comment: req.Record.Owner}
	method, path := http.MethodPost, "/zones/"+url.PathEscape(req.Zone.ID)+"/dns_records"
	if req.Expected != nil {
		if prior.ID == "" {
			return provider.DNSRecord{}, provider.ErrInfrastructureDrift
		}
		method, path = http.MethodPatch, path+"/"+url.PathEscape(prior.ID)
	}
	var out infrastructureRecord
	if err = c.json(ctx, credential.Token, method, path, nil, in, &out); err != nil {
		return provider.DNSRecord{}, errors.Join(provider.ErrInfrastructureAmbiguous, infrastructureError(err, "DNS:Edit"))
	}
	// Anything but the record just written, proxied included, is unresolved.
	actual, err := out.record(req.Zone, "")
	if err != nil {
		return provider.DNSRecord{}, errors.Join(provider.ErrInfrastructureAmbiguous, err)
	}
	desired := req.Record
	if desired.ID == "" {
		desired.ID = actual.ID
	}
	if actual.ID == "" || actual != desired {
		return actual, provider.ErrInfrastructureAmbiguous
	}
	return actual, nil
}

// DeleteRecord removes the record the exact receipt names, and nothing
// else. A record already gone is done.
func (c *Client) DeleteRecord(ctx context.Context, credential provider.InfrastructureCredential, req provider.DNSDeleteRequest) error {
	if req.Expected.ID == "" || req.Expected.ZoneID != req.Zone.ID || !strings.HasPrefix(req.Expected.Owner, "loomplane:") {
		return provider.ErrInfrastructureDrift
	}
	if err := provider.ValidateDNSRecord(req.Expected); err != nil {
		return err
	}
	records, _, err := c.listRecords(ctx, credential, req.Zone, req.Expected.ID)
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
	err = infrastructureError(c.json(ctx, credential.Token, http.MethodDelete, "/zones/"+url.PathEscape(req.Zone.ID)+"/dns_records/"+url.PathEscape(req.Expected.ID), nil, nil, nil), "DNS:Edit")
	if errors.Is(err, provider.ErrInfrastructureNotFound) {
		return nil
	}
	if err != nil {
		return errors.Join(provider.ErrInfrastructureAmbiguous, err)
	}
	return nil
}

// InfrastructureCapabilities lists the implemented account APIs.
func InfrastructureCapabilities() []provider.InfrastructureCapability {
	return []provider.InfrastructureCapability{provider.DNSRecordDeletion, provider.DNSRecordDiscovery, provider.DNSRecordUpsert, provider.DNSZoneCreation, provider.DNSZoneDiscovery}
}

// SupportsDNSRecordOwnership says records carry the owner token in their comment.
func (*Client) SupportsDNSRecordOwnership() bool { return true }
