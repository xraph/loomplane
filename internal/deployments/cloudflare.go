package deployments

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"unicode"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// CloudflareEndpoints reveals reviewed application credentials only to the native executor.
type CloudflareEndpoints interface {
	Get(context.Context, string, string) (*catalogconnect.Endpoint, error)
	Resolve(context.Context, string, string) (*catalogconnect.Endpoint, string, error)
}

// CloudflareAuthorizer checks current membership and access to application credentials.
type CloudflareAuthorizer interface {
	Require(context.Context, security.Principal, security.Permission, string) error
	RequireCurrentMembership(context.Context, security.Principal) error
}

// Cloudflare deploys prebundled Worker modules and manages native account resources.
type Cloudflare struct {
	Client     *cloudflare.Client
	Endpoints  CloudflareEndpoints
	Authorizer CloudflareAuthorizer
}

var _ Executor = (*Cloudflare)(nil)

// RetainCheckpointOnRetry preserves a native identity after an interrupted API request.
func (*Cloudflare) RetainCheckpointOnRetry() bool { return true }

type cloudflareWorkerState struct {
	Account        string                           `json:"account"`
	Script         string                           `json:"script"`
	Owner          string                           `json:"owner"`
	Release        string                           `json:"release"`
	CreatedAt      string                           `json:"created_at,omitempty"`
	Revision       string                           `json:"revision,omitempty"`
	Configuration  string                           `json:"configuration"`
	CreatedClasses []string                         `json:"created_classes,omitempty"`
	Namespaces     []CloudflareWorkerOwnedNamespace `json:"namespaces,omitempty"`
}

func cloudflareAccount(conn *connections.Connection) (string, error) {
	if conn == nil || conn.Cloud != connections.Cloudflare || len(conn.Details["account_id"]) != 32 {
		return "", errors.New("Cloudflare deployment needs its reviewed account connection")
	}
	return conn.Details["account_id"], nil
}

func cloudflareIdentity(org, project, environment, name string) string {
	sum := sha256.Sum256([]byte(org + "\x00" + project + "\x00" + environment + "\x00" + name))
	return hex.EncodeToString(sum[:])[:24]
}

// CloudflareWorkerName gives each service and environment a deterministic native script name.
func CloudflareWorkerName(job *Job) string {
	name := strings.Trim(strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, strings.ToLower(job.Action.Name)), "-")
	if name == "" {
		name = "worker"
	}
	if len(name) > 30 {
		name = name[:30]
	}
	return name + "-" + cloudflareIdentity(job.Deployment.OrgID, job.Deployment.ProjectID, job.Deployment.EnvironmentID, job.Action.Name)
}

func cloudflareOwner(org, project, environment, name string) string {
	return "loomplane-owner-" + cloudflareIdentity(org, project, environment, name)
}
func cloudflarePhysical(id, created string) string {
	if created == "" {
		return ""
	}
	return id + "@" + created
}

func cloudflareSave(job *Job, state any) error {
	if job.Checkpoint == nil {
		return errors.New("Cloudflare deployment requires a durable identity checkpoint")
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return job.Checkpoint(raw)
}

// Apply deploys a Worker or creates the action's native account resource.
func (c *Cloudflare) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	if job.Action.Resource != nil {
		return c.applyCloudflareResource(ctx, job)
	}
	cfg := job.Action.Service
	if cfg == nil || cfg.Worker == nil || job.Deployment == nil || job.Plan == nil || c.Client == nil {
		return nil, errors.New("Cloudflare Workers needs reviewed Worker module settings")
	}
	modules, err := cloudflareWorkerModules(ctx, job)
	if err != nil {
		return nil, err
	}
	account, err := cloudflareAccount(job.Connection)
	if err != nil {
		return nil, err
	}
	owner := cloudflareOwner(job.Deployment.OrgID, job.Deployment.ProjectID, job.Deployment.EnvironmentID, job.Action.Name)
	release := "loomplane-release-" + cloudflareIdentity(job.Plan.ID, job.SourceID, job.Deployment.ID, job.Action.Name)
	state := cloudflareWorkerState{Account: account, Script: CloudflareWorkerName(job), Owner: owner, Release: release, Configuration: cloudflareConfigurationDigest(job.Action.Name, cfg)}
	if len(job.Resume) > 0 {
		var previous cloudflareWorkerState
		if json.Unmarshal(job.Resume, &previous) != nil || previous.Account != state.Account || previous.Script != state.Script || previous.Owner != state.Owner || previous.Release != state.Release || previous.Configuration != state.Configuration {
			return nil, errors.New("Cloudflare Worker checkpoint no longer matches its reviewed deployment")
		}
		state = previous
	}
	current, err := c.Client.GetWorkerSettings(ctx, string(job.Credential), account, state.Script)
	exists := !cloudflareNotFound(err)
	if err != nil && exists {
		return nil, err
	}
	if !exists && (state.CreatedAt != "" || job.Binding != nil) {
		return nil, errors.New("the original Cloudflare Worker is missing; review a replacement")
	}
	if exists && !slices.Contains(current.Tags, owner) {
		return nil, errors.New("the Cloudflare Worker name belongs to another deployment")
	}
	if job.Binding != nil && job.Binding.ExternalID != state.Script {
		return nil, errors.New("the Cloudflare Worker binding identity changed")
	}
	if exists {
		native, nativeErr := c.Client.GetWorkerScript(ctx, string(job.Credential), account, state.Script)
		if nativeErr != nil {
			return nil, nativeErr
		}
		if native.CreatedAt == "" || state.CreatedAt != "" && native.CreatedAt != state.CreatedAt || job.Binding != nil && job.Binding.PhysicalID != cloudflarePhysical(state.Script, native.CreatedAt) {
			return nil, errors.New("the Cloudflare Worker incarnation changed")
		}
		state.CreatedAt = native.CreatedAt
	}
	if len(state.Namespaces) == 0 {
		prior, receiptErr := cloudflarePriorNamespaces(job.Binding, state)
		if receiptErr != nil {
			return nil, receiptErr
		}
		state.Namespaces = prior
	}
	bindings, err := c.cloudflareWorkerBindings(ctx, job, account)
	if err != nil {
		return nil, err
	}
	if !exists || !slices.Contains(current.Tags, release) {
		upload := cloudflare.WorkerUpload{MainModule: cfg.Worker.Main, CompatibilityDate: cfg.Worker.CompatibilityDate, CompatibilityFlags: cfg.Worker.CompatibilityFlags, Modules: modules, Bindings: bindings, Tags: []string{owner, release}}
		if upload.MainModule == "" {
			upload.MainModule = "worker.js"
		}
		if len(cfg.Worker.DurableObjects) > 0 || len(state.Namespaces) > 0 {
			namespaces, listErr := c.Client.ListResources(ctx, string(job.Credential), account, cloudflare.ResourceDurableObject)
			if listErr != nil {
				return nil, listErr
			}
			preflightState := state
			preflightState.CreatedClasses = nil
			if receiptErr := cloudflareConfirmNamespaces(&preflightState, namespaces); receiptErr != nil {
				return nil, receiptErr
			}
			existingClasses := map[string]bool{}
			for _, namespace := range namespaces {
				if namespace.Metadata["script"] == state.Script {
					existingClasses[namespace.Metadata["class"]] = true
				}
			}
			var added []string
			for _, class := range cfg.Worker.DurableObjects {
				if !existingClasses[class.ClassName] {
					added = append(added, class.ClassName)
				}
			}
			state.CreatedClasses = added
			if len(added) > 0 {
				oldTag := ""
				if exists {
					native, nativeErr := c.Client.GetWorkerScript(ctx, string(job.Credential), account, state.Script)
					if nativeErr != nil {
						return nil, nativeErr
					}
					oldTag = native.MigrationTag
				}
				upload.Migrations = &cloudflare.WorkerMigrations{OldTag: oldTag, NewTag: release, NewSQLiteClasses: added}
			}
		}
		if saveErr := cloudflareSave(job, state); saveErr != nil {
			return nil, saveErr
		}
		if job.Progress != nil {
			job.Progress(StepDeploying, "Uploading the pinned Worker modules and native bindings")
		}
		uploaded, uploadErr := c.Client.UploadWorker(ctx, string(job.Credential), account, state.Script, upload)
		if uploadErr != nil {
			return nil, fmt.Errorf("upload the prebundled Worker ESM modules; compile TypeScript and package imports before deploying, and check exported Durable Object classes: %w", uploadErr)
		}
		if uploaded.CreatedAt == "" {
			return nil, errors.New("Cloudflare Worker upload did not return its creation identity")
		}
		state.CreatedAt = uploaded.CreatedAt
		if saveErr := cloudflareSave(job, state); saveErr != nil {
			return nil, saveErr
		}
	}
	verified, err := c.Client.GetWorkerSettings(ctx, string(job.Credential), account, state.Script)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(verified.Tags, owner) || !slices.Contains(verified.Tags, release) {
		return nil, errors.New("Cloudflare Worker changed while its deployment was being verified")
	}
	active, err := c.Client.GetWorkerDeployment(ctx, string(job.Credential), account, state.Script)
	if err != nil {
		return nil, err
	}
	if len(active.Versions) != 1 || active.Versions[0].Percentage != 100 {
		return nil, errors.New("Cloudflare Worker is serving an unexpected traffic split")
	}
	if state.Revision != "" && state.Revision != active.Revision() {
		return nil, errors.New("Cloudflare Worker active version changed during deployment")
	}
	state.Revision = active.Revision()
	if len(cfg.Worker.DurableObjects) > 0 || len(state.Namespaces) > 0 {
		namespaces, listErr := c.Client.ListResources(ctx, string(job.Credential), account, cloudflare.ResourceDurableObject)
		if listErr != nil {
			return nil, listErr
		}
		if receiptErr := cloudflareConfirmNamespaces(&state, namespaces); receiptErr != nil {
			return nil, receiptErr
		}
		for _, class := range cfg.Worker.DurableObjects {
			found := false
			for _, namespace := range namespaces {
				if namespace.Metadata["class"] == class.ClassName && namespace.Metadata["script"] == state.Script {
					found = true
					break
				}
			}
			if !found {
				return nil, errors.New("Cloudflare has not confirmed the Worker's Durable Object namespace")
			}
		}
	}
	address, err := c.Client.EnableWorkerSubdomain(ctx, string(job.Credential), account, state.Script, cfg.Public)
	if err != nil {
		return nil, err
	}
	if saveErr := cloudflareSave(job, state); saveErr != nil {
		return nil, saveErr
	}
	owned, marshalErr := cloudflareWorkerOwnedJSON(state)
	if marshalErr != nil {
		return nil, marshalErr
	}
	return &Outcome{ExternalID: state.Script, PhysicalID: cloudflarePhysical(state.Script, state.CreatedAt), URL: address, Revision: state.Revision, OwnedResources: owned}, nil
}

func cloudflareNotFound(err error) bool {
	var ae *cloudflare.APIError
	return errors.As(err, &ae) && ae.Status == httpStatusNotFound
}

const httpStatusNotFound = 404

// Remove deletes only a script with the binding's original ownership and creation identity.
func (c *Cloudflare) Remove(ctx context.Context, job *Job, b *planning.Binding) error {
	if b.Subject == planning.ResourceSubject {
		return c.removeCloudflareResource(ctx, job, b)
	}
	account, err := cloudflareAccount(job.Connection)
	if err != nil {
		return err
	}
	settings, err := c.Client.GetWorkerSettings(ctx, string(job.Credential), account, b.ExternalID)
	if cloudflareNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !slices.Contains(settings.Tags, cloudflareOwner(b.OrgID, b.ProjectID, b.EnvironmentID, b.Name)) {
		return errors.New("Cloudflare Worker ownership changed; removal stopped")
	}
	native, err := c.Client.GetWorkerScript(ctx, string(job.Credential), account, b.ExternalID)
	if err != nil {
		return err
	}
	if b.PhysicalID == "" || b.PhysicalID != cloudflarePhysical(b.ExternalID, native.CreatedAt) {
		return errors.New("Cloudflare Worker incarnation changed; removal stopped")
	}
	namespaces, namespaceErr := c.Client.ListResources(ctx, string(job.Credential), account, cloudflare.ResourceDurableObject)
	if namespaceErr != nil {
		return namespaceErr
	}
	for _, namespace := range namespaces {
		if namespace.Metadata["script"] == b.ExternalID {
			return errors.New("the Worker still owns Durable Object data; review and remove its class lifecycle before deleting the Worker")
		}
	}
	return c.Client.DeleteWorker(ctx, string(job.Credential), account, b.ExternalID)
}

// Observe reads the active native version without exposing source or runtime values.
func (c *Cloudflare) Observe(ctx context.Context, conn *connections.Connection, credential []byte, b *planning.Binding) (*Observation, error) {
	if b.Subject == planning.ResourceSubject {
		return c.observeCloudflareResource(ctx, conn, credential, b)
	}
	account, err := cloudflareAccount(conn)
	if err != nil {
		return nil, err
	}
	settings, err := c.Client.GetWorkerSettings(ctx, string(credential), account, b.ExternalID)
	if cloudflareNotFound(err) {
		return &Observation{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !slices.Contains(settings.Tags, cloudflareOwner(b.OrgID, b.ProjectID, b.EnvironmentID, b.Name)) {
		return nil, errors.New("Cloudflare Worker ownership changed")
	}
	native, err := c.Client.GetWorkerScript(ctx, string(credential), account, b.ExternalID)
	if err != nil {
		return nil, err
	}
	if b.PhysicalID == "" || b.PhysicalID != cloudflarePhysical(b.ExternalID, native.CreatedAt) {
		return nil, errors.New("Cloudflare Worker incarnation changed")
	}
	active, err := c.Client.GetWorkerDeployment(ctx, string(credential), account, b.ExternalID)
	if err != nil {
		return nil, err
	}
	return &Observation{Exists: true, Revision: active.Revision()}, nil
}

func cloudflareWorkerModules(ctx context.Context, job *Job) ([]cloudflare.WorkerModule, error) {
	cfg := job.Action.Service
	if cfg == nil || cfg.Worker == nil || job.Source == nil || job.CommitSHA == "" || len(job.SourceID) < 12 {
		return nil, errors.New("Cloudflare Workers requires source pinned to the reviewed commit")
	}
	if cfg.Build != "" || cfg.Image != "" {
		return nil, errors.New("Cloudflare Workers deploys prebundled JavaScript ESM; compile the Worker in your build pipeline and remove the deployment build command")
	}
	root := cfg.Source
	if root == "" {
		root = "."
	}
	if path.Clean(root) != root || strings.HasPrefix(root, "/") || root == ".." || strings.HasPrefix(root, "../") || strings.ContainsAny(root, "\\\x00") {
		return nil, errors.New("the Worker source directory is invalid")
	}
	main := cfg.Worker.Main
	if main == "" {
		main = "worker.js"
	}
	if !cloudflare.ValidWorkerModuleName(main) {
		return nil, errors.New("worker main must be a prebundled .js or .mjs module relative to the service source directory")
	}
	source, _, err := job.Source(ctx)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	zr, err := gzip.NewReader(source)
	if err != nil {
		return nil, errors.New("read the pinned Worker source archive")
	}
	defer zr.Close()
	tr := tar.NewReader(io.LimitReader(zr, 100<<20))
	modules := []cloudflare.WorkerModule{}
	seen := map[string]bool{}
	prefix := ""
	total := int64(0)
	for count := 0; count < 20000; count++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		header, readErr := tr.Next()
		if errors.Is(readErr, io.EOF) {
			if !seen[main] {
				return nil, errors.New("the pinned source does not contain Worker main; commit its prebundled JavaScript output")
			}
			return modules, nil
		}
		if readErr != nil {
			return nil, errors.New("read the pinned Worker source archive")
		}
		name := strings.TrimPrefix(header.Name, "./")
		if strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\\x00") || strings.ContainsFunc(name, unicode.IsControl) || path.Clean(name) != strings.TrimSuffix(name, "/") || strings.HasPrefix(name, "../") {
			return nil, errors.New("the Worker source archive contains an unsafe path")
		}
		top, relative, ok := strings.Cut(name, "/")
		if !ok || relative == "" {
			continue
		}
		if prefix == "" {
			prefix = top
		}
		if prefix != top {
			return nil, errors.New("the Worker source archive contains more than one repository root")
		}
		if root != "." {
			relative, ok = strings.CutPrefix(relative, root+"/")
			if !ok {
				continue
			}
		}
		if strings.HasPrefix(relative, "node_modules/") || strings.Contains(relative, "/node_modules/") || strings.HasPrefix(relative, ".git/") {
			continue
		}
		if !cloudflare.ValidWorkerModuleName(relative) {
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > 2<<20 || seen[relative] || len(modules) >= 100 {
			return nil, errors.New("worker modules must be unique regular files no larger than 2 MiB, with at most 100 modules")
		}
		total += header.Size
		if total > 10<<20 {
			return nil, errors.New("prebundle the Worker below the 10 MiB upload limit")
		}
		content, readErr := io.ReadAll(tr)
		if readErr != nil {
			return nil, errors.New("read a Worker module from the pinned source")
		}
		modules = append(modules, cloudflare.WorkerModule{Name: relative, Content: content})
		seen[relative] = true
	}
	return nil, errors.New("the Worker source archive exceeds the file limit")
}
