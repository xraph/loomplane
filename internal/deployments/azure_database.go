package deployments

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/clouds/progress"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

// AzureDatabase makes Postgres and MySQL as Azure Database flexible
// servers in the connection's resource group: public access over TLS,
// with a firewall rule that lets in Azure's services, Container Apps
// among them. Its admin user's password is one Loomplane generates and
// keeps in Vault.
type AzureDatabase struct {
	Client *azure.Client
}

var (
	_ Executor             = (*AzureDatabase)(nil)
	_ Locator              = (*AzureDatabase)(nil)
	_ PendingCloudResource = (*AzureDatabase)(nil)
)

// azureDatabaseLogin is the admin user of every flexible server. Azure
// reserves names such as admin and root; this isn't one of them.
const azureDatabaseLogin = "loomplane"

// azureDatabases are the engines azure-database makes, by resource kind:
// the port they take connections on, the database the address names, and
// the version a resource gets when it names none.
var azureDatabases = map[string]struct {
	port            int
	database, title string
	version         string
}{
	"postgres": {port: 5432, database: "postgres", title: "PostgreSQL", version: "17"},
	"mysql":    {port: 3306, database: "mysql", title: "MySQL", version: "8.4"},
}

// azureDatabaseState is what an azure-database action checkpoints: the
// server it makes, before it makes it, so cleanup can find it.
type azureDatabaseState struct {
	ServerID string `json:"server_id,omitempty"`
}

// AzureDatabaseName is the flexible server a resource runs as in an
// environment. Server names are DNS names across all of Azure, so it ends
// in a hash of the subscription; it's at most 39 characters, well within
// Azure's 63.
func AzureDatabaseName(job *Job, subscription string) string {
	return azureServerName(doName(job.Action.Name+"-"+job.Deployment.Environment+"-"+idSuffix(job.Deployment.ProjectID)), subscription)
}

func azureServerName(base, subscription string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(subscription)))
	return base + "-" + hex.EncodeToString(sum[:])[:6]
}

// azurePasswordChars are what a password is made of: 64 characters, so a
// random byte's low six bits pick one evenly.
const azurePasswordChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

// azurePassword is a new admin password: 32 characters with at least one
// upper case letter, one lower case letter and one digit, which is three
// of the four kinds of character Azure asks for.
func azurePassword() (string, error) {
	buf := make([]byte, 32)
	for {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for i, b := range buf {
			buf[i] = azurePasswordChars[b&63]
		}
		p := string(buf)
		if strings.ContainsAny(p, azurePasswordChars[:26]) && strings.ContainsAny(p, azurePasswordChars[26:52]) && strings.ContainsAny(p, azurePasswordChars[52:62]) {
			return p, nil
		}
	}
}

// Apply implements Executor.
func (a *AzureDatabase) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	cfg := job.Action.Resource
	if cfg == nil {
		return nil, fmt.Errorf("%w: azure-database provisions databases, not %s", ErrNoExecutor, job.Action.Subject)
	}
	engine, ok := azureDatabases[cfg.Kind]
	if !ok {
		return nil, fmt.Errorf("%w: azure-database provisions postgres and mysql, not %s", ErrNoExecutor, cfg.Kind)
	}
	s, err := azureSession(ctx, a.Client, job.Connection, job.Credential)
	if err != nil {
		return nil, err
	}
	location := cmp.Or(job.Action.Region, cfg.Region, azureRegion)
	fresh, err := azurePassword()
	if err != nil {
		return nil, err
	}
	password, err := keptPassword(ctx, job, fresh)
	if err != nil {
		return nil, err
	}
	name := a.serverName(s, job, cfg.Kind)
	state := azureDatabaseState{ServerID: s.FlexibleServerPath(cfg.Kind, name)}
	if cerr := azureDatabaseCheckpoint(job, state); cerr != nil {
		return nil, cerr
	}
	ctx = progress.With(ctx, func(m string) { job.Progress(StepDeploying, m) })

	job.Progress(StepDeploying, "Setting up the resource group")
	if gerr := s.EnsureGroup(ctx, location); gerr != nil {
		return nil, fmt.Errorf("make resource group %s: %w", s.Credential().ResourceGroup, gerr)
	}
	job.Progress(StepDeploying, "Making the "+engine.title+" flexible server "+name+"; this takes several minutes")
	fqdn, err := s.EnsureFlexibleServer(ctx, azure.FlexibleServer{
		Engine: cfg.Kind, Name: name, Location: location, Version: cmp.Or(cfg.Version, engine.version),
		Login: azureDatabaseLogin, Password: password,
	})
	if err != nil {
		return nil, err
	}
	addr := azureDatabaseURL(cfg.Kind, password, fqdn)
	if kerr := job.Keep(ctx, ResourceURL, []byte(addr)); kerr != nil {
		return nil, kerr
	}
	return &Outcome{ExternalID: state.ServerID, Revision: cfg.Kind}, nil
}

// azureDatabaseCheckpoint saves the server or cluster an action makes. A
// cloud sync must save it before it's made; otherwise a failed save is
// tried again with the next heartbeat.
func azureDatabaseCheckpoint(job *Job, state any) error {
	if job.Plan != nil && job.Plan.CloudSyncID != "" {
		return checkpointCloudIdentity(job, state)
	}
	if job.Checkpoint != nil {
		data, _ := json.Marshal(state) //nolint:errcheck // a plain struct always encodes
		_ = job.Checkpoint(data)       //nolint:errcheck // the next heartbeat retries it
	}
	return nil
}

// serverName is the flexible server to make: the bound one, or the
// action's name. One the action replaces in the same subscription, as
// after a move to another region, keeps that name, so this one takes
// another.
func (a *AzureDatabase) serverName(s *azure.Session, job *Job, kind string) string {
	return azureGlobalName(job, s.Credential().SubscriptionID, func(name string) string { return s.FlexibleServerPath(kind, name) })
}

// azureGlobalName is the globally named resource an action makes, whose
// resource ID pathOf gives: the bound one, or the action's name. One the
// action replaces in the same subscription, as after a move to another
// region, keeps that name, so this one takes another.
func azureGlobalName(job *Job, subscription string, pathOf func(name string) string) string {
	if job.Binding != nil && strings.EqualFold(job.Binding.ExternalID, pathOf(path.Base(job.Binding.ExternalID))) {
		return path.Base(job.Binding.ExternalID)
	}
	name := AzureDatabaseName(job, subscription)
	if job.Replaces != nil && strings.EqualFold(job.Replaces.ExternalID, pathOf(name)) {
		name = azureServerName(doName(job.Action.Name+"-"+job.Deployment.Environment+"-"+idSuffix(job.Deployment.ProjectID)+"-2"), subscription)
	}
	return name
}

// azureDatabaseURL is how a service reaches the database, over TLS, which
// flexible servers require.
func azureDatabaseURL(kind, password, host string) string {
	engine := azureDatabases[kind]
	creds := url.UserPassword(azureDatabaseLogin, password).String()
	hostPort := host + ":" + strconv.Itoa(engine.port)
	if kind == "mysql" {
		return "mysql://" + creds + "@" + hostPort + "/" + engine.database + "?tls=true"
	}
	return "postgresql://" + creds + "@" + hostPort + "/" + engine.database + "?sslmode=require"
}

// Remove implements Executor. A database goes with its data, and then
// what Loomplane kept for it.
func (a *AzureDatabase) Remove(ctx context.Context, job *Job, b *planning.Binding) error {
	if b.ExternalID != "" {
		s, err := azureSession(ctx, a.Client, job.Connection, job.Credential)
		if err != nil {
			return err
		}
		ctx = progress.With(ctx, func(m string) { job.Progress(StepDeploying, m) })
		if derr := s.DeleteFlexibleServer(ctx, b.ExternalID); derr != nil {
			return derr
		}
	}
	return job.Forget(ctx)
}

// Observe implements Executor: what's live is the server's engine.
func (a *AzureDatabase) Observe(ctx context.Context, conn *connections.Connection, cred []byte, b *planning.Binding) (*Observation, error) {
	s, err := azureSession(ctx, a.Client, conn, cred)
	if err != nil {
		return nil, err
	}
	_, err = s.GetFlexibleServer(ctx, b.ExternalID)
	if azure.IsNotFound(err) {
		return &Observation{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &Observation{Exists: true, Revision: azure.FlexibleServerEngine(b.ExternalID)}, nil
}

// Locate implements Locator.
func (a *AzureDatabase) Locate(b *planning.Binding) string {
	engine, ok := azureDatabases[azure.FlexibleServerEngine(b.ExternalID)]
	if b.ExternalID == "" || !ok {
		return ""
	}
	return fmt.Sprintf("the %s flexible server %s in %s", engine.title, path.Base(b.ExternalID), b.Region)
}

// PendingCloudResource reads the exact flexible server the operation made.
func (a *AzureDatabase) PendingCloudResource(_ context.Context, job *Job) (string, error) {
	return pendingCheckpointID(job.Resume, "server_id")
}

// AzureRedis makes Redis as Azure Managed Redis in the connection's
// resource group: the smallest Balanced cluster, with one database that
// takes TLS connections and an access key. The key is read from Azure on
// every apply, so only the address is kept.
type AzureRedis struct {
	Client *azure.Client
}

var (
	_ Executor             = (*AzureRedis)(nil)
	_ Locator              = (*AzureRedis)(nil)
	_ PendingCloudResource = (*AzureRedis)(nil)
)

// azureRedisRevision is what an azure-managed-redis binding runs.
const azureRedisRevision = "redis"

// azureRedisState is what an azure-managed-redis action checkpoints: the
// cluster it makes, before it makes it, so cleanup can find it.
type azureRedisState struct {
	ClusterID string `json:"cluster_id,omitempty"`
}

// Apply implements Executor.
func (a *AzureRedis) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	cfg := job.Action.Resource
	if cfg == nil || cfg.Kind != "redis" {
		return nil, fmt.Errorf("%w: azure-managed-redis provisions redis, not %s", ErrNoExecutor, job.Action.Subject)
	}
	s, err := azureSession(ctx, a.Client, job.Connection, job.Credential)
	if err != nil {
		return nil, err
	}
	location := cmp.Or(job.Action.Region, cfg.Region, azureRegion)
	// Cluster names are DNS names across Azure, like flexible servers'.
	name := azureGlobalName(job, s.Credential().SubscriptionID, s.ManagedRedisPath)
	state := azureRedisState{ClusterID: s.ManagedRedisPath(name)}
	if cerr := azureDatabaseCheckpoint(job, state); cerr != nil {
		return nil, cerr
	}
	ctx = progress.With(ctx, func(m string) { job.Progress(StepDeploying, m) })

	job.Progress(StepDeploying, "Setting up the resource group")
	if gerr := s.EnsureGroup(ctx, location); gerr != nil {
		return nil, fmt.Errorf("make resource group %s: %w", s.Credential().ResourceGroup, gerr)
	}
	job.Progress(StepDeploying, "Making the Managed Redis cluster "+name+"; this takes several minutes")
	host, key, err := s.EnsureManagedRedis(ctx, name, location)
	if err != nil {
		return nil, err
	}
	if kerr := job.Keep(ctx, ResourceURL, []byte(azureRedisURL(key, host))); kerr != nil {
		return nil, kerr
	}
	return &Outcome{ExternalID: state.ClusterID, Revision: azureRedisRevision}, nil
}

// azureRedisURL is how a service reaches Managed Redis: over TLS, with
// the access key as the password.
func azureRedisURL(key, host string) string {
	return "rediss://" + url.UserPassword("", key).String() + "@" + host + ":" + strconv.Itoa(azure.ManagedRedisPort)
}

// Remove implements Executor. The cluster goes with its data, and then
// what Loomplane kept for it.
func (a *AzureRedis) Remove(ctx context.Context, job *Job, b *planning.Binding) error {
	if b.ExternalID != "" {
		s, err := azureSession(ctx, a.Client, job.Connection, job.Credential)
		if err != nil {
			return err
		}
		ctx = progress.With(ctx, func(m string) { job.Progress(StepDeploying, m) })
		if derr := s.DeleteManagedRedis(ctx, b.ExternalID); derr != nil {
			return derr
		}
	}
	return job.Forget(ctx)
}

// Observe implements Executor.
func (a *AzureRedis) Observe(ctx context.Context, conn *connections.Connection, cred []byte, b *planning.Binding) (*Observation, error) {
	s, err := azureSession(ctx, a.Client, conn, cred)
	if err != nil {
		return nil, err
	}
	_, err = s.GetManagedRedis(ctx, b.ExternalID)
	if azure.IsNotFound(err) {
		return &Observation{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &Observation{Exists: true, Revision: azureRedisRevision}, nil
}

// Locate implements Locator.
func (a *AzureRedis) Locate(b *planning.Binding) string {
	if b.ExternalID == "" || !azure.IsManagedRedis(b.ExternalID) {
		return ""
	}
	return fmt.Sprintf("the Azure Managed Redis cluster %s in %s", path.Base(b.ExternalID), b.Region)
}

// PendingCloudResource reads the exact cluster the operation made.
func (a *AzureRedis) PendingCloudResource(_ context.Context, job *Job) (string, error) {
	return pendingCheckpointID(job.Resume, "cluster_id")
}
