package octopusloom

import (
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/deployments"
)

// Tokens the fake Octopus accepts. Neither is a substring of the other, so a
// test can look for either one in an error.
const (
	fakeAdminToken = "tok"
	fakeFarpToken  = "farp-secret"
)

// fakeOctopus is an Octopus 0.5 admin API and FARP endpoint, in memory. It
// answers the way Octopus does: one route entry per method and host, PUT and
// DELETE acting on every entry of an id, a 409 naming the conflicting
// route, and admin and FARP calls each checked for their own token header.
type fakeOctopus struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	startTime string
	routes    map[string]*fakeRoute
	upstreams map[string]UpstreamBody
	binding   []string
	services  []map[string]any
	// instances are FARP registrations by instance id.
	instances map[string]json.RawMessage
	// conflicts makes a PUT of the route id answer 409 naming the value.
	// Without one, a PUT that overlaps another config or admin route on a
	// method, path and host answers 409 naming that route, as Octopus does.
	conflicts map[string]string
	// status makes "METHOD /path" answer with that status and failBody.
	status   map[string]int
	failBody string
	// farpStatus makes the FARP call "METHOD instance-id", such as
	// "POST lp-svc-billing", answer with that status.
	farpStatus map[string]int
	requests   []fakeRequest
	closed     int
}

// fakeRoute is a route as the fake holds it. NoRouteID reports the entries
// with a null route_id, as some discovered routes have.
type fakeRoute struct {
	RouteBody
	Origin    string
	NoRouteID bool
}

type fakeRequest struct {
	Method, Path, Body string
	Header             http.Header
}

func newFakeOctopus(t *testing.T) *fakeOctopus {
	t.Helper()
	f := &fakeOctopus{
		t: t, startTime: "2026-10-21 10:00:00",
		routes: map[string]*fakeRoute{}, upstreams: map[string]UpstreamBody{},
		binding: []string{}, instances: map[string]json.RawMessage{},
		conflicts: map[string]string{}, status: map[string]int{}, farpStatus: map[string]int{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// seed loads d into the fake as routes and upstreams of origin, and d's FARP
// hostnames as the binding: what Octopus holds after starting with d's
// config (origin "config") or after Loomplane pushed d (origin "admin").
func (f *fakeOctopus) seed(d Desired, origin string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range d.Upstreams {
		body := UpstreamBody{Strategy: "round_robin"}
		for _, i := range u.Instances {
			body.Instances = append(body.Instances, UpstreamInstance{ID: i.ID, Address: i.Host, Port: i.Port})
		}
		f.upstreams[u.Name] = body
	}
	for _, r := range d.Routes {
		f.routes[r.ID] = &fakeRoute{Origin: origin, RouteBody: RouteBody{
			Path: r.Path, Methods: slices.Clone(r.Methods), Hosts: slices.Clone(r.Hosts),
			Upstream: r.Upstream, StripPrefix: r.StripPrefix,
		}}
	}
	f.binding = slices.Clone(d.FarpHostnames)
}

// setOrigin changes the origin every route reports, as a restart or a PUT
// does.
func (f *fakeOctopus) setOrigin(origin string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.routes {
		r.Origin = origin
	}
}

func (f *fakeOctopus) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = nil
}

func (f *fakeOctopus) log() []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// writes are the requests that weren't GETs, as "METHOD /path".
func (f *fakeOctopus) writes() []string {
	var out []string
	for _, r := range f.log() {
		if r.Method != http.MethodGet {
			out = append(out, r.Method+" "+r.Path)
		}
	}
	return out
}

// body is the body of the first request to "METHOD /path", or fails.
func (f *fakeOctopus) body(call string) string {
	f.t.Helper()
	for _, r := range f.log() {
		if r.Method+" "+r.Path == call {
			return r.Body
		}
	}
	f.t.Fatalf("no %s in %v", call, f.writes())
	return ""
}

// channel is a component channel to the fake: it sends only to
// http://component, the way deployments.ComponentChannel does.
func (f *fakeOctopus) channel() *deployments.ComponentChannel {
	target, err := url.Parse(f.srv.URL)
	if err != nil {
		f.t.Fatal(err)
	}
	return &deployments.ComponentChannel{
		Client:  &http.Client{Transport: componentTransport{host: target.Host, next: f.srv.Client().Transport}, Timeout: 30 * time.Second},
		BaseURL: &url.URL{Scheme: "http", Host: "component"},
		Close: func() error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.closed++
			return nil
		},
	}
}

// componentTransport sends requests for http://component to the fake and
// refuses anything else.
type componentTransport struct {
	host string
	next http.RoundTripper
}

func (c componentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "http" || req.URL.Host != "component" {
		return nil, errors.New("a component channel only sends to http://component")
	}
	out := req.Clone(req.Context())
	out.URL.Host = c.host
	return c.next.RoundTrip(out)
}

func (f *fakeOctopus) serve(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, fakeRequest{Method: r.Method, Path: r.URL.Path, Body: string(raw), Header: r.Header.Clone()})

	if r.Header.Get("Authorization") != "" {
		f.t.Errorf("%s %s carries an Authorization header", r.Method, r.URL.Path)
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/admin/"):
		if r.Header.Get("X-Octopus-Admin-Token") != fakeAdminToken {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
	case strings.HasPrefix(r.URL.Path, "/_farp/"):
		if r.Header.Get("X-Octopus-Farp-Token") != fakeFarpToken {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
	}
	if code, ok := f.status[r.Method+" "+r.URL.Path]; ok {
		w.WriteHeader(code)
		_, _ = io.WriteString(w, f.failBody)
		return
	}

	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/admin/api/system/info":
		writeJSON(w, http.StatusOK, map[string]any{"version": "0.5.0", "uptime_seconds": 42, "start_time": f.startTime, "hostname": "octopus"})
	case r.Method == http.MethodGet && path == "/admin/api/routes":
		writeJSON(w, http.StatusOK, f.routeEntries())
	case strings.HasPrefix(path, "/admin/api/routes/"):
		f.serveRoute(w, r.Method, strings.TrimPrefix(path, "/admin/api/routes/"), raw)
	case r.Method == http.MethodGet && path == "/admin/api/upstreams":
		writeJSON(w, http.StatusOK, f.upstreamEntries())
	case strings.HasPrefix(path, "/admin/api/upstreams/"):
		f.serveUpstream(w, r.Method, strings.TrimPrefix(path, "/admin/api/upstreams/"), raw)
	case path == "/admin/api/farp/binding":
		f.serveBinding(w, r.Method, raw)
	case r.Method == http.MethodGet && path == "/admin/api/farp/services":
		services := f.services
		if services == nil {
			services = []map[string]any{}
		}
		writeJSON(w, http.StatusOK, services)
	case strings.HasPrefix(path, "/_farp/v1/"):
		f.serveFarp(w, r.Method, strings.TrimPrefix(path, "/_farp/v1/"), raw)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

// routeEntries is GET /admin/api/routes: one entry per method and host.
func (f *fakeOctopus) routeEntries() []map[string]any {
	out := []map[string]any{}
	for _, id := range slices.Sorted(maps.Keys(f.routes)) {
		r := f.routes[id]
		methods := r.Methods
		if len(methods) == 0 {
			methods = methodOrder
		}
		hosts := []*string{nil}
		if len(r.Hosts) > 0 {
			hosts = nil
			for _, h := range r.Hosts {
				hosts = append(hosts, &h)
			}
		}
		for _, m := range methods {
			for _, h := range hosts {
				var routeID, strip any
				if !r.NoRouteID {
					routeID = id
				}
				if r.StripPrefix != "" {
					strip = r.StripPrefix
				}
				entryID := id + ":" + m
				if h != nil {
					entryID += ":" + *h
				}
				out = append(out, map[string]any{
					"id": entryID, "route_id": routeID, "origin": r.Origin, "host": h,
					"path": r.Path, "method": m, "upstream": r.Upstream, "priority": r.Priority,
					"strip_prefix": strip, "request_count": 0, "is_healthy": true,
				})
			}
		}
	}
	return out
}

func (f *fakeOctopus) serveRoute(w http.ResponseWriter, method, id string, raw []byte) {
	switch method {
	case http.MethodPut:
		var body RouteBody
		if err := json.Unmarshal(raw, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		other := f.conflicts[id]
		if other == "" {
			other = f.overlap(id, body)
		}
		if other != "" {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "route conflicts with " + other, "conflict": other})
			return
		}
		if _, ok := f.upstreams[body.Upstream]; !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown upstream " + body.Upstream})
			return
		}
		f.routes[id] = &fakeRoute{RouteBody: body, Origin: "admin"}
		writeJSON(w, http.StatusOK, map[string]string{"id": id})
	case http.MethodDelete:
		if _, ok := f.routes[id]; !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no route " + id})
			return
		}
		delete(f.routes, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// overlap is the first other config or admin route that answers a method
// and host body answers on body's path.
func (f *fakeOctopus) overlap(id string, body RouteBody) string {
	want := fakeEntries(body.Methods, body.Hosts)
	for _, other := range slices.Sorted(maps.Keys(f.routes)) {
		r := f.routes[other]
		if other == id || r.Path != body.Path || (r.Origin != "config" && r.Origin != "admin") {
			continue
		}
		for e := range fakeEntries(r.Methods, r.Hosts) {
			if want[e] {
				return other
			}
		}
	}
	return ""
}

// fakeEntries are a route's "METHOD host" entries, host "" for any host.
func fakeEntries(methods, hosts []string) map[string]bool {
	if len(methods) == 0 {
		methods = methodOrder
	}
	if len(hosts) == 0 {
		hosts = []string{""}
	}
	out := map[string]bool{}
	for _, m := range methods {
		for _, h := range hosts {
			out[m+" "+h] = true
		}
	}
	return out
}

func (f *fakeOctopus) upstreamEntries() []map[string]any {
	out := make([]map[string]any, 0, len(f.upstreams))
	for _, name := range slices.Sorted(maps.Keys(f.upstreams)) {
		u := f.upstreams[name]
		instances := make([]map[string]any, 0, len(u.Instances))
		for _, i := range u.Instances {
			instances = append(instances, map[string]any{
				"id": i.ID, "address": i.Address, "port": i.Port, "weight": 1, "healthy": true,
				"url": "http://" + i.Address,
			})
		}
		out = append(out, map[string]any{
			"name": name, "strategy": "RoundRobin", "instance_count": len(u.Instances),
			"healthy_count": len(u.Instances), "instances": instances,
		})
	}
	return out
}

func (f *fakeOctopus) serveUpstream(w http.ResponseWriter, method, name string, raw []byte) {
	switch method {
	case http.MethodPut:
		var body UpstreamBody
		if err := json.Unmarshal(raw, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		f.upstreams[name] = body
		writeJSON(w, http.StatusOK, map[string]string{"name": name})
	case http.MethodDelete:
		if _, ok := f.upstreams[name]; !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no upstream " + name})
			return
		}
		delete(f.upstreams, name)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *fakeOctopus) serveBinding(w http.ResponseWriter, method string, raw []byte) {
	if method == http.MethodPut {
		var body struct {
			Hostnames []string `json:"hostnames"`
		}
		if err := json.Unmarshal(raw, &body); err != nil || body.Hostnames == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "hostnames is required"})
			return
		}
		f.binding = body.Hostnames
	} else if method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	farpRoutes := 0
	for _, r := range f.routes {
		if r.Origin == "farp" {
			farpRoutes++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"hostnames": f.binding, "gateway_id": nil, "farp_routes": farpRoutes})
}

// serveFarp is the FARP registration protocol under /_farp/v1/.
func (f *fakeOctopus) serveFarp(w http.ResponseWriter, method, rest string, raw []byte) {
	switch {
	case method == http.MethodPost && rest == "register":
		var body struct {
			Instance struct {
				ID string `json:"id"`
			} `json:"instance"`
		}
		if err := json.Unmarshal(raw, &body); err != nil || body.Instance.ID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "instance.id is required"})
			return
		}
		if code, ok := f.farpStatus["POST "+body.Instance.ID]; ok {
			writeJSON(w, code, map[string]string{"error": "refused"})
			return
		}
		f.instances[body.Instance.ID] = raw
		writeJSON(w, http.StatusCreated, map[string]string{"instance_id": body.Instance.ID})
	case method == http.MethodPut && strings.HasPrefix(rest, "heartbeat/"):
		if code, ok := f.farpStatus["PUT "+strings.TrimPrefix(rest, "heartbeat/")]; ok {
			writeJSON(w, code, map[string]string{"error": "refused"})
			return
		}
		if _, ok := f.instances[strings.TrimPrefix(rest, "heartbeat/")]; !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown instance"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
	case method == http.MethodDelete && strings.HasPrefix(rest, "deregister/"):
		id := strings.TrimPrefix(rest, "deregister/")
		if code, ok := f.farpStatus["DELETE "+id]; ok {
			writeJSON(w, code, map[string]string{"error": "refused"})
			return
		}
		if _, ok := f.instances[id]; !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown instance"})
			return
		}
		delete(f.instances, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
