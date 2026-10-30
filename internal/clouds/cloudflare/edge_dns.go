package cloudflare

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/xraph/loomplane/pkg/provider"
)

// Proxied DNS always uses Cloudflare's automatic TTL, represented as 300
// seconds in receipts so handoff draining keeps a useful duration.
func (c *Client) edgeDNS(ctx context.Context, cred provider.InfrastructureCredential, zone provider.DNSZone, id string) (provider.DNSRecord, error) {
	var raw infrastructureRecord
	err := c.json(ctx, cred.Token, http.MethodGet, "/zones/"+url.PathEscape(zone.ID)+"/dns_records/"+url.PathEscape(id), nil, nil, &raw)
	if err != nil {
		return provider.DNSRecord{}, infrastructureError(err, "DNS:Read")
	}
	r, err := raw.record(zone, id)
	if err != nil {
		return r, err
	}
	if raw.ID != id || !strings.HasPrefix(raw.Comment, "loomplane:") {
		return r, provider.ErrInfrastructureDrift
	}
	r.Owner, r.Proxied = raw.Comment, raw.Proxied
	if raw.Proxied {
		r.TTL = 300
	}
	return r, nil
}

func (c *Client) VerifyEdgeDNS(ctx context.Context, cred provider.InfrastructureCredential, zone provider.DNSZone, expected provider.DNSRecord) error {
	r, err := c.edgeDNS(ctx, cred, zone, expected.ID)
	if err != nil {
		return err
	}
	return provider.MatchDNSPrior(expected, r)
}

func (c *Client) upsertEdgeDNS(ctx context.Context, cred provider.InfrastructureCredential, req provider.DNSUpsertRequest) (provider.DNSRecord, error) {
	// A CDN attaches only after the origin's ordinary DNS route is ready.
	if req.Expected == nil || (req.Record.Proxied && req.Record.TTL != 300) {
		return provider.DNSRecord{}, provider.ErrInfrastructureInvalid
	}
	if _, _, err := provider.PlanDNSUpsert(req, []provider.DNSRecord{*req.Expected}, true); err != nil {
		return provider.DNSRecord{}, err
	}
	if req.Record.Type != "A" && req.Record.Type != "AAAA" && req.Record.Type != "CNAME" {
		return provider.DNSRecord{}, provider.ErrInfrastructureInvalid
	}
	current, err := c.edgeDNS(ctx, cred, req.Zone, req.Expected.ID)
	if err != nil {
		return current, err
	}
	if current == req.Record {
		return current, nil
	}
	if checkErr := provider.MatchDNSPrior(*req.Expected, current); checkErr != nil {
		return current, checkErr
	}
	if req.ReconcileOnly {
		return current, provider.ErrInfrastructureAmbiguous
	}
	ttl := req.Record.TTL
	if req.Record.Proxied {
		ttl = 1
	}
	body := infrastructureRecord{Name: fqdn(req.Record.Name, req.Zone.Name), Type: req.Record.Type, Content: req.Record.Value, TTL: ttl, Proxied: req.Record.Proxied, Comment: req.Record.Owner}
	err = c.json(ctx, cred.Token, http.MethodPatch, "/zones/"+url.PathEscape(req.Zone.ID)+"/dns_records/"+url.PathEscape(current.ID), nil, body, nil)
	if err != nil {
		return current, errors.Join(provider.ErrInfrastructureAmbiguous, infrastructureError(err, "DNS:Edit"))
	}
	actual, err := c.edgeDNS(ctx, cred, req.Zone, current.ID)
	if err == nil && actual != req.Record {
		err = provider.ErrInfrastructureAmbiguous
	}
	return actual, err
}

func (c *Client) deleteEdgeDNS(ctx context.Context, cred provider.InfrastructureCredential, req provider.DNSDeleteRequest) error {
	err := c.VerifyEdgeDNS(ctx, cred, req.Zone, req.Expected)
	if errors.Is(err, provider.ErrInfrastructureNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	err = infrastructureError(c.json(ctx, cred.Token, http.MethodDelete, "/zones/"+url.PathEscape(req.Zone.ID)+"/dns_records/"+url.PathEscape(req.Expected.ID), nil, nil, nil), "DNS:Edit")
	if errors.Is(err, provider.ErrInfrastructureNotFound) {
		return nil
	}
	return err
}
