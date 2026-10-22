package mesh

import (
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/xraph/bastion"
	"github.com/xraph/bastion/health"
	"github.com/xraph/bastion/proxy"
	"github.com/xraph/bastion/resilience"
	"github.com/xraph/bastion/routing"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/access"
)

// NewLoopbackHTTP builds one immutable Bastion route to a local application.
// The application must bind only to loopback in the gateway's network namespace;
// installation must verify that separately before advertising backend isolation.
// The returned handler must run behind HTTPGuard, never on a public listener.
func NewLoopbackHTTP(address string, port uint16) (http.Handler, error) {
	ip, err := netip.ParseAddr(address)
	if err != nil || !ip.IsLoopback() || ip.Zone() != "" || ip.String() != address || port == 0 {
		return nil, access.ErrDenied
	}
	origin := "http://" + net.JoinHostPort(address, strconv.Itoa(int(port)))
	routes := routing.NewManager()
	if err := routes.AddRoute(&bastion.Route{ID: "local", Path: "/*", Targets: []*bastion.Target{{ID: "local", URL: origin, Weight: 1, Healthy: true}}, Protocol: bastion.ProtocolHTTP, Source: bastion.SourceManual, Enabled: true}); err != nil {
		return nil, err
	}
	logger := forge.NewNoopLogger()
	// No dynamic discovery, retries, mirroring, caching or path rewriting. A short
	// idle bound retires this immutable handler's unused upstream connections.
	config := bastion.Config{Timeouts: bastion.TimeoutConfig{Connect: 5 * time.Second, Read: 30 * time.Second, Idle: time.Second}}
	return &loopbackHTTP{port: port, handler: proxy.NewEngine(config, logger, routes, health.NewMonitor(health.Config{}, logger), resilience.NewCBManager(bastion.CircuitBreakerConfig{}), bastion.NewRateLimiter(bastion.RateLimitConfig{}), proxy.NewStatsCollector(), bastion.NewHookEngine(), routing.NewLoadBalancer(bastion.LBRoundRobin))}, nil
}

// The bound port is retained with the immutable route for guard construction.
type loopbackHTTP struct {
	port    uint16
	handler http.Handler
}

func (h *loopbackHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.handler.ServeHTTP(w, r) }
