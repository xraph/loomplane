package planning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
	"github.com/xraph/loomplane/internal/security"
)

const falkorRouteHello = "*14\r\n$6\r\nserver\r\n$5\r\nredis\r\n$7\r\nversion\r\n$5\r\n7.2.4\r\n$5\r\nproto\r\n:2\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*1\r\n*8\r\n$4\r\nname\r\n$5\r\ngraph\r\n$3\r\nver\r\n:40202\r\n$4\r\npath\r\n$10\r\n/module.so\r\n$4\r\nargs\r\n*0\r\n"

type falkorRouteDialer struct {
	t     *testing.T
	dials atomic.Int32
	done  atomic.Int32
}

func (*falkorRouteDialer) TargetCloud(context.Context, string, string) (string, error) {
	return connections.Server, nil
}

func (d *falkorRouteDialer) TargetDial(_ context.Context, org, target string) (catalogconnect.DialFunc, io.Closer, error) {
	if org != "org" || target != "graph-target" {
		return nil, nil, connections.ErrNotFound
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		d.dials.Add(1)
		if network != "tcp" || address != "graph.example:6379" {
			d.t.Error("wrong FalkorDB target")
			return nil, errors.New("unexpected target")
		}
		client, server := net.Pipe()
		deadline, _ := ctx.Deadline()
		_ = server.SetDeadline(deadline)
		done := make(chan struct{})
		d.t.Cleanup(func() { _ = server.Close(); _ = client.Close(); <-done })
		go func() {
			defer close(done)
			defer server.Close()
			for _, exchange := range []struct{ request, reply string }{
				{"*5\r\n$5\r\nHELLO\r\n$1\r\n2\r\n$4\r\nAUTH\r\n$3\r\napp\r\n$17\r\nfixture-secret-42\r\n", falkorRouteHello},
				{"*3\r\n$12\r\nGRAPH.CONFIG\r\n$3\r\nGET\r\n$14\r\nRESULTSET_SIZE\r\n", "*2\r\n$14\r\nRESULTSET_SIZE\r\n:73\r\n"},
			} {
				got := make([]byte, len(exchange.request))
				if _, err := io.ReadFull(server, got); err != nil || string(got) != exchange.request {
					d.t.Error("unexpected FalkorDB request")
					return
				}
				if _, err := io.WriteString(server, exchange.reply); err != nil {
					d.t.Error("fixture could not send the FalkorDB reply")
					return
				}
			}
		}()
		return client, nil
	}, closer(func() error { d.done.Add(1); return nil }), nil
}

func TestFalkorEndpointRoutesUseProductionCheckerAndTarget(t *testing.T) {
	kinds := recipes.NewRegistry()
	kind := recipestest.Sample()
	kind.Dependencies[0].External = &recipes.Requirement{Engine: "falkordb", MinVersion: "4.2", TLS: []string{"disable"}}
	if err := kinds.Register(kind); err != nil {
		t.Fatal("fixture could not register the FalkorDB requirement")
	}
	store := &endpointStore{rows: map[string]catalogconnect.Endpoint{}, inUse: map[string]bool{}}
	vault := endpointSecrets{}
	audit := &endpointAudit{}
	svc := catalogconnect.NewService(store, vault, searchRouteAuthz{})
	svc.Audit = audit
	dialer := &falkorRouteDialer{t: t}
	checker := newEndpointChecker(svc)
	if _, ok := checker.Probers["falkordb"].(catalogconnect.FalkorDBProber); !ok {
		t.Fatal("production checker lacks the FalkorDB prober")
	}
	ext := &Extension{endpoints: svc, endpointChecks: checker, endpointDialer: dialer, catalogKinds: kinds, checkLimit: newCheckLimiter(1, 1)}
	router := forge.NewRouter()
	if err := ext.routes(router, nil); err != nil {
		t.Fatal("fixture could not register the endpoint routes")
	}
	send := func(method, path, user string, body any) *httptest.ResponseRecorder {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal("fixture could not encode the request")
		}
		ctx := security.WithPrincipal(t.Context(), security.Principal{OrgID: "org", UserID: user})
		req := httptest.NewRequestWithContext(ctx, method, "/api/v1/catalog/endpoints"+path, strings.NewReader(string(data)))
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		if strings.Contains(out.Body.String(), "fixture-secret") {
			t.Fatal("endpoint response exposed a password")
		}
		return out
	}
	raw := `{"engine":"falkordb","host":"graph.example","username":"app","password":"fixture-secret-42","tls":"disable"}`
	in := catalogconnect.CheckInput{Engine: "falkordb", DSN: raw, Target: "graph-target", Kind: kind.Name, Dependency: "postgres"}
	if got := send(http.MethodPost, "/checks", security.RoleViewer, in); got.Code != 403 || dialer.dials.Load() != 0 {
		t.Fatal("unauthorized check reached the target")
	}
	var result catalogconnect.CheckResult
	out := send(http.MethodPost, "/checks", security.RoleAdmin, in)
	if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &result) != nil || !result.OK || result.Version != "4.2.2" || dialer.dials.Load() != 1 || dialer.done.Load() != 1 || len(vault) != 0 {
		t.Fatal("FalkorDB check failed or touched Vault")
	}
	if got := send(http.MethodPost, "/checks", security.RoleAdmin, in); got.Code != 429 || dialer.dials.Load() != 1 {
		t.Fatal("rate-limited check reached the target")
	}
	out = send(http.MethodPost, "", security.RoleAdmin, catalogconnect.CreateInput{Name: "graph", Engine: "falkordb", DSN: raw})
	var saved catalogconnect.Endpoint
	if out.Code != 201 || json.Unmarshal(out.Body.Bytes(), &saved) != nil || string(vault[catalogconnect.DSNKey("org", saved.ID)]) != raw {
		t.Fatal("FalkorDB endpoint was not stored in Vault")
	}
	if got := send(http.MethodGet, "", security.RoleViewer, nil); got.Code != 200 || !strings.Contains(got.Body.String(), saved.ID) {
		t.Fatal("stored endpoint missing from the list")
	}
	for _, event := range audit.events {
		if event.Address != "" && event.Address != "graph.example:6379" || strings.Contains(fmt.Sprint(event), "fixture-secret") {
			t.Fatal("audit exposed more than the allowed endpoint metadata")
		}
	}
}
