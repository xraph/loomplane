package deployments

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

// The proof and its random MAC key stay in the service's Vault scope. Public
// release metadata never contains a digest that could guess a weak password.
type doRollbackProof struct {
	Version  int               `json:"version"`
	Scope    map[string]string `json:"scope"`
	Key      []byte            `json:"key"`
	Desired  []byte            `json:"desired"`
	Accepted []byte            `json:"accepted"`
	Actual   string            `json:"actual"`
}

const doRollbackProofPrefix = "app-platform-rollback:"

func doPendingProofKey(job *Job) string {
	return "app-platform-pending:" + job.Deployment.ID + ":" + job.Action.ID
}

func doProviderSpecMAC(key []byte, digest string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(digest))
	return mac.Sum(nil)
}

// Keep the update response before the deployment starts. Its encrypted SECRET
// values must be the ones the historical deployment later reports.
func prepareDORollbackProof(ctx context.Context, job *Job, desired digitalocean.AppSpec, app *digitalocean.App) error {
	if job.Keep == nil {
		if doHasResourceAccess(job) {
			return doRollbackProofError()
		}
		return nil
	}
	accepted, digest, err := doActualSpec(app.SpecRaw)
	if err != nil || !doReviewedSpecMatches(accepted, desired) {
		return doRollbackProofError()
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return doRollbackProofError()
	}
	proof := doRollbackProof{Version: 1, Scope: doProofScope(job, app.ID, ""), Key: key, Desired: doSpecMAC(key, desired), Accepted: doProviderSpecMAC(key, digest)}
	data, _ := json.Marshal(proof) //nolint:errcheck // this struct contains only JSON values
	if err := job.Keep(ctx, doPendingProofKey(job), data); err != nil {
		return doRollbackProofError()
	}
	return nil
}

func doRollbackProofError() error {
	return errors.New("cannot verify the historical App Platform resource credentials and settings; deploy reviewed source with the current attachments")
}

func doHasResourceAccess(job *Job) bool {
	hasAccess := func(cfg *planning.ServiceConfig) bool {
		return cfg.TargetAccountIdentity != "" || slices.ContainsFunc(cfg.Env, func(link planning.EnvLink) bool { return link.Subject == planning.ResourceSubject })
	}
	if hasAccess(job.Action.Service) {
		return true
	}
	if job.Binding != nil && len(job.Binding.Config) != 0 {
		var previous planning.ServiceConfig
		if json.Unmarshal(job.Binding.Config, &previous) != nil || (previous.Provider == "" && previous.Source == "") {
			return true
		}
		return hasAccess(&previous)
	}
	return false
}

func doProofScope(job *Job, app, revision string) map[string]string {
	account := ""
	if job.Connection != nil {
		account = connections.AccountIdentity(job.Connection.Cloud, job.Connection.Account, job.Connection.Details)
	}
	links, _ := json.Marshal(job.Action.Service.Env)               //nolint:errcheck // links contain only JSON values
	settings, _ := json.Marshal(job.Action.Service.TargetSettings) //nolint:errcheck // settings contain only strings
	return map[string]string{
		"org": job.Deployment.OrgID, "project": job.Deployment.ProjectID,
		"environment": job.Deployment.Environment, "environment_id": job.Deployment.EnvironmentID,
		"action": job.Action.ID, "service": job.Action.Name, "connection": job.Action.ConnectionID,
		"account": account, "reviewed_account": job.Action.Service.TargetAccountIdentity,
		"links": string(links), "target_settings": string(settings), "provider": job.Action.Provider,
		"app": app, "revision": revision, "commit": job.CommitSHA,
	}
}

func doSpecMAC(key []byte, spec digitalocean.AppSpec) []byte {
	// Domains are managed independently. The provider preflight below checks
	// them against the current app before restoring a historical spec.
	spec.Domains = nil
	data, _ := json.Marshal(spec) //nolint:errcheck // this struct contains only JSON values
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(data)
	return mac.Sum(nil)
}

func doActualSpec(raw json.RawMessage) (digitalocean.AppSpec, string, error) {
	var spec digitalocean.AppSpec
	var all map[string]any
	if err := json.Unmarshal(raw, &all); err != nil || len(all) == 0 {
		return spec, "", doRollbackProofError()
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return spec, "", doRollbackProofError()
	}
	// A service app must not hide additional executable components or
	// app-wide variables outside the reviewed service's environment.
	for _, field := range []string{"jobs", "functions", "envs"} {
		if items, exists := all[field]; exists && items != nil {
			if list, ok := items.([]any); !ok || len(list) != 0 {
				return spec, "", doRollbackProofError()
			}
		}
	}
	canonical, _ := json.Marshal(all) //nolint:errcheck // decoded JSON always marshals
	digest := sha256.Sum256(canonical)
	return spec, hex.EncodeToString(digest[:]), nil
}

func doReviewedSpecMatches(actual, desired digitalocean.AppSpec) bool {
	// The provider encrypts SECRET values. Match their names, scope and
	// classification here; the MAC separately binds their submitted values.
	clearSecrets := func(spec *digitalocean.AppSpec) {
		spec.Domains = nil
		for _, components := range [][]digitalocean.Component{spec.Services, spec.Workers, spec.StaticSites} {
			for i := range components {
				components[i].Envs = slices.Clone(components[i].Envs)
				for j := range components[i].Envs {
					if components[i].Envs[j].Type == "SECRET" {
						components[i].Envs[j].Value = ""
					}
				}
				slices.SortFunc(components[i].Envs, func(a, b digitalocean.Env) int { return strings.Compare(a.Key, b.Key) })
			}
		}
	}
	actual.Services, actual.Workers, actual.StaticSites = slices.Clone(actual.Services), slices.Clone(actual.Workers), slices.Clone(actual.StaticSites)
	desired.Services, desired.Workers, desired.StaticSites = slices.Clone(desired.Services), slices.Clone(desired.Workers), slices.Clone(desired.StaticSites)
	clearSecrets(&actual)
	clearSecrets(&desired)
	return reflect.DeepEqual(actual, desired)
}

func (d *DigitalOcean) keepRollbackProof(ctx context.Context, token string, job *Job, desired digitalocean.AppSpec, app, revision string) error {
	if job.Keep == nil {
		if doHasResourceAccess(job) {
			return doRollbackProofError()
		}
		return nil
	}
	dep, err := d.Client.GetDeployment(ctx, token, app, revision)
	if err != nil || dep.ID != revision || dep.Phase != digitalocean.PhaseActive || !dep.Built(job.CommitSHA) || len(dep.Services)+len(dep.Workers)+len(dep.StaticSites) != 1 {
		return doRollbackProofError()
	}
	actual, digest, err := doActualSpec(dep.Spec)
	if err != nil || !doReviewedSpecMatches(actual, desired) {
		return doRollbackProofError()
	}
	if job.Recall == nil {
		return doRollbackProofError()
	}
	field, sourceRevision := doPendingProofKey(job), ""
	if artifact := job.Action.Service.Artifact; artifact != nil && artifact.Revision != "" {
		field, sourceRevision = doRollbackProofPrefix+artifact.Revision, artifact.Revision
	}
	data, err := job.Recall(ctx, field)
	if sourceRevision != "" && !doHasResourceAccess(job) && ((err == nil && len(data) == 0) || errors.Is(err, ErrNoCredential)) {
		return nil
	}
	var proof doRollbackProof
	if err != nil || json.Unmarshal(data, &proof) != nil || proof.Version != 1 || len(proof.Key) != 32 || !reflect.DeepEqual(proof.Scope, doProofScope(job, app, sourceRevision)) || !hmac.Equal(proof.Desired, doSpecMAC(proof.Key, desired)) {
		return doRollbackProofError()
	}
	if sourceRevision == "" {
		if !hmac.Equal(proof.Accepted, doProviderSpecMAC(proof.Key, digest)) {
			return doRollbackProofError()
		}
	} else if proof.Actual != digest {
		return doRollbackProofError()
	}
	proof.Scope, proof.Actual = doProofScope(job, app, revision), digest
	data, _ = json.Marshal(proof) //nolint:errcheck // this struct contains only JSON values
	if err := job.Keep(ctx, doRollbackProofPrefix+revision, data); err != nil {
		return doRollbackProofError()
	}
	return nil
}

func (d *DigitalOcean) checkRollbackProof(ctx context.Context, token string, job *Job, app, revision string) error {
	if job.Recall == nil {
		if doHasResourceAccess(job) {
			return doRollbackProofError()
		}
		return nil
	}
	data, err := job.Recall(ctx, doRollbackProofPrefix+revision)
	if err != nil {
		// Legacy releases without external attachments retain their existing
		// rollback path. Other Vault failures stay closed.
		if errors.Is(err, ErrNoCredential) && !doHasResourceAccess(job) {
			return nil
		}
		return doRollbackProofError()
	}
	if len(data) == 0 && !doHasResourceAccess(job) {
		return nil
	}
	var proof doRollbackProof
	if json.Unmarshal(data, &proof) != nil || proof.Version != 1 || len(proof.Key) != 32 || !reflect.DeepEqual(proof.Scope, doProofScope(job, app, revision)) {
		return doRollbackProofError()
	}
	repo, err := repositoryFor(job)
	if err != nil {
		return doRollbackProofError()
	}
	desired := doAppSpec(job, job.Action.Service, repo, doBranchFor(job))
	if !hmac.Equal(proof.Desired, doSpecMAC(proof.Key, desired)) {
		return doRollbackProofError()
	}
	dep, err := d.Client.GetDeployment(ctx, token, app, revision)
	if err != nil || dep.ID != revision || !dep.Built(job.CommitSHA) || len(dep.Services)+len(dep.Workers)+len(dep.StaticSites) != 1 {
		return doRollbackProofError()
	}
	actual, digest, err := doActualSpec(dep.Spec)
	if err != nil || proof.Actual != digest || !doReviewedSpecMatches(actual, desired) {
		return doRollbackProofError()
	}
	live, err := d.Client.GetApp(ctx, token, app)
	if err != nil || live.ID != app || !reflect.DeepEqual(live.Spec.Domains, actual.Domains) {
		return doRollbackProofError()
	}
	return nil
}
