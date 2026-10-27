package gcp

import (
	"context"
	"errors"
	"net"

	"cloud.google.com/go/cloudsqlconn"
)

// SQLConnector binds pgx transport to one authenticated Cloud SQL instance.
type SQLConnector interface {
	DialContext(context.Context, string, string) (net.Conn, error)
	Close() error
}

// PostgresConnector uses only the session's explicit, checked credential. The
// dialer supplies IAM authorization and TLS for this fixed instance name.
func (s *Session) PostgresConnector(ctx context.Context, region, instance string) (SQLConnector, error) {
	return s.postgresConnector(ctx, region, instance, false)
}

// PostgresPSCConnector selects only the instance's PSC route. Missing PSC
// metadata or an unreachable private endpoint never permits public fallback.
func (s *Session) PostgresPSCConnector(ctx context.Context, region, instance string) (SQLConnector, error) {
	return s.postgresConnector(ctx, region, instance, true)
}

func (s *Session) postgresConnector(ctx context.Context, region, instance string, psc bool) (SQLConnector, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.key == nil || !validSQLPathID(s.key.ProjectID) || !sqlNativeName(region) || len(region) > 63 || !sqlNativeName(instance) || len(instance) > 63 {
		return nil, ErrInvalidKey
	}
	key, err := ParseKey(s.key.raw)
	if err != nil || key.ProjectID != s.key.ProjectID || key.ClientEmail != s.key.ClientEmail || key.Keyless != s.key.Keyless {
		return nil, ErrInvalidKey
	}
	// A keyless key has no credential JSON: the dialer uses the session's
	// tokens, which impersonate the same service account.
	credential := cloudsqlconn.WithCredentialsJSON(key.raw)
	if key.Keyless {
		if s.ts == nil {
			return nil, ErrInvalidKey
		}
		credential = cloudsqlconn.WithTokenSource(s.ts)
	}
	dialer, err := cloudsqlconn.NewDialer(ctx, credential, cloudsqlconn.WithUniverseDomain("googleapis.com"), cloudsqlconn.WithLazyRefresh(), cloudsqlconn.WithUserAgent("loomplane-native-recovery"))
	if err != nil {
		return nil, errors.New("could not initialize the explicit Cloud SQL recovery credential")
	}
	return &sqlConnector{dialer: dialer, instance: key.ProjectID + ":" + region + ":" + instance, psc: psc}, nil
}

type sqlConnector struct {
	dialer   *cloudsqlconn.Dialer
	instance string
	psc      bool
}

func (c *sqlConnector) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	if c.psc {
		return c.dialer.Dial(ctx, c.instance, cloudsqlconn.WithPSC())
	}
	return c.dialer.Dial(ctx, c.instance, cloudsqlconn.WithPublicIP())
}

func (c *sqlConnector) Close() error { return c.dialer.Close() }
