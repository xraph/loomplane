// Package cloudflare talks to Cloudflare's REST API for Loomplane: it
// checks an API token and what it may do, and is the one client DNS and
// domain registration go through.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

// DefaultEndpoint is Cloudflare's API.
const DefaultEndpoint = "https://api.cloudflare.com/client/v4"

const (
	requestTimeout = 60 * time.Second
	maxResponse    = 4 << 20
	maxToken       = 512
)

// Errors.
var (
	// ErrInvalidToken is something that can't be a Cloudflare API token.
	ErrInvalidToken = errors.New("that isn't a Cloudflare API token")
	// ErrRejected is a token Cloudflare refused: expired, revoked, not
	// active or without access to the account.
	ErrRejected = errors.New("cloudflare refused the API token")
)

// Client calls Cloudflare's API.
type Client struct {
	endpoint         string
	registrarSandbox bool
	http             *http.Client
	pollInterval     time.Duration
}

// New returns a Client. An empty endpoint means Cloudflare's own.
// registrarSandbox points registrar calls at the sandbox, which never
// charges anyone.
func New(endpoint string, registrarSandbox bool) *Client {
	// A redirect is an answer, not a second request: following one would
	// send a POST again, or turn it into a GET whose refusal means nothing.
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c := &Client{endpoint: DefaultEndpoint, registrarSandbox: registrarSandbox, http: &http.Client{Timeout: requestTimeout, CheckRedirect: noRedirect}}
	if endpoint != "" {
		c.endpoint = strings.TrimRight(endpoint, "/")
	}
	return c
}

// RegistrarMode says which registrar the client is built for: production
// or sandbox.
func (c *Client) RegistrarMode() string {
	if c.registrarSandbox {
		return "sandbox"
	}
	return "production"
}

// registrarPath is the account's registrar API.
func (c *Client) registrarPath(accountID string) string {
	segment := "registrar"
	if c.registrarSandbox {
		segment = "registrar-sandbox"
	}
	return "/accounts/" + url.PathEscape(accountID) + "/" + segment
}

// CheckToken tells whether s could be a token, without asking Cloudflare.
func CheckToken(s string) error {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxToken || strings.ContainsAny(s, " \t\r\n") {
		return ErrInvalidToken
	}
	return nil
}

// APIError is an error response from Cloudflare. It keeps the status and
// Cloudflare's numeric error codes, never the message or body.
type APIError struct {
	Status int
	Codes  []int
}

func (e *APIError) Error() string {
	if len(e.Codes) == 0 {
		return fmt.Sprintf("cloudflare answered %d", e.Status)
	}
	return fmt.Sprintf("cloudflare answered %d (code %v)", e.Status, e.Codes)
}

// statusOf is the HTTP status of a Cloudflare error response, or 0.
func statusOf(err error) int {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

// envelope is the shape of every Cloudflare response.
type envelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code int `json:"code"`
	} `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo struct {
		Page       int    `json:"page"`
		TotalPages int    `json:"total_pages"`
		Cursor     string `json:"cursor"`
	} `json:"result_info"`
}

// do sends a request for token and decodes the envelope's result into out.
func (c *Client) do(ctx context.Context, token, method, path string, query url.Values, body io.Reader, out any) error {
	_, err := c.send(ctx, token, method, path, query, body, out)
	return err
}

// pageInfo is where a page sits among a list's pages, the cursor to the
// next one (empty at the end) and the HTTP status of the answer.
type pageInfo struct {
	Page, TotalPages int
	Cursor           string
	Status           int
}

// errNotSent marks an error from before the request left the client, so a
// caller of a mutation can tell it from one that may have reached Cloudflare.
var errNotSent = errors.New("request not sent")

// send is do, and also reports the page the answer was for.
func (c *Client) send(ctx context.Context, token, method, path string, query url.Values, body io.Reader, out any) (pageInfo, error) {
	u := c.endpoint + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return pageInfo{}, fmt.Errorf("%w: %w", errNotSent, err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		// The URL error can carry the request; keep only its cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return pageInfo{}, fmt.Errorf("cloudflare: %w", err)
	}
	defer res.Body.Close() // read-only; errors surface through ReadAll
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponse))
	if err != nil {
		return pageInfo{}, fmt.Errorf("cloudflare: read response: %w", err)
	}
	var env envelope
	decoded := json.Unmarshal(data, &env) == nil
	if res.StatusCode >= 300 || (decoded && !env.Success) {
		ae := &APIError{Status: res.StatusCode}
		for _, e := range env.Errors {
			ae.Codes = append(ae.Codes, e.Code)
		}
		if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			return pageInfo{}, fmt.Errorf("%w: %w", ErrRejected, ae)
		}
		return pageInfo{}, ae
	}
	info := pageInfo{Page: env.ResultInfo.Page, TotalPages: env.ResultInfo.TotalPages, Cursor: env.ResultInfo.Cursor, Status: res.StatusCode}
	if out == nil || !decoded || len(env.Result) == 0 || string(env.Result) == "null" {
		return info, nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return pageInfo{}, fmt.Errorf("cloudflare: decode response: %w", err)
	}
	return info, nil
}

func (c *Client) json(ctx context.Context, token, method, path string, query url.Values, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(data)
	}
	return c.do(ctx, token, method, path, query, body, out)
}

// TokenInfo is what Cloudflare says about a token.
type TokenInfo struct {
	ID     string
	Status string
}

// VerifyToken asks Cloudflare whether token is active. An account-owned
// token verifies under its account; a user token only under /user, so
// that's tried when the account path refuses.
func (c *Client) VerifyToken(ctx context.Context, token, accountID string) (TokenInfo, error) {
	var res struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	err := c.json(ctx, token, http.MethodGet, "/accounts/"+url.PathEscape(accountID)+"/tokens/verify", nil, nil, &res)
	if statusOf(err) != 0 {
		res.ID, res.Status = "", ""
		err = c.json(ctx, token, http.MethodGet, "/user/tokens/verify", nil, nil, &res)
	}
	if err != nil {
		return TokenInfo{}, err
	}
	if res.Status != "active" {
		return TokenInfo{}, fmt.Errorf("%w: the token isn't active", ErrRejected)
	}
	if res.ID == "" {
		return TokenInfo{}, errors.New("cloudflare token response did not include a token id")
	}
	return TokenInfo{ID: res.ID, Status: res.Status}, nil
}

// Account is a Cloudflare account.
type Account struct {
	ID   string
	Name string
}

// Account reads the account a token acts for, and confirms it's the one
// asked for.
func (c *Client) Account(ctx context.Context, token, accountID string) (Account, error) {
	var res struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := c.json(ctx, token, http.MethodGet, "/accounts/"+url.PathEscape(accountID), nil, nil, &res); err != nil {
		return Account{}, err
	}
	if res.ID != accountID {
		return Account{}, errors.New("cloudflare account response did not confirm the requested account id")
	}
	return Account{ID: res.ID, Name: res.Name}, nil
}

// PermissionState is what a permission probe found.
type PermissionState string

// Permission states.
const (
	Granted   PermissionState = "granted"
	Missing   PermissionState = "missing"
	Unchecked PermissionState = "unchecked"
)

// Permissions are what a token may do, found without changing anything.
type Permissions struct {
	ZoneRead       PermissionState
	DNSWrite       PermissionState
	RegistrarRead  PermissionState
	RegistrarWrite PermissionState
}

// Permissions probes the token. Reads are plain lists. The DNS write sends
// a record no zone accepts, so Cloudflare checks permission first and then
// refuses the payload: 403 is a missing permission, 400 one that's held.
// The registrar write edits a registration under .invalid, which nobody
// can ever register: 403 is missing, 404 or 400 held. A PATCH can't buy
// anything, so the registration POST is only ever sent for an approved
// purchase. A write that succeeds anyway is a bug in the probe, and
// ErrInfrastructureAmbiguous.
//
// Registrar access is optional, so a 429, a 5xx or a network failure on a
// registrar probe leaves it unchecked and the rest of the answer stands.
func (c *Client) Permissions(ctx context.Context, token, accountID string) (Permissions, error) {
	return c.permissions(ctx, token, accountID, "")
}

// ZonePermissions is Permissions with the DNS Write probe aimed at zoneID
// rather than the first zone the token lists.
func (c *Client) ZonePermissions(ctx context.Context, token, accountID, zoneID string) (Permissions, error) {
	if zoneID == "" {
		return Permissions{}, provider.ErrInfrastructureInvalid
	}
	return c.permissions(ctx, token, accountID, zoneID)
}

func (c *Client) permissions(ctx context.Context, token, accountID, zoneID string) (Permissions, error) {
	p := Permissions{ZoneRead: Unchecked, DNSWrite: Unchecked, RegistrarRead: Unchecked, RegistrarWrite: Unchecked}
	var zones []struct {
		ID string `json:"id"`
	}
	err := c.json(ctx, token, http.MethodGet, "/zones", url.Values{"account.id": {accountID}, "per_page": {"1"}}, nil, &zones)
	if p.ZoneRead, err = probeState(err); err != nil {
		return Permissions{}, err
	}
	if zoneID == "" && p.ZoneRead == Granted && len(zones) > 0 {
		zoneID = zones[0].ID
	}
	if zoneID != "" {
		if p.DNSWrite, err = c.probeDNSWrite(ctx, token, zoneID); err != nil {
			return Permissions{}, err
		}
	}

	registrar := c.registrarPath(accountID)
	err = c.json(ctx, token, http.MethodGet, registrar+"/registrations", url.Values{"per_page": {"1"}}, nil, nil)
	if p.RegistrarRead, err = registrarProbeState(ctx, err); err != nil {
		return Permissions{}, err
	}
	var edited json.RawMessage
	err = c.json(ctx, token, http.MethodPatch, registrar+"/registrations/"+registrarProbeDomain, nil, map[string]bool{"auto_renew": false}, &edited)
	if err == nil {
		// No such registration can exist, so there's nothing to undo, but
		// the outcome isn't one to trust.
		return Permissions{}, fmt.Errorf("%w: the registrar accepted an edit of %s", provider.ErrInfrastructureAmbiguous, registrarProbeDomain)
	}
	if statusOf(err) == http.StatusNotFound {
		// The token may edit registrations; this one just doesn't exist.
		p.RegistrarWrite = Granted
	} else if p.RegistrarWrite, err = registrarProbeState(ctx, err); err != nil {
		return Permissions{}, err
	}
	return p, nil
}

// registrarProbeDomain is under .invalid, which can never be registered.
const registrarProbeDomain = "loomplane-permission-probe.invalid"

// registrarProbeState is probeState for the optional registrar probes:
// Cloudflare trouble leaves the permission unchecked rather than failing
// the check. A 401 is still a refusal, and so is a cancelled check.
func registrarProbeState(ctx context.Context, err error) (PermissionState, error) {
	state, err := probeState(err)
	if err != nil && ctx.Err() == nil && statusOf(err) != http.StatusUnauthorized {
		return Unchecked, nil
	}
	return state, err
}

// probeState reads a probe's answer. A list that succeeds is a grant,
// since it changes nothing (callers handle a write that succeeds). A 401,
// a 429, a 5xx or a network failure is an error, not an answer; any other
// refusal says nothing about the permission.
func probeState(err error) (PermissionState, error) {
	switch status := statusOf(err); {
	case err == nil:
		return Granted, nil
	case status == http.StatusForbidden:
		return Missing, nil
	case status == http.StatusBadRequest:
		return Granted, nil
	case status == 0 || status == http.StatusUnauthorized || status == http.StatusTooManyRequests || status >= 500:
		return Unchecked, err
	}
	return Unchecked, nil
}

// probeDNSWrite posts a record no zone accepts. Should Cloudflare create
// it anyway, it's deleted by its exact ID.
func (c *Client) probeDNSWrite(ctx context.Context, token, zoneID string) (PermissionState, error) {
	path := "/zones/" + url.PathEscape(zoneID) + "/dns_records"
	probe := map[string]any{"type": "A", "name": "_loomplane-permission-probe", "content": "permission-probe", "ttl": 60, "proxied": false}
	var created struct {
		ID string `json:"id"`
	}
	err := c.json(ctx, token, http.MethodPost, path, nil, probe, &created)
	if err != nil {
		return probeState(err)
	}
	ambiguous := fmt.Errorf("%w: the DNS permission probe created a record", provider.ErrInfrastructureAmbiguous)
	if created.ID == "" {
		return Unchecked, ambiguous
	}
	// The caller may have given up; the cleanup still runs.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if derr := c.json(cleanup, token, http.MethodDelete, path+"/"+url.PathEscape(created.ID), nil, nil, nil); derr != nil {
		return Unchecked, errors.Join(ambiguous, derr)
	}
	return Unchecked, ambiguous
}

// ChallengeScope is what a DNS challenge token reaches: every zone it
// lists, and its permissions with DNS Write probed on one zone.
type ChallengeScope struct {
	ZoneIDs     []string
	Permissions Permissions
}

// ChallengeScope checks a token meant for one zone's DNS-01 challenges. It
// verifies the token, lists every zone it can see in the account and probes
// DNS Write on zoneID, all without changing anything.
func (c *Client) ChallengeScope(ctx context.Context, credential provider.InfrastructureCredential, zoneID string) (ChallengeScope, error) {
	if _, err := c.VerifyToken(ctx, credential.Token, credential.AccountID); err != nil {
		return ChallengeScope{}, err
	}
	zones, err := c.ListZones(ctx, credential)
	if err != nil {
		return ChallengeScope{}, err
	}
	out := ChallengeScope{ZoneIDs: make([]string, 0, len(zones))}
	for _, zone := range zones {
		out.ZoneIDs = append(out.ZoneIDs, zone.ID)
	}
	if out.Permissions, err = c.ZonePermissions(ctx, credential.Token, credential.AccountID, zoneID); err != nil {
		return ChallengeScope{}, err
	}
	return out, nil
}
