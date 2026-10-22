package planning

import (
	"errors"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/xraph/loomplane/internal/config"
)

// errDatabaseNotDenied is a database URL loomplaneServices can't read.
// It never repeats the URL, which holds the database's password.
var errDatabaseNotDenied = errors.New("the database URL can't be read for the endpoint check deny list, so checks from a cluster aren't refused Loomplane's database")

// loomplaneServices are the host:port addresses of what Loomplane itself
// depends on and serves, which an endpoint check from its own network must
// never reach: its database, its own API, the worker's health checks, the
// federation agent listener and its provider plugins. Its Vault lives in
// that database (vault/store/postgres), so it has no address of its own.
// A Unix socket isn't an address a check can dial. A database URL it
// can't read is errDatabaseNotDenied, beside every other address.
func loomplaneServices(cfg config.Config) ([]string, error) {
	var services []string
	add := func(host, port string) {
		if strings.HasPrefix(host, "/") || port == "" {
			return
		}
		if s := net.JoinHostPort(host, port); !slices.Contains(services, s) {
			services = append(services, s)
		}
	}
	var dbErr error
	if pg, err := pgconn.ParseConfig(cfg.Database.URL); err == nil {
		add(pg.Host, strconv.Itoa(int(pg.Port)))
		for _, f := range pg.Fallbacks {
			add(f.Host, strconv.Itoa(int(f.Port)))
		}
	} else {
		dbErr = errDatabaseNotDenied
	}
	for _, addr := range []string{cfg.HTTPAddr, cfg.Worker.HTTPAddr, cfg.Federation.Runtime.ListenAddress} {
		if host, port, err := net.SplitHostPort(addr); addr != "" && err == nil {
			add(host, port)
		}
	}
	for _, p := range cfg.Providers.Plugins {
		u, err := url.Parse(p.URL)
		if err != nil || u.Hostname() == "" {
			continue
		}
		port := u.Port()
		if port == "" {
			port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
		}
		add(u.Hostname(), port)
	}
	return services, dbErr
}
