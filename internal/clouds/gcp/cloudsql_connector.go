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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.key == nil || !validSQLPathID(s.key.ProjectID) || !sqlNativeName(region) || len(region) > 63 || !sqlNativeName(instance) || len(instance) > 63 {
		return nil, ErrInvalidKey
	}
	key, err := ParseKey(s.key.raw)
	if err != nil || key.ProjectID != s.key.ProjectID || key.ClientEmail != s.key.ClientEmail {
		return nil, ErrInvalidKey
	}
	dialer, err := cloudsqlconn.NewDialer(ctx, cloudsqlconn.WithCredentialsJSON(key.raw), cloudsqlconn.WithUniverseDomain("googleapis.com"), cloudsqlconn.WithLazyRefresh(), cloudsqlconn.WithUserAgent("loomplane-native-recovery"))
	if err != nil {
		return nil, errors.New("could not initialize the explicit Cloud SQL recovery credential")
	}
	return &sqlConnector{dialer: dialer, instance: key.ProjectID + ":" + region + ":" + instance}, nil
}

type sqlConnector struct {
	dialer   *cloudsqlconn.Dialer
	instance string
}

func (c *sqlConnector) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	return c.dialer.Dial(ctx, c.instance, cloudsqlconn.WithPublicIP())
}

func (c *sqlConnector) Close() error { return c.dialer.Close() }
