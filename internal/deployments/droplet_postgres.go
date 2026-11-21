package deployments

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
)

// DropletPostgresChecker resolves the bound cluster's private endpoint and checks it from the service's SSH target.
type DropletPostgresChecker struct {
	Client      *digitalocean.Client
	Credentials Credentials
	Dialer      planning.TargetDialer
}

type privateDatabase struct {
	id, token, uri, host string
}

func (c *DropletPostgresChecker) database(ctx context.Context, org string, db *planning.Binding) (privateDatabase, error) {
	var cfg planning.ResourceConfig
	if c == nil || c.Client == nil || c.Credentials == nil || db == nil || db.OrgID != org || db.Provider != "digitalocean" || db.Subject != planning.ResourceSubject || db.ConnectionID == "" || json.Unmarshal(db.Config, &cfg) != nil || cfg.Kind != "postgres" || cfg.ExistingResource != nil {
		return privateDatabase{}, errors.New("the managed Postgres binding is unavailable")
	}
	id, ok := strings.CutPrefix(db.ExternalID, doDatabasePrefix)
	if !ok || id == "" {
		return privateDatabase{}, errors.New("the DigitalOcean cluster ID is missing")
	}
	conn, raw, err := c.Credentials.Credential(ctx, org, db.ConnectionID)
	if err != nil || conn == nil || conn.ID != db.ConnectionID || conn.OrgID != org || conn.Cloud != "digitalocean" || conn.Status != connections.StatusReady {
		return privateDatabase{}, errors.New("the DigitalOcean database connection is unavailable")
	}
	token := strings.TrimSpace(string(raw))
	cluster, err := c.Client.GetDatabase(ctx, token, id)
	if err != nil || cluster.ID != id || cluster.Engine != digitalocean.Postgres || cluster.PrivateConnection == nil {
		return privateDatabase{}, errors.New("could not read the cluster's private Postgres endpoint from DigitalOcean")
	}
	uri := cluster.PrivateConnection.URI
	endpoint, err := privatePostgresURL(uri)
	if err != nil {
		return privateDatabase{}, err
	}
	return privateDatabase{id: id, token: token, uri: uri, host: endpoint.Host}, nil
}

func privatePostgresURL(uri string) (*url.URL, error) {
	u, err := url.Parse(uri)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.User == nil || u.User.Username() == "" || !strings.HasSuffix(strings.ToLower(u.Hostname()), ".db.ondigitalocean.com") || u.Port() == "" || u.Fragment != "" {
		return nil, errors.New("the cluster has no usable private Postgres endpoint")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) != 1 || len(q["sslmode"]) != 1 || (q.Get("sslmode") != "require" && q.Get("sslmode") != "verify-full") {
		return nil, errors.New("the private database URL must request TLS")
	}
	return u, nil
}

// Check opens a verified TLS session and reads the server version over the selected SSH connection.
func (c *DropletPostgresChecker) Check(ctx context.Context, org, target string, db *planning.Binding) error {
	return c.check(ctx, org, target, db, nil)
}

func (c *DropletPostgresChecker) check(ctx context.Context, org, target string, db *planning.Binding, applied *string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	info, err := c.database(ctx, org, db)
	if err != nil {
		return err
	}
	uri := info.uri
	if applied != nil {
		got, parseErr := privatePostgresURL(*applied)
		if parseErr != nil || !strings.EqualFold(got.Host, info.host) {
			return errors.New("the service variable must use the cluster's private Postgres endpoint with TLS")
		}
		uri = *applied
	}
	pem, err := c.Client.DatabaseCA(ctx, info.token, info.id)
	roots := x509.NewCertPool()
	if err != nil || !roots.AppendCertsFromPEM(pem) {
		return errors.New("could not verify the private database certificate authority")
	}
	if c.Dialer == nil || target == "" {
		return errors.New("the Droplet database verifier is unavailable")
	}
	dial, closer, err := c.Dialer.TargetDial(ctx, org, target)
	if closer != nil {
		// Closing the SSH session interrupts a stalled read even when the channel does not support deadlines.
		stop := context.AfterFunc(ctx, func() { _ = closer.Close() })
		defer func() {
			if stop() {
				_ = closer.Close()
			}
		}()
	}
	if err != nil || dial == nil {
		return errors.New("could not open the Droplet SSH connection")
	}
	_, err = (catalogconnect.PostgresProber{Roots: roots}).Probe(ctx, dial, uri, recipes.Requirement{})
	if catalogconnect.RefusedProbe(err) {
		return errors.New("the database refused the private connection from the Droplet. Check database trusted sources and credentials")
	}
	if err != nil {
		return errors.New("could not establish a private TLS connection from the Droplet. Check the VPC route, database trusted sources and certificate")
	}
	return nil
}

func (r *Runner) serviceResourceAddress(ctx context.Context, d *Deployment, app string, services, resources map[string]*planning.Binding, link planning.EnvLink) (value string, extra map[string]linkExtra, err error) {
	if !link.DropletPostgres {
		return r.resourceAddress(ctx, d, app, services, link)
	}
	db := resources[link.From]
	if db == nil || db.ProjectID != d.ProjectID || db.EnvironmentID != d.EnvironmentID || db.Name != link.From {
		return "", nil, errors.New("the reviewed private database binding changed. Plan again")
	}
	info, err := r.dropletPostgres.database(ctx, d.OrgID, db)
	return info.uri, nil, err
}

func (r *Runner) checkDropletPostgres(ctx context.Context, job *Job, resources map[string]*planning.Binding) error {
	type route struct{ resource, uri string }
	verified := map[route]bool{}
	for _, link := range job.Action.Service.Env {
		if !link.DropletPostgres {
			continue
		}
		target := job.Action.Service.Target
		db := resources[link.From]
		if link.Subject != planning.ResourceSubject || job.Action.Provider != "server" || job.Connection == nil || target == "" || job.Connection.ID != target || job.Connection.Cloud != "server" || db == nil || db.EnvironmentID != job.Deployment.EnvironmentID || db.ProjectID != job.Deployment.ProjectID || db.Name != link.From {
			return fmt.Errorf("the reviewed Droplet or Postgres binding for %s changed. Plan again", link.From)
		}
		key := route{link.From, job.Env[link.Key]}
		if verified[key] {
			continue
		}
		if err := r.dropletPostgres.check(ctx, job.Deployment.OrgID, target, db, &key.uri); err != nil {
			return fmt.Errorf("check %s for %s: %w", link.Key, link.From, err)
		}
		verified[key] = true
	}
	return nil
}
