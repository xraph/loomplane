package deployments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

const vercelResourceOwnershipKey = "vercel-resource"

type vercelResourceReceipt struct {
	ResourceID                 string `json:"resource_id"`
	AccountID                  string `json:"account_id"`
	AccountIdentity            string `json:"account_identity"`
	ConnectionID               string `json:"connection_id"`
	OrgID                      string `json:"org_id"`
	ProjectScopeID             string `json:"project_scope_id"`
	EnvironmentID              string `json:"environment_id"`
	Kind                       string `json:"kind"`
	NativeType                 string `json:"native_type"`
	ConfigDigest               string `json:"config_digest"`
	BridgeProjectID            string `json:"bridge_project_id,omitempty"`
	BridgeConnectionID         string `json:"bridge_connection_id,omitempty"`
	ExternalResourceID         string `json:"external_resource_id,omitempty"`
	IntegrationProductID       string `json:"integration_product_id,omitempty"`
	IntegrationConfigurationID string `json:"integration_configuration_id,omitempty"`
}

type vercelResourceState struct {
	Started      bool                   `json:"started"`
	AccountID    string                 `json:"account_id"`
	ConfigDigest string                 `json:"config_digest"`
	Receipt      *vercelResourceReceipt `json:"receipt,omitempty"`
	TokenID      string                 `json:"token_id,omitempty"`
}

func vercelResourceType(kind string) string {
	switch kind {
	case "vercel-blob":
		return "blob"
	case "vercel-global-config":
		return "global-config"
	case "vercel-integration":
		return "integration"
	default:
		return ""
	}
}

func vercelResourceDigest(cfg *planning.ResourceConfig, name string) string {
	raw, err := json.Marshal(struct {
		Name   string
		Config *planning.ResourceConfig
	}{name, cfg})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (v *Vercel) resourceAccount(ctx context.Context, job *Job, cfg *planning.ResourceConfig) (vercel.Token, string, error) {
	if v.Client == nil || job.Connection == nil || job.Deployment == nil || cfg == nil || cfg.ExistingResource != nil || vercelResourceType(cfg.Kind) == "" {
		return vercel.Token{}, "", errors.New("a managed Vercel resource needs its reviewed account and resource settings")
	}
	c := job.Connection
	identity := connections.AccountIdentity(c.Cloud, c.Account, c.Details)
	if c.Cloud != connections.Vercel || c.Status != connections.StatusReady || c.OrgID != job.Deployment.OrgID || identity == "" || cfg.AccountIdentity != identity || job.Action.ConnectionID != "" && job.Action.ConnectionID != c.ID || cfg.TargetSettings != nil && !reflect.DeepEqual(cfg.TargetSettings, planning.ReviewedConnectionSettings(c.Cloud, c.Details)) {
		return vercel.Token{}, "", errors.New("the managed Vercel resource account changed; review the deployment again")
	}
	tok := token(job)
	account, err := v.Client.Check(ctx, tok)
	if err != nil {
		return vercel.Token{}, "", err
	}
	owner := account.TeamID
	if owner == "" {
		owner = account.UserID
	}
	expected := c.Details["team_id"]
	if expected == "" {
		expected = c.Details["user_id"]
	}
	if owner == "" || owner != expected {
		return vercel.Token{}, "", errors.New("vercel did not confirm the reviewed resource account")
	}
	return tok, owner, nil
}

func vercelResourceOptions(job *Job) (vercel.CreateResourceOptions, error) {
	cfg := job.Action.Resource
	s := cfg.Settings
	name := ProjectName(job)
	if cfg.Kind == "vercel-global-config" {
		name = strings.ReplaceAll(name, "-", "_")
	}
	opts := vercel.CreateResourceOptions{Name: name, Region: cfg.Region, Access: s["access"], IntegrationConfigurationID: s["integration_configuration_id"], IntegrationProductIDOrSlug: s["integration_product_id"], BillingPlanID: s["billing_plan_id"], Metadata: map[string]any{}}
	aggregate, metadataErr := vercel.ParseProvisioningMetadata(s["metadata"])
	if metadataErr != nil {
		return vercel.CreateResourceOptions{}, metadataErr
	}
	opts.Metadata = aggregate
	if opts.Region == "" {
		opts.Region = s["region"]
	}
	if cfg.Kind == "vercel-blob" && opts.Access == "" {
		opts.Access = "private"
	}
	if cfg.Kind == "vercel-blob" && opts.Access != "public" && opts.Access != "private" {
		return vercel.CreateResourceOptions{}, errors.New("vercel Blob access must be public or private")
	}
	if cfg.Kind == "vercel-integration" && (opts.IntegrationConfigurationID == "" || opts.IntegrationProductIDOrSlug == "") {
		return vercel.CreateResourceOptions{}, errors.New("vercel Marketplace creation requires an installed integration and selected product")
	}
	if cfg.Kind == "vercel-integration" {
		if _, err := vercelIntegrationOutputKeys(cfg); err != nil {
			return vercel.CreateResourceOptions{}, err
		}
	}
	for key, value := range s {
		if !strings.HasPrefix(key, "metadata.") {
			continue
		}
		key = strings.TrimPrefix(key, "metadata.")
		if _, duplicate := opts.Metadata[key]; duplicate {
			return vercel.CreateResourceOptions{}, errors.New("vercel provisioning metadata cannot repeat an aggregate option through metadata.*")
		}
		lower := strings.ToLower(key)
		if key == "" || strings.ContainsAny(key, ".[]") || strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "credential") || strings.Contains(lower, "connection") {
			return vercel.CreateResourceOptions{}, errors.New("vercel integration metadata must contain reviewed non-secret provisioning settings")
		}
		if parsed, e := url.Parse(value); e == nil && parsed.User != nil {
			return vercel.CreateResourceOptions{}, errors.New("vercel integration metadata cannot contain application credentials")
		}
		var typed any
		if json.Unmarshal([]byte(value), &typed) == nil {
			opts.Metadata[key] = typed
		} else {
			opts.Metadata[key] = value
		}
	}
	encoded, encodeErr := json.Marshal(opts.Metadata)
	if encodeErr != nil {
		return vercel.CreateResourceOptions{}, encodeErr
	}
	validated, validateErr := vercel.ParseProvisioningMetadata(string(encoded))
	if validateErr != nil {
		return vercel.CreateResourceOptions{}, validateErr
	}
	opts.Metadata = validated
	if vercelResourceType(cfg.Kind) != "global-config" && s["project_id"] == "" {
		return vercel.CreateResourceOptions{}, errors.New("vercel Blob and Marketplace resources require a credential bridge project_id")
	}
	return opts, nil
}

func saveVercelResource(job *Job, state vercelResourceState) error {
	if job.Checkpoint == nil {
		return errors.New("a managed Vercel resource requires a durable checkpoint")
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return job.Checkpoint(raw)
}

func (v *Vercel) applyResource(ctx context.Context, job *Job) (out *Outcome, err error) {
	defer nativeDeliveryBoundary(job)(&err)
	cfg := job.Action.Resource
	tok, account, err := v.resourceAccount(ctx, job, cfg)
	if err != nil {
		return nil, err
	}
	if job.Keep == nil || job.Recall == nil {
		return nil, errors.New("managed Vercel application credentials require private storage")
	}
	opts, err := vercelResourceOptions(job)
	if err != nil {
		return nil, err
	}
	digest := vercelResourceDigest(cfg, job.Action.Name)
	state := vercelResourceState{AccountID: account, ConfigDigest: digest}
	if len(job.Resume) > 0 {
		if json.Unmarshal(job.Resume, &state) != nil || state.AccountID != account || state.ConfigDigest != digest {
			return nil, errors.New("the managed Vercel resource checkpoint does not match this reviewed request")
		}
	}
	if job.Binding != nil {
		receipt, e := vercelOwnedResource(job.Binding)
		if e != nil {
			return nil, e
		}
		if state.Receipt != nil && state.Receipt.ResourceID != receipt.ResourceID {
			return nil, errors.New("vercel resource binding and checkpoint disagree")
		}
		state.Receipt = receipt
	}
	if cfg.Kind != "vercel-global-config" {
		if checkErr := v.Client.VerifyResourceProject(ctx, tok, cfg.Settings["project_id"], account); checkErr != nil {
			return nil, checkErr
		}
	}
	if state.Receipt == nil {
		if state.Started {
			return nil, errors.New("the prior Vercel resource create has no confirmed identity; sync and review it before retrying")
		}
		state.Started = true
		if saveErr := saveVercelResource(job, state); saveErr != nil {
			return nil, saveErr
		}
		var resource vercel.Resource
		switch cfg.Kind {
		case "vercel-blob":
			resource, err = v.Client.CreateBlobStore(ctx, tok, opts)
		case "vercel-global-config":
			resource, err = v.Client.CreateGlobalConfig(ctx, tok, opts)
		case "vercel-integration":
			opts.ExternalID = "loomplane-" + digest[:24]
			resource, err = v.Client.CreateIntegrationStore(ctx, tok, opts)
		}
		if err != nil {
			return nil, err
		}
		state.Receipt = &vercelResourceReceipt{ResourceID: resource.ID, AccountID: account, AccountIdentity: cfg.AccountIdentity, ConnectionID: job.Connection.ID, OrgID: job.Deployment.OrgID, ProjectScopeID: job.Deployment.ProjectID, EnvironmentID: job.Deployment.EnvironmentID, Kind: cfg.Kind, NativeType: vercelResourceType(cfg.Kind), ConfigDigest: digest, BridgeProjectID: cfg.Settings["project_id"], ExternalResourceID: resource.ExternalResourceID, IntegrationProductID: resource.Product.ID, IntegrationConfigurationID: resource.Product.IntegrationConfigurationID}
		// Save immutable provider identity before creating tokens or connections.
		if saveErr := saveVercelResource(job, state); saveErr != nil {
			return nil, saveErr
		}
	}
	if checkErr := verifyVercelReceipt(job, cfg, state.Receipt, account, digest); checkErr != nil {
		return nil, checkErr
	}
	resource, err := v.readResource(ctx, tok, state.Receipt)
	if err != nil {
		return nil, err
	}
	if cfg.Kind == "vercel-integration" && (resource.Product.IntegrationConfigurationID != opts.IntegrationConfigurationID || resource.Product.ID != opts.IntegrationProductIDOrSlug && resource.Product.Slug != opts.IntegrationProductIDOrSlug) {
		return nil, errors.New("vercel did not confirm the selected Marketplace installation and product")
	}
	var credentials vercel.CredentialResult
	if cfg.Kind == "vercel-global-config" {
		var kept []byte
		kept, err = job.Recall(ctx, "output:GLOBAL_CONFIG")
		if err == nil && validVercelConfigAccess(string(kept), resource.ID) {
			credentials.Values = map[string]string{"GLOBAL_CONFIG": string(kept)}
		} else {
			if err != nil && !errors.Is(err, ErrNoCredential) {
				return nil, err
			}
			if len(kept) > 0 {
				return nil, errors.New("the kept Global Config read token belongs to a different resource")
			}
			if state.TokenID != "" {
				if revokeErr := v.Client.RevokeGlobalConfigReadToken(ctx, tok, resource.ID, state.TokenID); revokeErr != nil {
					return nil, revokeErr
				}
			}
			credentials, err = v.Client.CreateGlobalConfigReadToken(ctx, tok, resource.ID, "loomplane-"+digest[:16])
			if err != nil {
				return nil, err
			}
			state.TokenID = credentials.TokenID
			if saveErr := saveVercelResource(job, state); saveErr != nil {
				return nil, saveErr
			}
		}
	} else {
		connection, e := vercelBridgeConnection(resource, state.Receipt.BridgeProjectID)
		if e != nil {
			return nil, e
		}
		if connection == nil {
			if connectErr := v.Client.ConnectStorageResource(ctx, tok, resource.ID, vercel.ConnectResourceOptions{ProjectID: state.Receipt.BridgeProjectID, Environments: []string{"production"}}); connectErr != nil {
				return nil, connectErr
			}
			resource, err = v.readResource(ctx, tok, state.Receipt)
			if err != nil {
				return nil, err
			}
			connection, e = vercelBridgeConnection(resource, state.Receipt.BridgeProjectID)
			if e != nil {
				return nil, e
			}
		}
		if connection == nil || connection.ID == "" {
			return nil, errors.New("vercel did not confirm the created resource connection")
		}
		if state.Receipt.BridgeConnectionID != "" && state.Receipt.BridgeConnectionID != connection.ID {
			return nil, errors.New("the Vercel resource credential bridge connection changed")
		}
		state.Receipt.BridgeConnectionID = connection.ID
		if saveErr := saveVercelResource(job, state); saveErr != nil {
			return nil, saveErr
		}
		credentials, err = v.Client.ReadStorageResourceCredentials(ctx, tok, resource.ID, state.Receipt.BridgeProjectID, "production")
		if err != nil {
			return nil, err
		}
	}
	if cfg.Kind == "vercel-integration" {
		keys, e := vercelIntegrationOutputKeys(cfg)
		if e != nil {
			return nil, e
		}
		for _, key := range keys {
			if credentials.Values[key] == "" {
				return nil, errors.New("the Vercel integration did not provide every reviewed application output")
			}
		}
	}
	_, err = storeVercelOutputs(ctx, job, credentials.Values, account, resource.ID)
	if err != nil {
		return nil, err
	}
	if saveErr := saveVercelResource(job, state); saveErr != nil {
		return nil, saveErr
	}
	ownership, err := json.Marshal(map[string]any{vercelResourceOwnershipKey: state.Receipt})
	if err != nil {
		return nil, err
	}
	return &Outcome{ExternalID: resource.ID, PhysicalID: resource.ID, OwnedResources: string(ownership), Revision: resource.ID, URL: ""}, nil
}

func validVercelConfigAccess(raw, id string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && (u.Host == "global-config.vercel.com" || u.Host == "edge-config.vercel.com") && u.Path == "/"+id && u.Query().Get("token") != ""
}

func verifyVercelReceipt(job *Job, cfg *planning.ResourceConfig, r *vercelResourceReceipt, account, digest string) error {
	if r == nil || r.ResourceID == "" || r.AccountID != account || r.AccountIdentity != cfg.AccountIdentity || r.ConnectionID != job.Connection.ID || r.OrgID != job.Deployment.OrgID || r.ProjectScopeID != job.Deployment.ProjectID || r.EnvironmentID != job.Deployment.EnvironmentID || r.Kind != cfg.Kind || r.NativeType != vercelResourceType(cfg.Kind) || r.ConfigDigest != digest || r.BridgeProjectID != cfg.Settings["project_id"] {
		return errors.New("the original Vercel resource ownership does not match this account and deployment scope")
	}
	return nil
}

func (v *Vercel) readResource(ctx context.Context, tok vercel.Token, r *vercelResourceReceipt) (vercel.Resource, error) {
	var resource vercel.Resource
	var err error
	if r.NativeType == "global-config" {
		resource, err = v.Client.GetGlobalConfig(ctx, tok, r.ResourceID)
	} else {
		resource, err = v.Client.GetStorageResource(ctx, tok, r.ResourceID)
	}
	if err != nil {
		return vercel.Resource{}, err
	}
	if resource.ID != r.ResourceID || resource.Type != r.NativeType || resource.OwnerID != "" && resource.OwnerID != r.AccountID || r.ExternalResourceID != "" && resource.ExternalResourceID != r.ExternalResourceID || r.IntegrationProductID != "" && resource.Product.ID != r.IntegrationProductID || r.IntegrationConfigurationID != "" && resource.Product.IntegrationConfigurationID != r.IntegrationConfigurationID {
		return vercel.Resource{}, errors.New("the current Vercel resource no longer matches its owned identity")
	}
	return resource, nil
}

func vercelBridgeConnection(resource vercel.Resource, projectID string) (*vercel.ResourceConnection, error) {
	var found *vercel.ResourceConnection
	for i := range resource.ProjectsMetadata {
		c := &resource.ProjectsMetadata[i]
		if c.ProjectID != projectID || !slices.Contains(c.Environments, "production") {
			continue
		}
		if found != nil {
			return nil, errors.New("the Vercel resource has ambiguous credential bridge connections")
		}
		found = c
	}
	return found, nil
}

var vercelOutputName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

func vercelIntegrationOutputKeys(cfg *planning.ResourceConfig) ([]string, error) {
	raw := cfg.Settings["output_keys"]
	if raw == "" {
		raw = "DATABASE_URL"
	}
	keys := strings.Split(raw, ",")
	seen := map[string]bool{}
	for i, key := range keys {
		key = strings.TrimSpace(key)
		if !vercelOutputName.MatchString(key) || seen[key] {
			return nil, errors.New("vercel integration output_keys must name distinct application environment variables")
		}
		seen[key] = true
		keys[i] = key
	}
	return keys, nil
}

func storeVercelOutputs(ctx context.Context, job *Job, values map[string]string, account, id string) (string, error) {
	if len(values) == 0 {
		return "", errors.New("vercel did not return scoped application outputs")
	}
	for key, value := range values {
		if !vercelOutputName.MatchString(key) || value == "" || value == string(job.Credential) {
			return "", errors.New("vercel returned invalid application output names or credentials")
		}
	}
	for _, key := range slices.Sorted(maps.Keys(values)) {
		if keepErr := job.Keep(ctx, "output:"+key, []byte(values[key])); keepErr != nil {
			return "", &nativeDeliveryError{message: "Vercel application credentials could not be stored", cause: keepErr}
		}
	}
	address := ""
	for _, key := range []string{"GLOBAL_CONFIG", "BLOB_READ_WRITE_TOKEN", "DATABASE_URL", "POSTGRES_URL", "REDIS_URL", "KV_URL"} {
		if values[key] != "" {
			address = values[key]
			break
		}
	}
	if address == "" {
		environment := job.Action.Resource.Settings["environment"]
		if environment == "" {
			environment = "production"
		}
		raw, err := catalogconnect.EncodeAPIAccess(catalogconnect.APIAccess{Provider: "vercel", Kind: job.Action.Resource.Kind, AccountID: account, ResourceID: id, ProjectID: job.Action.Resource.Settings["project_id"], Environment: environment, Values: values})
		if err != nil {
			return "", errors.New("vercel application outputs could not be encoded")
		}
		address = raw
	}
	if keepErr := job.Keep(ctx, ResourceURL, []byte(address)); keepErr != nil {
		return "", &nativeDeliveryError{message: "Vercel application access could not be stored", cause: keepErr}
	}
	return address, nil
}

func vercelOwnedResource(b *planning.Binding) (*vercelResourceReceipt, error) {
	var cfg planning.ResourceConfig
	var receipt vercelResourceReceipt
	if b == nil || b.Provider != "vercel" || b.Subject != planning.ResourceSubject || b.ExternalID == "" || b.PhysicalID != b.ExternalID || json.Unmarshal(b.Config, &cfg) != nil || cfg.ExistingResource != nil || json.Unmarshal(b.OwnedResources[vercelResourceOwnershipKey], &receipt) != nil || receipt.ResourceID != b.ExternalID || receipt.ConnectionID != b.ConnectionID || receipt.ConfigDigest != vercelResourceDigest(&cfg, b.Name) {
		return nil, errors.New("vercel resource removal requires its original managed ownership receipt")
	}
	return &receipt, nil
}

func (v *Vercel) removeResource(ctx context.Context, job *Job, b *planning.Binding) (err error) {
	defer nativeDeliveryBoundary(job)(&err)
	receipt, err := vercelOwnedResource(b)
	if err != nil {
		return err
	}
	var cfg planning.ResourceConfig
	if decodeErr := json.Unmarshal(b.Config, &cfg); decodeErr != nil {
		return decodeErr
	}
	tok, account, err := v.resourceAccount(ctx, job, &cfg)
	if err != nil {
		return err
	}
	if checkErr := verifyVercelReceipt(job, &cfg, receipt, account, vercelResourceDigest(&cfg, b.Name)); checkErr != nil {
		return checkErr
	}
	resource, err := v.readResource(ctx, tok, receipt)
	if vercel.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if receipt.NativeType != "global-config" {
		for _, connection := range resource.ProjectsMetadata {
			if connection.ID == "" || connection.ID != receipt.BridgeConnectionID || connection.ProjectID != receipt.BridgeProjectID || len(connection.Environments) != 1 || connection.Environments[0] != "production" {
				return errors.New("the Vercel resource has project connections outside its owned credential bridge")
			}
			if disconnectErr := v.Client.DisconnectStorageResource(ctx, tok, receipt.ResourceID, connection.ID); disconnectErr != nil {
				return disconnectErr
			}
		}
	}
	switch receipt.NativeType {
	case "blob":
		err = v.Client.DeleteBlobStore(ctx, tok, receipt.ResourceID)
	case "global-config":
		err = v.Client.DeleteGlobalConfig(ctx, tok, receipt.ResourceID)
	case "integration":
		err = v.Client.DeleteIntegrationStore(ctx, tok, receipt.ResourceID)
	default:
		return errors.New("unsupported owned Vercel resource type")
	}
	if vercel.IsNotFound(err) {
		return nil
	}
	return err
}

func (v *Vercel) observeResource(ctx context.Context, conn *connections.Connection, credential []byte, b *planning.Binding) (*Observation, error) {
	receipt, err := vercelOwnedResource(b)
	if err != nil {
		return nil, err
	}
	var cfg planning.ResourceConfig
	if decodeErr := json.Unmarshal(b.Config, &cfg); decodeErr != nil {
		return nil, decodeErr
	}
	job := &Job{Connection: conn, Credential: credential, Deployment: &Deployment{OrgID: b.OrgID, ProjectID: b.ProjectID, EnvironmentID: b.EnvironmentID}, Action: planning.Action{ConnectionID: b.ConnectionID, Name: b.Name, Resource: &cfg}}
	tok, account, err := v.resourceAccount(ctx, job, &cfg)
	if err != nil {
		return nil, err
	}
	if checkErr := verifyVercelReceipt(job, &cfg, receipt, account, vercelResourceDigest(&cfg, b.Name)); checkErr != nil {
		return nil, checkErr
	}
	_, err = v.readResource(ctx, tok, receipt)
	if vercel.IsNotFound(err) {
		return &Observation{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &Observation{Exists: true, Revision: b.ExternalID}, nil
}

func vercelDeploymentEnvironments(job *Job) (runtime, build map[string]string, secrets map[string]bool) {
	runtime = map[string]string{}
	build = map[string]string{}
	maps.Copy(runtime, job.Env)
	maps.Copy(build, job.Env)
	secrets = maps.Clone(job.Secrets)
	if job.Action.Service != nil {
		for _, link := range job.Action.Service.Env {
			if link.Subject == planning.ResourceSubject {
				delete(build, link.Key)
				delete(secrets, link.Key)
			}
		}
	}
	return runtime, build, secrets
}

func vercelCredentialBridgeCollision(job *Job, projectID string) error {
	linked := map[string]bool{}
	if job.Action.Service != nil {
		for _, link := range job.Action.Service.Env {
			if link.Subject == planning.ResourceSubject {
				linked[link.From] = true
			}
		}
	}
	check := func(cfg *planning.ResourceConfig, provider string) bool {
		return cfg != nil && (cfg.Provider == "vercel" || provider == "vercel" || vercelResourceType(cfg.Kind) != "") && cfg.Settings["project_id"] == projectID
	}
	refusal := errors.New("use a separate Vercel credential bridge project; this application project would expose resource credentials to its builds")
	for _, binding := range job.Attach {
		if binding == nil || !linked[binding.Name] {
			continue
		}
		var cfg planning.ResourceConfig
		if json.Unmarshal(binding.Config, &cfg) == nil && check(&cfg, binding.Provider) {
			return refusal
		}
	}
	if job.Plan != nil {
		for _, action := range job.Plan.Actions {
			if linked[action.Name] && check(action.Resource, action.Provider) {
				return refusal
			}
		}
	}
	return nil
}

// ResolvePendingCloudResource recovers resource-only ownership from the durable
// create receipt. A service project checkpoint cannot prove native ownership.
func (v *Vercel) ResolvePendingCloudResource(ctx context.Context, job *Job) (identity *PendingCloudIdentity, err error) {
	defer nativeDeliveryBoundary(job)(&err)
	if job.Action.Resource == nil {
		return nil, errors.New("a Vercel service checkpoint does not establish resource ownership")
	}
	cfg := job.Action.Resource
	tok, account, err := v.resourceAccount(ctx, job, cfg)
	if err != nil {
		return nil, err
	}
	var state vercelResourceState
	if len(job.Resume) == 0 {
		return &PendingCloudIdentity{Proof: json.RawMessage(`{}`)}, nil
	}
	if json.Unmarshal(job.Resume, &state) != nil || state.AccountID != account || state.ConfigDigest != vercelResourceDigest(cfg, job.Action.Name) {
		return nil, errors.New("vercel pending resource scope changed")
	}
	if state.Receipt == nil {
		if state.Started {
			return nil, errors.New("the Vercel create result is uncertain; sync its native identity before cleanup")
		}
		return &PendingCloudIdentity{Proof: json.RawMessage(`{}`)}, nil
	}
	if checkErr := verifyVercelReceipt(job, cfg, state.Receipt, account, state.ConfigDigest); checkErr != nil {
		return nil, checkErr
	}
	resource, err := v.readResource(ctx, tok, state.Receipt)
	if err != nil && !vercel.IsNotFound(err) {
		return nil, err
	}
	if err == nil && state.Receipt.NativeType != "global-config" && state.Receipt.BridgeConnectionID == "" {
		connection, e := vercelBridgeConnection(resource, state.Receipt.BridgeProjectID)
		if e != nil {
			return nil, e
		}
		if connection != nil {
			state.Receipt.BridgeConnectionID = connection.ID
		}
	}
	proof, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	receipt, err := json.Marshal(state.Receipt)
	if err != nil {
		return nil, err
	}
	return &PendingCloudIdentity{ExternalID: state.Receipt.ResourceID, PhysicalID: state.Receipt.ResourceID, OwnedResources: map[string]json.RawMessage{vercelResourceOwnershipKey: receipt}, Proof: proof}, nil
}

// RemovePendingCloudResource removes only the native resource pinned by the
// saved create receipt and the runner's retained pending ownership evidence.
func (v *Vercel) RemovePendingCloudResource(ctx context.Context, job *Job, identity *PendingCloudIdentity) (err error) {
	defer nativeDeliveryBoundary(job)(&err)
	if job.Action.Resource == nil || identity == nil || job.Connection == nil || job.Deployment == nil {
		return errors.New("vercel pending cleanup requires a resource ownership receipt")
	}
	if identity.ExternalID == "" && identity.PhysicalID == "" && string(identity.Proof) == "{}" {
		return nil
	}
	var state vercelResourceState
	var receipt vercelResourceReceipt
	if json.Unmarshal(identity.Proof, &state) != nil || state.Receipt == nil || identity.ExternalID != identity.PhysicalID || identity.ExternalID != state.Receipt.ResourceID || json.Unmarshal(identity.OwnedResources[vercelResourceOwnershipKey], &receipt) != nil || !reflect.DeepEqual(*state.Receipt, receipt) {
		return errors.New("vercel pending cleanup ownership changed")
	}
	raw, err := json.Marshal(job.Action.Resource)
	if err != nil {
		return err
	}
	b := &planning.Binding{OrgID: job.Deployment.OrgID, ProjectID: job.Deployment.ProjectID, EnvironmentID: job.Deployment.EnvironmentID, Subject: planning.ResourceSubject, Name: job.Action.Name, Provider: "vercel", ConnectionID: job.Connection.ID, ExternalID: identity.ExternalID, PhysicalID: identity.PhysicalID, OwnedResources: identity.OwnedResources, Config: raw}
	return v.removeResource(ctx, job, b)
}
