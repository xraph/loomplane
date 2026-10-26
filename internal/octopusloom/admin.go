package octopusloom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/deployments"
)

// Headers Octopus reads its credentials from. Neither is Authorization,
// which the Kubernetes API service proxy strips.
const (
	adminTokenHeader = "X-Octopus-Admin-Token" //nolint:gosec // A header name, not a credential.
	farpTokenHeader  = "X-Octopus-Farp-Token"  //nolint:gosec // A header name, not a credential.
)

const (
	// requestTimeout bounds every request to Octopus.
	requestTimeout = 10 * time.Second
	// maxAnswer bounds what Loomplane reads of one answer.
	maxAnswer = 4 << 20
	// maxErrorBody is how much of an error answer an error carries.
	maxErrorBody = 512
)

// Admin speaks Octopus 0.5's admin API on its private admin port, through a
// component channel. Admin calls send the admin API token in
// X-Octopus-Admin-Token and FARP calls send the FARP token in
// X-Octopus-Farp-Token. Neither token is ever sent as Authorization, and
// neither appears in an error.
type Admin struct {
	ch         *deployments.ComponentChannel
	adminToken []byte
	farpToken  []byte
}

// NewAdmin is an Admin on ch. The caller closes ch.
func NewAdmin(ch *deployments.ComponentChannel, adminToken, farpToken []byte) *Admin {
	return &Admin{ch: ch, adminToken: adminToken, farpToken: farpToken}
}

// SystemInfo is what GET /admin/api/system/info reports that Loomplane
// reads. StartTime changes when Octopus restarts.
type SystemInfo struct {
	Version   string `json:"version"`
	StartTime string `json:"start_time"`
}

// RouteInfo is one entry of GET /admin/api/routes. Octopus reports one per
// method and host of a route; RouteID is the id they share. A nil Host is
// any host and a nil StripPrefix strips nothing.
type RouteInfo struct {
	ID          string  `json:"id"`
	RouteID     *string `json:"route_id"`
	Origin      string  `json:"origin"` // "config" | "admin" | "farp" | "kubernetes"
	Host        *string `json:"host"`
	Path        string  `json:"path"`
	Method      string  `json:"method"`
	Upstream    string  `json:"upstream"`
	StripPrefix *string `json:"strip_prefix"`
}

// RouteBody is the body of PUT /admin/api/routes/{id}. Empty Methods is
// every method and empty Hosts is any host; both are sent as [] then.
type RouteBody struct {
	Path        string   `json:"path"`
	Methods     []string `json:"methods"`
	Hosts       []string `json:"hosts"`
	Upstream    string   `json:"upstream"`
	StripPrefix string   `json:"strip_prefix,omitempty"`
	Priority    int      `json:"priority"`
}

// UpstreamBody is the body of PUT /admin/api/upstreams/{name}.
type UpstreamBody struct {
	Strategy  string             `json:"strategy"` // "round_robin"
	Instances []UpstreamInstance `json:"instances"`
}

// UpstreamInstance is one instance of an upstream. The admin API calls the
// host "address", where the config file says "host".
type UpstreamInstance struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Port    int    `json:"port"`
}

// UpstreamInfo is one entry of GET /admin/api/upstreams. Octopus writes
// Strategy in its own spelling ("RoundRobin"), so it isn't compared.
type UpstreamInfo struct {
	Name      string             `json:"name"`
	Strategy  string             `json:"strategy"`
	Instances []UpstreamInstance `json:"instances"`
}

// FarpBinding is the hostnames FARP routes answer on. Empty is any host.
type FarpBinding struct {
	Hostnames  []string `json:"hostnames"`
	FarpRoutes int      `json:"farp_routes"`
}

// FarpService is one service registered with Octopus over FARP.
type FarpService struct {
	Name         string    `json:"name"`
	Version      string    `json:"version"`
	InstanceID   string    `json:"instance_id"`
	Capabilities []string  `json:"capabilities"`
	RegisteredAt time.Time `json:"registered_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// UnmarshalJSON reads Octopus 0.5's timestamps, which aren't RFC 3339:
// registered_at is a Rust SystemTime debug string and updated_at is Unix
// seconds, both as strings. A timestamp it can't read is left zero.
func (s *FarpService) UnmarshalJSON(raw []byte) error {
	var v struct {
		Name         string   `json:"name"`
		Version      string   `json:"version"`
		InstanceID   *string  `json:"instance_id"`
		Capabilities []string `json:"capabilities"`
		RegisteredAt any      `json:"registered_at"`
		UpdatedAt    any      `json:"updated_at"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	*s = FarpService{Name: v.Name, Version: v.Version, Capabilities: v.Capabilities,
		RegisteredAt: octopusTime(v.RegisteredAt), UpdatedAt: octopusTime(v.UpdatedAt)}
	if v.InstanceID != nil {
		s.InstanceID = *v.InstanceID
	}
	return nil
}

var systemTimeDebug = regexp.MustCompile(`tv_sec:\s*(\d+)(?:,\s*tv_nsec:\s*(\d+))?`)

func octopusTime(v any) time.Time {
	switch v := v.(type) {
	case float64:
		return time.Unix(int64(v), 0).UTC()
	case string:
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t
		}
		if sec, err := strconv.ParseInt(v, 10, 64); err == nil {
			return time.Unix(sec, 0).UTC()
		}
		if m := systemTimeDebug.FindStringSubmatch(v); m != nil {
			sec, _ := strconv.ParseInt(m[1], 10, 64)  //nolint:errcheck // the pattern only matches digits
			nsec, _ := strconv.ParseInt(m[2], 10, 64) //nolint:errcheck // empty when Octopus leaves it out
			return time.Unix(sec, nsec).UTC()
		}
	}
	return time.Time{}
}

// StatusError is an answer from Octopus that Loomplane didn't expect. Body
// is at most 512 bytes of what Octopus wrote, with any token Loomplane holds
// replaced. A 502, 503 or 504 unwraps to ErrUnavailable.
type StatusError struct {
	Method, Path string
	Status       int
	Body         string
}

func (e *StatusError) Error() string {
	msg := fmt.Sprintf("Octopus answered %s %s with HTTP %d", e.Method, e.Path, e.Status)
	if e.Body != "" {
		msg += ": " + e.Body
	}
	return msg
}

// Unwrap is ErrUnavailable when the status means Octopus isn't answering.
func (e *StatusError) Unwrap() error {
	switch e.Status {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return ErrUnavailable
	}
	return nil
}

// RouteConflictError is a 409 to PUT /admin/api/routes/{ID}: Octopus has
// another config or admin route, Conflict, on the same method, path and
// host.
type RouteConflictError struct {
	ID, Conflict string
}

func (e *RouteConflictError) Error() string {
	return fmt.Sprintf("Octopus route %s collides with its route %s", e.ID, e.Conflict)
}

// SystemInfo reads GET /admin/api/system/info.
func (a *Admin) SystemInfo(ctx context.Context) (SystemInfo, error) {
	var out SystemInfo
	_, err := a.admin(ctx, http.MethodGet, "/admin/api/system/info", nil, &out, http.StatusOK)
	return out, err
}

// Routes reads GET /admin/api/routes.
func (a *Admin) Routes(ctx context.Context) ([]RouteInfo, error) {
	var out []RouteInfo
	_, err := a.admin(ctx, http.MethodGet, "/admin/api/routes", nil, &out, http.StatusOK)
	return out, err
}

// PutRoute creates or replaces every entry of route id. A 409 is a
// *RouteConflictError.
func (a *Admin) PutRoute(ctx context.Context, id string, body RouteBody) error {
	if body.Methods == nil {
		body.Methods = []string{}
	}
	if body.Hosts == nil {
		body.Hosts = []string{}
	}
	var conflict struct {
		Conflict string `json:"conflict"`
	}
	status, err := a.admin(ctx, http.MethodPut, "/admin/api/routes/"+url.PathEscape(id), body, &conflict, http.StatusOK, http.StatusCreated, http.StatusConflict)
	if err != nil {
		return err
	}
	if status == http.StatusConflict {
		return &RouteConflictError{ID: id, Conflict: conflict.Conflict}
	}
	return nil
}

// DeleteRoute removes every entry of route id. A route Octopus doesn't have
// is removed already.
func (a *Admin) DeleteRoute(ctx context.Context, id string) error {
	_, err := a.admin(ctx, http.MethodDelete, "/admin/api/routes/"+url.PathEscape(id), nil, nil, http.StatusOK, http.StatusNoContent, http.StatusNotFound)
	return err
}

// Upstreams reads GET /admin/api/upstreams.
func (a *Admin) Upstreams(ctx context.Context) ([]UpstreamInfo, error) {
	var out []UpstreamInfo
	_, err := a.admin(ctx, http.MethodGet, "/admin/api/upstreams", nil, &out, http.StatusOK)
	return out, err
}

// PutUpstream creates or replaces upstream name.
func (a *Admin) PutUpstream(ctx context.Context, name string, body UpstreamBody) error {
	if body.Instances == nil {
		body.Instances = []UpstreamInstance{}
	}
	_, err := a.admin(ctx, http.MethodPut, "/admin/api/upstreams/"+url.PathEscape(name), body, nil, http.StatusOK, http.StatusCreated)
	return err
}

// DeleteUpstream removes upstream name. One Octopus doesn't have is removed
// already.
func (a *Admin) DeleteUpstream(ctx context.Context, name string) error {
	_, err := a.admin(ctx, http.MethodDelete, "/admin/api/upstreams/"+url.PathEscape(name), nil, nil, http.StatusOK, http.StatusNoContent, http.StatusNotFound)
	return err
}

// FarpBinding reads GET /admin/api/farp/binding.
func (a *Admin) FarpBinding(ctx context.Context) (FarpBinding, error) {
	var out FarpBinding
	_, err := a.admin(ctx, http.MethodGet, "/admin/api/farp/binding", nil, &out, http.StatusOK)
	return out, err
}

// PutFarpBinding sets the hostnames FARP routes answer on. None is any host.
func (a *Admin) PutFarpBinding(ctx context.Context, hostnames []string) error {
	if hostnames == nil {
		hostnames = []string{}
	}
	body := struct {
		Hostnames []string `json:"hostnames"`
	}{hostnames}
	_, err := a.admin(ctx, http.MethodPut, "/admin/api/farp/binding", body, nil, http.StatusOK)
	return err
}

// FarpServices reads GET /admin/api/farp/services.
func (a *Admin) FarpServices(ctx context.Context) ([]FarpService, error) {
	var out []FarpService
	_, err := a.admin(ctx, http.MethodGet, "/admin/api/farp/services", nil, &out, http.StatusOK)
	return out, err
}

// admin sends an admin API request with the admin token.
func (a *Admin) admin(ctx context.Context, method, path string, body, out any, accept ...int) (int, error) {
	return a.call(ctx, adminTokenHeader, a.adminToken, method, path, body, out, accept)
}

// farp sends a FARP request with the FARP token.
func (a *Admin) farp(ctx context.Context, method, path string, body, out any, accept ...int) (int, error) {
	return a.call(ctx, farpTokenHeader, a.farpToken, method, path, body, out, accept)
}

// call sends one JSON request to http://component/path on the channel,
// within requestTimeout, with token in header. It reads an accepted answer
// with a body into out. Not reaching Octopus wraps ErrUnavailable; an
// answer whose status isn't in accept is a *StatusError.
func (a *Admin) call(ctx context.Context, header string, token []byte, method, path string, body, out any, accept []int) (int, error) {
	if a.ch == nil || a.ch.Client == nil || a.ch.BaseURL == nil {
		return 0, fmt.Errorf("%w: no channel to Octopus", ErrUnavailable)
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(raw)
	}
	ref, err := url.Parse(path)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, method, a.ch.BaseURL.ResolveReference(ref).String(), reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(header, string(token))
	res, err := a.ch.Client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w: %s %s: %w", ErrUnavailable, method, path, a.redactErr(err))
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, maxAnswer)) //nolint:errcheck // draining for reuse only
		_ = res.Body.Close()
	}()
	if !slices.Contains(accept, res.StatusCode) {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, maxAnswer)) //nolint:errcheck // the status is the error; the body only explains it
		return res.StatusCode, &StatusError{Method: method, Path: path, Status: res.StatusCode, Body: a.errorBody(raw)}
	}
	if out != nil && res.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(io.LimitReader(res.Body, maxAnswer)).Decode(out); err != nil && !errors.Is(err, io.EOF) {
			return res.StatusCode, fmt.Errorf("read Octopus's answer to %s %s: %w", method, path, err)
		}
	}
	return res.StatusCode, nil
}

// errorBody is what an error carries of an answer's body: tokens replaced,
// then at most maxErrorBody bytes, trimmed.
func (a *Admin) errorBody(raw []byte) string {
	s := strings.TrimSpace(a.redact(string(raw)))
	if len(s) > maxErrorBody {
		s = strings.ToValidUTF8(s[:maxErrorBody], "")
	}
	return s
}

func (a *Admin) redact(s string) string {
	for _, token := range [][]byte{a.adminToken, a.farpToken} {
		if len(token) > 0 {
			s = strings.ReplaceAll(s, string(token), "[redacted]")
		}
	}
	return s
}

// redactErr keeps the error's chain, so a deadline is still one, with its
// text redacted.
func (a *Admin) redactErr(err error) error {
	if msg := a.redact(err.Error()); msg != err.Error() {
		return redactedError{msg: msg, err: err}
	}
	return err
}

type redactedError struct {
	msg string
	err error
}

func (e redactedError) Error() string { return e.msg }
func (e redactedError) Unwrap() error { return e.err }
