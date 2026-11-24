package catalogconnect

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/xraph/loomplane/internal/recipes"
)

// mongoParams are the options a MongoDB connection string may set. Left
// out are the ones that read files on Loomplane's host (tlsCAFile,
// tlsCertificateKeyFile), send traffic through a proxy (proxyHost), or
// authenticate as Loomplane rather than as the URL's user (MONGODB-AWS,
// MONGODB-X509, GSSAPI, through authMechanism).
var mongoParams = map[string]bool{
	"authsource": true, "authmechanism": true, "tls": true, "ssl": true, "tlsinsecure": true,
	"tlsallowinvalidcertificates": true, "tlsallowinvalidhostnames": true, "replicaset": true,
	"retrywrites": true, "retryreads": true, "w": true, "journal": true, "readpreference": true,
	"appname": true, "directconnection": true, "maxpoolsize": true, "minpoolsize": true,
	"connecttimeoutms": true, "sockettimeoutms": true, "serverselectiontimeoutms": true,
	"compressors": true, "loadbalanced": true, "srvservicename": true, "srvmaxhosts": true,
}

// mongoMechanisms are the authMechanism values that use the URL's own user
// and password.
var mongoMechanisms = []string{"SCRAM-SHA-1", "SCRAM-SHA-256", "PLAIN"}

// parseMongoDSN reads a mongodb:// or mongodb+srv:// URL. It looks nothing
// up: a +srv host is kept as written, and the driver resolves it when a
// check or a service connects. A replica set may list several hosts; the
// endpoint shows the first, and every one is dialed through the same
// guarded dialer. Its errors never repeat the URL.
func parseMongoDSN(e *Endpoint, dsn string) error {
	dsn = strings.TrimSpace(dsn)
	scheme, rest, ok := strings.Cut(dsn, "://")
	if !ok || (scheme != "mongodb" && scheme != "mongodb+srv") {
		return fmt.Errorf("%w: a mongodb connection string starts with mongodb:// or mongodb+srv://, such as %s", ErrInvalid, mongoExample)
	}
	srv := scheme == "mongodb+srv"
	// The authority ends at the first slash or question mark after the
	// user info; a password may hold neither unescaped.
	at := strings.LastIndex(rest, "@")
	end := len(rest)
	if i := strings.IndexAny(rest[at+1:], "/?"); i >= 0 {
		end = at + 1 + i
	}
	if at < 0 || at > end {
		return fmt.Errorf("%w: a mongodb connection string must name its user, such as %s", ErrInvalid, mongoExample)
	}
	info, err := url.Parse("mongodb://" + rest[:at] + "@x" + rest[end:])
	if err != nil || info.User == nil || info.User.Username() == "" {
		return fmt.Errorf("%w: a mongodb connection string must name its user, and escape any @, : or / in its password", ErrInvalid)
	}
	hosts := strings.Split(rest[at+1:end], ",")
	if srv && len(hosts) != 1 {
		return fmt.Errorf("%w: a mongodb+srv connection string names one host", ErrInvalid)
	}
	for i, h := range hosts {
		host, port, err := mongoHost(h, srv)
		if err != nil {
			return err
		}
		if i == 0 {
			e.Host, e.Port = host, port
		}
	}
	e.Username = info.User.Username()
	e.Database = strings.TrimPrefix(info.Path, "/")
	if strings.Contains(e.Database, "/") {
		return fmt.Errorf("%w: a mongodb connection string names at most one database", ErrInvalid)
	}
	tlsOn, insecure := srv, false
	for name, values := range info.Query() {
		key := strings.ToLower(name)
		if !mongoParams[key] {
			return fmt.Errorf("%w: a mongodb connection string can't set %q here", ErrInvalid, name)
		}
		value := ""
		if len(values) > 0 {
			value = values[len(values)-1]
		}
		switch key {
		case "authmechanism":
			if !slices.Contains(mongoMechanisms, strings.ToUpper(value)) {
				return fmt.Errorf("%w: Loomplane connects with the URL's user and password: SCRAM-SHA-256, SCRAM-SHA-1 or PLAIN, not %q", ErrInvalid, value)
			}
		case "tls", "ssl":
			tlsOn = value == "true"
		case "tlsinsecure", "tlsallowinvalidcertificates", "tlsallowinvalidhostnames":
			insecure = insecure || value == "true"
		}
	}
	switch {
	case !tlsOn:
		e.TLS = TLSDisable
	case insecure:
		e.TLS = TLSRequire
	default:
		e.TLS = TLSVerifyFull
	}
	return nil
}

const mongoExample = "mongodb://user:password@db.example.com:27017/app?authSource=admin" //nolint:gosec // the shape of one, in an error message

// mongoHost reads one host of a mongodb URL. A +srv host takes no port.
func mongoHost(h string, srv bool) (host string, port int, err error) {
	if h == "" || strings.HasPrefix(h, "/") || strings.Contains(h, "%2F") {
		return "", 0, fmt.Errorf("%w: each host in a mongodb connection string must be a host name or address", ErrInvalid)
	}
	host, rawPort, splitErr := net.SplitHostPort(h)
	if splitErr != nil {
		// No port, or a bare IPv6 address in brackets.
		return strings.Trim(h, "[]"), 27017, nil
	}
	if srv {
		return "", 0, fmt.Errorf("%w: a mongodb+srv host takes no port", ErrInvalid)
	}
	port, err = strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("%w: a mongodb port must be between 1 and 65535", ErrInvalid)
	}
	return host, port, nil
}

// MongoProber checks a MongoDB endpoint: it connects through the dialer,
// pings, reads the server's version and, when asked, what the user's roles
// let it do.
type MongoProber struct{}

var _ Prober = MongoProber{}

// mongoDialer adapts a DialFunc to the driver's dialer.
type mongoDialer struct{ dial DialFunc }

func (d mongoDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d.dial(ctx, network, address)
}

// Probe implements Prober.
func (MongoProber) Probe(ctx context.Context, dial DialFunc, dsn string, req recipes.Requirement) (*Report, error) {
	if dial == nil {
		return nil, errors.New("the MongoDB check needs a dialer")
	}
	e := &Endpoint{Engine: EngineMongoDB}
	if err := parseMongoDSN(e, dsn); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	opts := options.Client().ApplyURI(strings.TrimSpace(dsn)).
		SetDialer(mongoDialer{dial: dial}).
		SetConnectTimeout(probeTimeout).
		SetServerSelectionTimeout(probeTimeout).
		SetMaxPoolSize(1)
	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, errors.New("the MongoDB connection settings can't be used")
	}
	defer func() { _ = client.Disconnect(context.WithoutCancel(ctx)) }() //nolint:errcheck // a probe only reads
	admin := client.Database("admin")
	var info struct {
		Version string `bson:"version"`
	}
	if err := admin.RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&info); err != nil {
		return nil, mongoFailure(ctx, err)
	}
	report := &Report{Version: info.Version}
	checkVersion(report, EngineMongoDB, req)
	if wantsCapabilities(ctx) {
		var status struct {
			AuthInfo struct {
				Roles []struct {
					Role string `bson:"role"`
					DB   string `bson:"db"`
				} `bson:"authenticatedUserRoles"`
			} `bson:"authInfo"`
		}
		if err := admin.RunCommand(ctx, bson.D{{Key: "connectionStatus", Value: 1}}).Decode(&status); err != nil {
			return nil, mongoFailure(ctx, err)
		}
		caps := &Capabilities{}
		for _, r := range status.AuthInfo.Roles {
			anyDB := r.DB == "admin"
			switch {
			case r.Role == "root":
				caps.CreateDatabase, caps.CreateUser = true, true
			case anyDB && (r.Role == "readWriteAnyDatabase" || r.Role == "dbAdminAnyDatabase"):
				caps.CreateDatabase = true
			case anyDB && (r.Role == "userAdminAnyDatabase" || r.Role == "userAdmin"):
				caps.CreateUser = true
			}
		}
		report.Capabilities = caps
	}
	return report, nil
}

// mongoFailure words a failed command or connection without the URL.
func mongoFailure(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return errTimedOut
	}
	var server mongo.ServerError
	if errors.As(err, &server) {
		if server.HasErrorCode(18) || server.HasErrorCode(13) {
			return refused("MongoDB refused the user or its rights")
		}
		return refused("MongoDB refused the check")
	}
	if errors.Is(err, mongo.ErrClientDisconnected) {
		return errors.New("the MongoDB connection closed")
	}
	return errors.New("MongoDB couldn't be reached")
}
