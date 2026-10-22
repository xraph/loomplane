package vercel

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/pkg/provider"
)

const infrastructureMaxPages = 100

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
			return &provider.InfrastructureScopeError{Provider: "vercel", Permission: permission}
		case http.StatusNotFound:
			return provider.ErrInfrastructureNotFound
		default:
			return &provider.InfrastructureAPIError{Provider: "vercel", Status: api.Status}
		}
	}
	return &provider.InfrastructureAPIError{Provider: "vercel"}
}
func (c *Client) infrastructureIdentity(ctx context.Context, credential provider.InfrastructureCredential) (Token, error) {
	tok := Token{Value: credential.Token, TeamID: credential.TeamID}
	if err := ctx.Err(); err != nil {
		return Token{}, err
	}
	if credential.Provider != "vercel" || credential.AccountID == "" || CheckToken(credential.Token) != nil {
		return Token{}, provider.ErrInfrastructureInvalid
	}
	a, err := c.Check(ctx, tok)
	if err != nil {
		return Token{}, infrastructureError(err, "account/team read")
	}
	id := a.UserID
	if tok.TeamID != "" {
		id = a.TeamID
	}
	if id == "" || id != credential.AccountID {
		return Token{}, provider.ErrInfrastructureIdentity
	}
	return tok, nil
}

type infrastructureDomain struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	TeamID      string   `json:"teamId"`
	UserID      string   `json:"userId"`
	Nameservers []string `json:"nameservers"`
	Verified    bool     `json:"verified"`
	ServiceType string   `json:"serviceType"`
}

func (d infrastructureDomain) zone(credential provider.InfrastructureCredential) (provider.DNSZone, error) {
	if d.ID == "" || provider.ValidateDNSName(d.Name, false) != nil {
		return provider.DNSZone{}, provider.ErrInfrastructureIdentity
	}
	if credential.TeamID != "" {
		if d.TeamID != credential.TeamID {
			return provider.DNSZone{}, provider.ErrInfrastructureIdentity
		}
	} else if d.TeamID != "" || d.UserID != credential.AccountID {
		return provider.DNSZone{}, provider.ErrInfrastructureIdentity
	}
	return provider.DNSZone{ID: d.ID, Name: d.Name, AccountID: credential.AccountID, Nameservers: d.Nameservers, Verified: d.Verified && d.ServiceType == "zeit.world"}, nil
}

type infrastructurePagination struct {
	Next *int64 `json:"next"`
}

func (p infrastructurePagination) cursor(previous string) (string, error) {
	if p.Next == nil {
		return "", nil
	}
	if *p.Next < 0 {
		return "", provider.ErrInfrastructurePagination
	}
	next := strconv.FormatInt(*p.Next, 10)
	if previous != "" {
		prior, err := strconv.ParseInt(previous, 10, 64)
		if err != nil {
			return "", provider.ErrInfrastructurePagination
		}
		if *p.Next >= prior {
			return "", provider.ErrInfrastructurePagination
		}
	}
	return next, nil
}

// ListZones discovers only domains in the verified user or team scope.
func (c *Client) ListZones(ctx context.Context, credential provider.InfrastructureCredential) ([]provider.DNSZone, error) {
	tok, err := c.infrastructureIdentity(ctx, credential)
	if err != nil {
		return nil, err
	}
	result := []provider.DNSZone{}
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < infrastructureMaxPages; page++ {
		var out struct {
			Domains    []infrastructureDomain   `json:"domains"`
			Pagination infrastructurePagination `json:"pagination"`
		}
		q := url.Values{"limit": {"100"}}
		if cursor != "" {
			q.Set("until", cursor)
		}
		if err = c.json(ctx, tok, http.MethodGet, "/v5/domains", q, nil, &out); err != nil {
			return nil, infrastructureError(err, "domains read")
		}
		for _, d := range out.Domains {
			zone, zoneErr := d.zone(credential)
			if zoneErr != nil {
				return nil, zoneErr
			}
			if seen[zone.ID] {
				return nil, provider.ErrInfrastructurePagination
			}
			seen[zone.ID] = true
			result = append(result, zone)
		}
		cursor, err = out.Pagination.cursor(cursor)
		if err != nil {
			return nil, err
		}
		if cursor == "" {
			return result, nil
		}
	}
	return nil, provider.ErrInfrastructurePagination
}

// GetZone confirms the owning scope and immutable domain ID. Verified indicates
// verified Vercel DNS hosting, not a fresh external delegation or TLS probe.
func (c *Client) GetZone(ctx context.Context, credential provider.InfrastructureCredential, name string) (provider.DNSZone, error) {
	if err := provider.ValidateDNSName(name, false); err != nil {
		return provider.DNSZone{}, err
	}
	tok, err := c.infrastructureIdentity(ctx, credential)
	if err != nil {
		return provider.DNSZone{}, err
	}
	var out struct {
		Domain infrastructureDomain `json:"domain"`
	}
	if err = c.json(ctx, tok, http.MethodGet, "/v5/domains/"+url.PathEscape(name), nil, nil, &out); err != nil {
		return provider.DNSZone{}, infrastructureError(err, "domains read")
	}
	if out.Domain.Name != name {
		return provider.DNSZone{}, provider.ErrInfrastructureIdentity
	}
	return out.Domain.zone(credential)
}

// CreateZone adds DNS hosting with method add. Registrar transfers, nameserver
// changes, purchases and project attachment are never part of this operation.
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
		Name   string `json:"name"`
		Method string `json:"method"`
		Zone   bool   `json:"zone"`
	}{Name: spec.Name, Method: "add", Zone: true}
	var out struct {
		Domain infrastructureDomain `json:"domain"`
	}
	tok := Token{Value: credential.Token, TeamID: credential.TeamID}
	if err = c.json(ctx, tok, http.MethodPost, "/v7/domains", nil, in, &out); err != nil {
		return provider.DNSZone{}, errors.Join(provider.ErrInfrastructureAmbiguous, infrastructureError(err, "domains write"))
	}
	if out.Domain.Name != spec.Name {
		return provider.DNSZone{}, provider.ErrInfrastructureAmbiguous
	}
	result, err := out.Domain.zone(credential)
	if err != nil {
		return provider.DNSZone{}, errors.Join(provider.ErrInfrastructureAmbiguous, err)
	}
	return result, nil
}

type infrastructureRecord struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	RecordType string `json:"recordType"`
	Value      string `json:"value"`
	TTL        int    `json:"ttl"`
	Comment    string `json:"comment"`
	Domain     string `json:"domain"`
}

func (r infrastructureRecord) record(zoneID string) provider.DNSRecord {
	kind := r.Type
	if r.RecordType != "" {
		kind = r.RecordType
	}
	return provider.DNSRecord{ID: r.ID, ZoneID: zoneID, Name: r.Name, Type: kind, Value: r.Value, TTL: r.TTL, Owner: r.Comment}
}

func (c *Client) checkInfrastructureZone(ctx context.Context, credential provider.InfrastructureCredential, zone provider.DNSZone) error {
	if zone.AccountID != credential.AccountID || zone.ID == "" {
		return provider.ErrInfrastructureIdentity
	}
	actual, err := c.GetZone(ctx, credential, zone.Name)
	if err != nil {
		return err
	}
	if actual.ID != zone.ID {
		return provider.ErrInfrastructureIdentity
	}
	if !actual.Verified {
		return &provider.InfrastructureScopeError{Provider: "vercel", Permission: "verified domain using Vercel DNS"}
	}
	return nil
}

// ListRecords reads the exact zone with team scope on every numeric cursor page.
func (c *Client) ListRecords(ctx context.Context, credential provider.InfrastructureCredential, zone provider.DNSZone) ([]provider.DNSRecord, error) {
	if err := c.checkInfrastructureZone(ctx, credential, zone); err != nil {
		return nil, err
	}
	tok := Token{Value: credential.Token, TeamID: credential.TeamID}
	result := []provider.DNSRecord{}
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < infrastructureMaxPages; page++ {
		var out struct {
			Records    []infrastructureRecord   `json:"records"`
			Pagination infrastructurePagination `json:"pagination"`
		}
		q := url.Values{"limit": {"100"}}
		if cursor != "" {
			q.Set("until", cursor)
		}
		if err := c.json(ctx, tok, http.MethodGet, "/v5/domains/"+url.PathEscape(zone.Name)+"/records", q, nil, &out); err != nil {
			return nil, infrastructureError(err, "DNS read")
		}
		for _, r := range out.Records {
			if r.ID == "" || seen[r.ID] {
				return nil, provider.ErrInfrastructurePagination
			}
			if r.Domain != "" && r.Domain != zone.Name {
				return nil, provider.ErrInfrastructureIdentity
			}
			seen[r.ID] = true
			result = append(result, r.record(zone.ID))
		}
		next, err := out.Pagination.cursor(cursor)
		if err != nil {
			return nil, err
		}
		cursor = next
		if cursor == "" {
			return result, nil
		}
	}
	return nil, provider.ErrInfrastructurePagination
}

// UpsertRecord uses owner comments to reconcile a previously accepted create.
// Existing record IDs and exact prior values are mandatory for an update.
func (c *Client) UpsertRecord(ctx context.Context, credential provider.InfrastructureCredential, req provider.DNSUpsertRequest) (provider.DNSRecord, error) {
	records, err := c.ListRecords(ctx, credential, req.Zone)
	if err != nil {
		return provider.DNSRecord{}, err
	}
	if req.ReconcileOnly && req.Expected != nil {
		return reconcileDNSUpdate(req, records)
	}
	prior, done, err := provider.PlanDNSUpsert(req, records, true)
	if err != nil || done {
		return prior, err
	}
	tok := Token{Value: credential.Token, TeamID: credential.TeamID}
	in := struct {
		Name    string `json:"name"`
		Type    string `json:"type"`
		Value   string `json:"value"`
		TTL     int    `json:"ttl"`
		Comment string `json:"comment"`
	}{Name: req.Record.Name, Type: req.Record.Type, Value: req.Record.Value, TTL: req.Record.TTL, Comment: req.Record.Owner}
	if req.Expected == nil {
		var out struct {
			UID string `json:"uid"`
		}
		if err = c.json(ctx, tok, http.MethodPost, "/v2/domains/"+url.PathEscape(req.Zone.Name)+"/records", nil, in, &out); err != nil {
			return provider.DNSRecord{}, errors.Join(provider.ErrInfrastructureAmbiguous, infrastructureError(err, "DNS write"))
		}
		if out.UID == "" {
			return provider.DNSRecord{}, provider.ErrInfrastructureAmbiguous
		}
		actual := req.Record
		actual.ID = out.UID
		return actual, nil
	}
	if prior.ID == "" || !strings.HasPrefix(prior.Owner, "loomplane:") {
		return provider.DNSRecord{}, provider.ErrInfrastructureDrift
	}
	var out infrastructureRecord
	if err = c.json(ctx, tok, http.MethodPatch, "/v1/domains/records/"+url.PathEscape(prior.ID), nil, in, &out); err != nil {
		return provider.DNSRecord{}, errors.Join(provider.ErrInfrastructureAmbiguous, infrastructureError(err, "DNS write"))
	}
	actual := out.record(req.Zone.ID)
	desired := req.Record
	desired.ID = actual.ID
	if actual.ID == "" || out.Domain != req.Zone.Name || actual != desired {
		return actual, provider.ErrInfrastructureAmbiguous
	}
	return actual, nil
}

// reconcileDNSUpdate recognizes a provider-rotated ID only from the exact
// persisted owner and intended state. It never infers ownership from value alone
// and never submits another mutation while its outcome remains unresolved.
func reconcileDNSUpdate(req provider.DNSUpsertRequest, records []provider.DNSRecord) (provider.DNSRecord, error) {
	if _, _, err := provider.PlanDNSUpsert(req, []provider.DNSRecord{*req.Expected}, true); err != nil {
		return provider.DNSRecord{}, err
	}
	var recovered provider.DNSRecord
	priorPresent := false
	for _, current := range records {
		if current.ID == req.Expected.ID && current.Name != req.Record.Name {
			return provider.DNSRecord{}, provider.ErrInfrastructureDrift
		}
		if current.Name != req.Record.Name || (current.Type != req.Record.Type && current.Type != "CNAME" && req.Record.Type != "CNAME") {
			continue
		}
		desired := req.Record
		desired.ID = current.ID
		switch current {
		case desired:
			if recovered.ID != "" {
				return provider.DNSRecord{}, provider.ErrInfrastructureDrift
			}
			recovered = current
		case *req.Expected:
			priorPresent = true
		default:
			return provider.DNSRecord{}, provider.ErrInfrastructureDrift
		}
	}
	if recovered.ID != "" {
		if priorPresent {
			return provider.DNSRecord{}, provider.ErrInfrastructureDrift
		}
		return recovered, nil
	}
	return provider.DNSRecord{}, provider.ErrInfrastructureAmbiguous
}

// DeleteRecord confirms exact ownership in the requested zone before calling
// the zone-scoped delete endpoint. Vercel documents HTTP 200 with no object.
func (c *Client) DeleteRecord(ctx context.Context, credential provider.InfrastructureCredential, req provider.DNSDeleteRequest) error {
	if req.Expected.ID == "" || req.Expected.ZoneID != req.Zone.ID || !strings.HasPrefix(req.Expected.Owner, "loomplane:") {
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
	tok := Token{Value: credential.Token, TeamID: credential.TeamID}
	err = infrastructureError(c.json(ctx, tok, http.MethodDelete, "/v2/domains/"+url.PathEscape(req.Zone.Name)+"/records/"+url.PathEscape(req.Expected.ID), nil, nil, nil), "DNS write")
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

func (*Client) SupportsDNSRecordOwnership() bool { return true }
