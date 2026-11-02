package firecracker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"path"
	"reflect"
	"strings"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

type RouteSpec struct {
	ID, Hostname string
	Port         int
	BlockedPaths []string
	Remove       bool
}
type RouteRecord struct {
	Scope           Scope
	Route           RouteSpec
	Authority       Authority
	RuntimeIdentity string
}

func (s *Store) Route(ctx context.Context, hostname string) (RouteRecord, error) {
	var raw []byte
	var r RouteRecord
	err := s.db.QueryRowContext(ctx, `SELECT body FROM routes WHERE hostname=?`, hostname).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrMissing
	}
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(raw, &r)
	return r, err
}
func (h *Host) ApplyRoute(ctx context.Context, q Request) (RouteRecord, error) {
	if q.Route == nil || q.Route.ID == "" || !identifier.MatchString(q.Route.ID) || q.Authority.Generation < 1 || !q.Authority.Expires.After(time.Now()) || q.Authority.Expires.After(time.Now().Add(5*time.Minute)) {
		return RouteRecord{}, ErrInvalid
	}
	name := q.Route.Hostname
	if name == "" || len(name) > 253 || strings.ContainsAny(name, "/:*\\ \t\n\r") || name != strings.ToLower(name) || !strings.Contains(name, ".") {
		return RouteRecord{}, ErrInvalid
	}
	if len(q.Route.BlockedPaths) > 8 {
		return RouteRecord{}, ErrInvalid
	}
	for _, p := range q.Route.BlockedPaths {
		if !strings.HasPrefix(p, "/") || len(p) > 128 || strings.ContainsAny(p, "%*?#\\") || strings.Contains(p, "..") || strings.Contains(p, "//") {
			return RouteRecord{}, ErrInvalid
		}
	}
	h.Store.mu.Lock()
	defer h.Store.mu.Unlock()
	if !q.Authority.Expires.After(time.Now()) {
		return RouteRecord{}, ErrConflict
	}
	old, err := h.Store.Route(ctx, name)
	if err != nil && !errors.Is(err, ErrMissing) {
		return old, err
	}
	if err == nil && (old.Scope != q.Scope || (old.Route.ID != q.Route.ID && (!old.Route.Remove || q.Authority.Generation <= old.Authority.Generation)) || old.Authority.Generation > q.Authority.Generation) {
		return old, ErrConflict
	}
	record := RouteRecord{Scope: q.Scope, Route: *q.Route, Authority: q.Authority, RuntimeIdentity: q.RuntimeIdentity}
	if err == nil && old.Authority.Generation == q.Authority.Generation {
		if !reflect.DeepEqual(old.Route, *q.Route) || old.Authority.Operation != q.Authority.Operation {
			return old, ErrConflict
		}
	}
	if !q.Route.Remove {
		r, e := h.Store.Get(ctx, q.Scope)
		if e != nil {
			return record, e
		}
		if r.Current == nil || r.State != "ready" || r.Current.Spec.Kind != "app" || r.Current.Spec.Port != q.Route.Port || r.RuntimeIdentity != q.RuntimeIdentity {
			return record, ErrConflict
		}
		status, e := h.Driver.Guest(ctx, *r.Current, "status", nil)
		if e != nil {
			return record, e
		}
		if !status.Ready {
			return record, ErrUnavailable
		}
	}
	if !q.Authority.Expires.After(time.Now()) {
		return record, ErrConflict
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return record, err
	}
	_, err = h.Store.db.ExecContext(ctx, `INSERT INTO routes(hostname,body) VALUES(?,?) ON CONFLICT(hostname) DO UPDATE SET body=excluded.body`, name, raw)
	return record, err
}

type Ingress struct {
	Host   *Host
	Config HostConfig
}

func (i *Ingress) route(ctx context.Context, hostname string) (RouteRecord, Record, error) {
	r, err := i.Host.Store.Route(ctx, hostname)
	if err != nil || r.Route.Remove {
		return r, Record{}, ErrMissing
	}
	vm, err := i.Host.Store.Get(ctx, r.Scope)
	if err != nil {
		return r, vm, err
	}
	if !vm.Serving() || vm.Current.Spec.Kind != "app" || vm.Current.Spec.Port != r.Route.Port || vm.RuntimeIdentity != r.RuntimeIdentity {
		return r, vm, ErrUnavailable
	}
	return r, vm, nil
}
func (i *Ingress) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := r.Host
	if host, _, e := net.SplitHostPort(name); e == nil {
		name = host
	}
	route, vm, err := i.route(r.Context(), strings.ToLower(name))
	if err != nil {
		http.Error(w, "route unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.TLS == nil || r.TLS.ServerName != name {
		http.Error(w, "hostname mismatch", http.StatusMisdirectedRequest)
		return
	}
	for _, p := range route.Route.BlockedPaths {
		if path.Clean(r.URL.Path) == p || strings.HasPrefix(path.Clean(r.URL.Path), p+"/") {
			http.NotFound(w, r)
			return
		}
	}
	driver, ok := i.Host.Driver.(TunnelDriver)
	if !ok {
		http.Error(w, "transport unavailable", http.StatusServiceUnavailable)
		return
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return driver.DialVM(ctx, *vm.Current) }, DisableKeepAlives: true, ResponseHeaderTimeout: 30 * time.Second}
	defer transport.CloseIdleConnections()
	proxy := httputil.ReverseProxy{Rewrite: func(p *httputil.ProxyRequest) {
		p.Out.URL.Scheme = "http"
		p.Out.URL.Host = "guest"
		p.Out.Host = name
		p.SetXForwarded()
	}, Transport: transport, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "backend unavailable", http.StatusBadGateway)
	}}
	proxy.ServeHTTP(w, r)
}
func (i *Ingress) Run(ctx context.Context) error {
	manager := &autocert.Manager{Prompt: autocert.AcceptTOS, Email: i.Config.ACMEEmail, Cache: autocert.DirCache(i.Config.StateDir + "/certificates"), HostPolicy: func(ctx context.Context, name string) error { _, _, err := i.route(ctx, name); return err }}
	if i.Config.ACMEDirectory != "" {
		manager.Client = &acme.Client{DirectoryURL: i.Config.ACMEDirectory}
	}
	https := &http.Server{Addr: i.Config.HTTPSListen, Handler: i, TLSConfig: manager.TLSConfig(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute}
	httpServer := &http.Server{Addr: i.Config.HTTPListen, Handler: manager.HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, err := i.route(r.Context(), r.Host); err != nil {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "https://"+r.Host+r.URL.RequestURI(), http.StatusPermanentRedirect) //nolint:gosec // The journal lookup above accepts only an owned, validated hostname.
	})), ReadHeaderTimeout: 10 * time.Second}
	results := make(chan error, 2)
	go func() { results <- https.ListenAndServeTLS("", "") }()
	go func() { results <- httpServer.ListenAndServe() }()
	var err error
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case err = <-results:
	}
	_ = https.Close()
	_ = httpServer.Close()
	return err
}
func (c *Client) Route(ctx context.Context, q Request) (RouteRecord, error) {
	var out RouteRecord
	err := c.call(ctx, http.MethodPost, "/v1/route", q, &out, nil)
	return out, err
}
