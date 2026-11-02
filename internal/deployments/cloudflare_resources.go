package deployments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// CloudflareBindingOutput is the Vault field used for reviewed native Worker bindings.
const CloudflareBindingOutput = "output:CLOUDFLARE_BINDING"

type cloudflareResourceState struct {
	Account       string                  `json:"account"`
	Kind          cloudflare.ResourceKind `json:"kind"`
	Name          string                  `json:"name"`
	ID            string                  `json:"id,omitempty"`
	PhysicalID    string                  `json:"physical_id,omitempty"`
	Jurisdiction  string                  `json:"jurisdiction,omitempty"`
	Configuration string                  `json:"configuration"`
	Pending       bool                    `json:"pending,omitempty"`
}

type cloudflareNativeBinding struct {
	Type         string `json:"type"`
	Kind         string `json:"kind"`
	AccountID    string `json:"account_id"`
	ResourceID   string `json:"resource_id"`
	Incarnation  string `json:"incarnation"`
	BucketName   string `json:"bucket_name,omitempty"`
	QueueName    string `json:"queue_name,omitempty"`
	IndexName    string `json:"index_name,omitempty"`
	Jurisdiction string `json:"jurisdiction,omitempty"`
	ClassName    string `json:"class_name,omitempty"`
	ScriptName   string `json:"script_name,omitempty"`
}

func cloudflareResourceKind(kind string) (cloudflare.ResourceKind, error) {
	switch kind {
	case "cloudflare-d1":
		return cloudflare.ResourceD1, nil
	case "cloudflare-kv":
		return cloudflare.ResourceKV, nil
	case "object-storage", "cloudflare-r2":
		return cloudflare.ResourceR2, nil
	case "cloudflare-queue":
		return cloudflare.ResourceQueue, nil
	case "cloudflare-vectorize":
		return cloudflare.ResourceVectorize, nil
	case "cloudflare-hyperdrive":
		return cloudflare.ResourceHyperdrive, nil
	case "cloudflare-durable-object":
		return cloudflare.ResourceDurableObject, nil
	}
	return "", errors.New("this Cloudflare resource kind is not supported")
}

func cloudflareResourcePhysical(resource cloudflare.Resource) string {
	if resource.Kind == cloudflare.ResourceR2 || resource.Kind == cloudflare.ResourceVectorize {
		return cloudflarePhysical(resource.ID, resource.CreatedAt)
	}
	return resource.ID
}

func cloudflareResourceJurisdiction(kind cloudflare.ResourceKind, settings map[string]string) string {
	if kind == cloudflare.ResourceR2 {
		if value := settings["jurisdiction"]; value != "" {
			return value
		}
		return "default"
	}
	return ""
}

func (c *Cloudflare) applyCloudflareResource(ctx context.Context, job *Job) (*Outcome, error) {
	cfg := job.Action.Resource
	if cfg == nil || job.Deployment == nil || job.Keep == nil || c.Client == nil {
		return nil, errors.New("Cloudflare resource provisioning requires its reviewed settings and Vault outputs")
	}
	account, err := cloudflareAccount(job.Connection)
	if err != nil {
		return nil, err
	}
	kind, err := cloudflareResourceKind(cfg.Kind)
	if err != nil {
		return nil, err
	}
	if kind == cloudflare.ResourceDurableObject {
		return nil, cloudflare.ErrResourceOperationUnsupported
	}
	name := CloudflareWorkerName(job)
	settingsJSON, marshalErr := json.Marshal(struct {
		Settings map[string]string
		Pins     map[string]planning.EndpointPin
	}{cfg.Settings, cfg.EndpointPins})
	if marshalErr != nil {
		return nil, marshalErr
	}
	settingsSum := sha256.Sum256(settingsJSON)
	configuration := hex.EncodeToString(settingsSum[:])
	state := cloudflareResourceState{Account: account, Kind: kind, Name: name, Jurisdiction: cloudflareResourceJurisdiction(kind, cfg.Settings), Configuration: configuration}
	if len(job.Resume) > 0 {
		var old cloudflareResourceState
		if json.Unmarshal(job.Resume, &old) != nil || old.Account != state.Account || old.Kind != state.Kind || old.Name != state.Name || old.Jurisdiction != state.Jurisdiction || old.Configuration != state.Configuration {
			return nil, errors.New("Cloudflare resource checkpoint no longer matches its reviewed scope")
		}
		state = old
	}
	if job.Binding != nil {
		var previous planning.ResourceConfig
		if json.Unmarshal(job.Binding.Config, &previous) != nil || previous.Kind != cfg.Kind {
			return nil, errors.New("Cloudflare resource binding configuration changed")
		}
		oldSettings, newSettings := map[string]string{}, map[string]string{}
		for key, value := range previous.Settings {
			if key != "application_endpoint" {
				oldSettings[key] = value
			}
		}
		for key, value := range cfg.Settings {
			if key != "application_endpoint" {
				newSettings[key] = value
			}
		}
		if !reflect.DeepEqual(oldSettings, newSettings) || previous.EndpointPins["origin_endpoint"] != cfg.EndpointPins["origin_endpoint"] {
			return nil, errors.New("Cloudflare native provisioning settings are immutable; review a resource replacement")
		}
		if state.ID != "" && state.ID != job.Binding.ExternalID {
			return nil, errors.New("Cloudflare resource checkpoint and binding disagree")
		}
		state.ID, state.PhysicalID = job.Binding.ExternalID, job.Binding.PhysicalID
	}
	var resource cloudflare.Resource
	if state.ID != "" {
		resource, err = c.Client.GetResourceInJurisdiction(ctx, string(job.Credential), account, kind, state.ID, state.Jurisdiction)
		if err != nil {
			return nil, err
		}
		if state.PhysicalID == "" || cloudflareResourcePhysical(resource) != state.PhysicalID {
			return nil, errors.New("Cloudflare resource incarnation changed; provisioning stopped")
		}
	} else {
		if state.Pending {
			return nil, errors.New("Cloudflare resource creation has an unresolved result; recover its native identity before retrying")
		}
		options := cloudflare.CreateResourceOptions{Kind: kind, Name: name, Location: cfg.Settings["location"], Jurisdiction: cfg.Settings["jurisdiction"], Metric: cfg.Settings["metric"], CACertificateID: cfg.Settings["ca_certificate_id"]}
		if raw := cfg.Settings["dimensions"]; raw != "" {
			options.Dimensions, err = strconv.Atoi(raw)
			if err != nil {
				return nil, errors.New("vector dimensions must be a whole number")
			}
		}
		if kind == cloudflare.ResourceHyperdrive {
			options.Origin, err = c.cloudflareHyperdriveOrigin(ctx, job, cfg.Settings["origin_endpoint"])
			if err != nil {
				return nil, err
			}
			options.SSLMode = "verify-full"
			if options.Origin.Scheme == "mysql" {
				options.SSLMode = "VERIFY_IDENTITY"
			}
		}
		if cfg.Settings["application_endpoint"] != "" {
			if _, endpointErr := c.cloudflareApplicationEndpoint(ctx, job, cfg.Settings["application_endpoint"]); endpointErr != nil {
				return nil, endpointErr
			}
		}
		inventory, listErr := c.Client.ListResourcesInJurisdiction(ctx, string(job.Credential), account, kind, state.Jurisdiction)
		if listErr != nil {
			return nil, listErr
		}
		for _, existing := range inventory {
			if existing.Name == name {
				return nil, errors.New("the Cloudflare resource name already exists; sync and attach it explicitly")
			}
		}
		state.Pending = true
		if saveErr := cloudflareSave(job, state); saveErr != nil {
			return nil, saveErr
		}
		if kind == cloudflare.ResourceHyperdrive {
			options.Origin, err = c.cloudflareHyperdriveOrigin(ctx, job, cfg.Settings["origin_endpoint"])
		}
		if err == nil && cfg.Settings["application_endpoint"] != "" {
			_, err = c.cloudflareApplicationEndpoint(ctx, job, cfg.Settings["application_endpoint"])
		}
		if err != nil {
			state.Pending = false
			if saveErr := cloudflareSave(job, state); saveErr != nil {
				return nil, saveErr
			}
			return nil, err
		}
		resource, err = c.Client.CreateResource(ctx, string(job.Credential), account, options)
		if err != nil {
			if errors.Is(err, cloudflare.ErrInvalidResource) || errors.Is(err, cloudflare.ErrInvalidToken) || errors.Is(err, cloudflare.ErrRejected) {
				state.Pending = false
				if saveErr := cloudflareSave(job, state); saveErr != nil {
					return nil, saveErr
				}
			}
			return nil, err
		}
		state.ID, state.PhysicalID, state.Pending = resource.ID, cloudflareResourcePhysical(resource), false
		if saveErr := cloudflareSave(job, state); saveErr != nil {
			return nil, saveErr
		}
		if state.PhysicalID == "" {
			return nil, errors.New("Cloudflare did not return the resource's immutable creation identity")
		}
	}
	descriptor := cloudflareResourceDescriptor(account, cfg.Kind, resource)
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		return nil, err
	}
	if keepErr := job.Keep(ctx, CloudflareBindingOutput, encoded); keepErr != nil {
		return nil, keepErr
	}
	applicationDSN := ""
	if endpoint := cfg.Settings["application_endpoint"]; endpoint != "" {
		applicationDSN, err = c.cloudflareApplicationEndpoint(ctx, job, endpoint)
		if err != nil {
			return nil, err
		}
	}
	if applicationDSN != "" {
		if outputErr := c.cloudflareApplicationOutputs(ctx, job, account, resource, applicationDSN); outputErr != nil {
			return nil, outputErr
		}
	}
	if keepErr := job.Keep(ctx, ResourceURL, []byte(applicationDSN)); keepErr != nil {
		return nil, keepErr
	}
	owned, marshalErr := cloudflareResourceOwnedJSON(job, resource, account)
	if marshalErr != nil {
		return nil, marshalErr
	}
	return &Outcome{ExternalID: resource.ID, PhysicalID: state.PhysicalID, Revision: state.PhysicalID, OwnedResources: owned}, nil
}

func cloudflareResourceDescriptor(account, kind string, resource cloudflare.Resource) cloudflareNativeBinding {
	descriptor := cloudflareNativeBinding{Kind: kind, AccountID: account, ResourceID: resource.ID, Incarnation: resource.ID}
	if resource.Kind == cloudflare.ResourceR2 || resource.Kind == cloudflare.ResourceVectorize {
		descriptor.Incarnation = resource.CreatedAt
	}
	switch resource.Kind {
	case cloudflare.ResourceD1:
		descriptor.Type = "d1"
	case cloudflare.ResourceKV:
		descriptor.Type = "kv_namespace"
	case cloudflare.ResourceR2:
		descriptor.Type = "r2_bucket"
		descriptor.BucketName = resource.Name
		descriptor.Jurisdiction = resource.Metadata["jurisdiction"]
		descriptor.ResourceID = "r2/" + descriptor.Jurisdiction + "/" + resource.Name
	case cloudflare.ResourceQueue:
		descriptor.Type = "queue"
		descriptor.QueueName = resource.Name
	case cloudflare.ResourceVectorize:
		descriptor.Type = "vectorize"
		descriptor.IndexName = resource.Name
	case cloudflare.ResourceHyperdrive:
		descriptor.Type = "hyperdrive"
	case cloudflare.ResourceDurableObject:
		descriptor.Type = "durable_object_namespace"
		descriptor.ClassName = resource.Metadata["class"]
		descriptor.ScriptName = resource.Metadata["script"]
	}
	return descriptor
}

func (c *Cloudflare) cloudflareResolveEndpoint(ctx context.Context, job *Job, setting, id string) (*catalogconnect.Endpoint, string, error) {
	if c.Endpoints == nil || c.Authorizer == nil || job.Plan == nil || job.Deployment == nil || job.Action.Resource == nil || job.Plan.OrgID != job.Deployment.OrgID {
		return nil, "", errors.New("Cloudflare application access requires its reviewed endpoint and current authorization")
	}
	pin, ok := job.Action.Resource.EndpointPins[setting]
	if !ok || pin.ID != id || id == "" || job.Action.Resource.Settings[setting] != id || pin.Revision <= 0 || pin.Engine == "" || pin.Host == "" {
		return nil, "", errors.New("the Cloudflare endpoint reference is missing its reviewed revision")
	}
	actor := job.Plan.DecidedBy
	if actor == "" {
		actor = job.Plan.CreatedBy
	}
	if actor == "" {
		return nil, "", errors.New("the Cloudflare endpoint reference needs its approving actor")
	}
	principal := security.ActorPrincipal(job.Deployment.OrgID, actor)
	if c.Authorizer.RequireCurrentMembership(ctx, principal) != nil || c.Authorizer.Require(ctx, principal, security.SecretManage, "") != nil {
		return nil, "", errors.New("the approving actor no longer has access to application endpoint credentials")
	}
	metadata, metadataErr := c.Endpoints.Get(ctx, job.Deployment.OrgID, id)
	if metadataErr != nil || metadata == nil || metadata.OrgID != job.Deployment.OrgID || metadata.ID != pin.ID || metadata.Revision != pin.Revision || metadata.Engine != pin.Engine || metadata.Host != pin.Host || metadata.Database != pin.Database || metadata.TLS != catalogconnect.TLSVerifyFull {
		return nil, "", errors.New("the Cloudflare endpoint metadata or credential revision changed; review the deployment again")
	}
	endpoint, dsn, err := c.Endpoints.Resolve(ctx, job.Deployment.OrgID, id)
	if err != nil || endpoint == nil || endpoint.OrgID != job.Deployment.OrgID || endpoint.ID != pin.ID || endpoint.Revision != pin.Revision || endpoint.Engine != pin.Engine || endpoint.Host != pin.Host || endpoint.Database != pin.Database || dsn == "" {
		return nil, "", errors.New("the Cloudflare endpoint metadata or credential revision changed; review the deployment again")
	}
	return endpoint, dsn, nil
}

func (c *Cloudflare) cloudflareApplicationEndpoint(ctx context.Context, job *Job, id string) (string, error) {
	endpoint, dsn, err := c.cloudflareResolveEndpoint(ctx, job, "application_endpoint", id)
	if err != nil {
		return "", err
	}
	kind := job.Action.Resource.Kind
	if kind == "object-storage" || kind == "cloudflare-r2" {
		if endpoint.Engine != catalogconnect.EngineR2 {
			return "", errors.New("the R2 application endpoint must contain matching S3 application keys")
		}
	} else if endpoint.Engine != kind {
		return "", errors.New("the Cloudflare application endpoint engine does not match its resource")
	}
	if endpoint.TLS != catalogconnect.TLSVerifyFull {
		return "", errors.New("Cloudflare application endpoints must verify TLS identity")
	}
	return dsn, nil
}

func (c *Cloudflare) cloudflareHyperdriveOrigin(ctx context.Context, job *Job, id string) (*cloudflare.HyperdriveOrigin, error) {
	endpoint, dsn, err := c.cloudflareResolveEndpoint(ctx, job, "origin_endpoint", id)
	if err != nil {
		return nil, err
	}
	if (endpoint.Engine != catalogconnect.EnginePostgres && endpoint.Engine != catalogconnect.EngineMySQL) || endpoint.TLS != catalogconnect.TLSVerifyFull {
		return nil, errors.New("the Hyperdrive origin must be a PostgreSQL or MySQL endpoint with verified TLS identity")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.User == nil || parsed.Hostname() != endpoint.Host || strings.TrimPrefix(parsed.Path, "/") != endpoint.Database || !oneCloudflareScheme(parsed.Scheme) || parsed.Query().Get("sslmode") != "verify-full" || (endpoint.Engine == catalogconnect.EngineMySQL) != (parsed.Scheme == "mysql") {
		return nil, errors.New("the Hyperdrive origin connection does not match its verified endpoint")
	}
	password, present := parsed.User.Password()
	if !present || password == "" {
		return nil, errors.New("the Hyperdrive origin needs application database credentials")
	}
	port := 5432
	if parsed.Scheme == "mysql" {
		port = 3306
	}
	if raw := parsed.Port(); raw != "" {
		port, err = strconv.Atoi(raw)
		if err != nil {
			return nil, errors.New("the Hyperdrive origin endpoint has an invalid port")
		}
	}
	if endpoint.Port != port || (endpoint.Username != "" && endpoint.Username != parsed.User.Username()) {
		return nil, errors.New("the Hyperdrive origin address or database user changed")
	}
	return &cloudflare.HyperdriveOrigin{Scheme: parsed.Scheme, Host: parsed.Hostname(), Port: port, Database: endpoint.Database, User: parsed.User.Username(), Password: password}, nil
}
func oneCloudflareScheme(scheme string) bool {
	return scheme == "postgres" || scheme == "postgresql" || scheme == "mysql"
}

func (c *Cloudflare) removeCloudflareResource(ctx context.Context, job *Job, b *planning.Binding) error {
	account, err := cloudflareAccount(job.Connection)
	if err != nil {
		return err
	}
	var cfg planning.ResourceConfig
	if json.Unmarshal(b.Config, &cfg) != nil {
		return errors.New("Cloudflare resource binding settings are invalid")
	}
	kind, err := cloudflareResourceKind(cfg.Kind)
	if err != nil {
		return err
	}
	jurisdiction := cloudflareResourceJurisdiction(kind, cfg.Settings)
	resource, err := c.Client.GetResourceInJurisdiction(ctx, string(job.Credential), account, kind, b.ExternalID, jurisdiction)
	if cloudflareNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if b.PhysicalID == "" || cloudflareResourcePhysical(resource) != b.PhysicalID {
		return errors.New("Cloudflare resource incarnation changed; removal stopped")
	}
	if err = c.Client.DeleteResourceInJurisdiction(ctx, string(job.Credential), account, kind, b.ExternalID, jurisdiction); err != nil && !cloudflareNotFound(err) {
		return err
	}
	if job.Forget != nil {
		return job.Forget(ctx)
	}
	return nil
}

func (c *Cloudflare) observeCloudflareResource(ctx context.Context, conn *connections.Connection, credential []byte, b *planning.Binding) (*Observation, error) {
	account, err := cloudflareAccount(conn)
	if err != nil {
		return nil, err
	}
	var cfg planning.ResourceConfig
	if json.Unmarshal(b.Config, &cfg) != nil {
		return nil, errors.New("Cloudflare resource binding settings are invalid")
	}
	kind, err := cloudflareResourceKind(cfg.Kind)
	if err != nil {
		return nil, err
	}
	resource, err := c.Client.GetResourceInJurisdiction(ctx, string(credential), account, kind, b.ExternalID, cloudflareResourceJurisdiction(kind, cfg.Settings))
	if cloudflareNotFound(err) {
		return &Observation{}, nil
	}
	if err != nil {
		return nil, err
	}
	physical := cloudflareResourcePhysical(resource)
	if b.PhysicalID == "" || physical != b.PhysicalID {
		return nil, errors.New("Cloudflare resource incarnation changed")
	}
	return &Observation{Exists: true, Revision: physical}, nil
}

func (c *Cloudflare) cloudflareApplicationOutputs(ctx context.Context, job *Job, account string, resource cloudflare.Resource, raw string) error {
	var outputs map[string]string
	var err error
	if resource.Kind == cloudflare.ResourceR2 {
		access, parseErr := catalogconnect.ParseBucketAccess(raw)
		jurisdiction := access.Jurisdiction
		if jurisdiction == "" {
			jurisdiction = "default"
		}
		if parseErr != nil || access.Provider != "cloudflare" || access.AccountID != account || access.Bucket != resource.Name || jurisdiction != resource.Metadata["jurisdiction"] {
			return errors.New("the R2 application credentials do not match the created bucket")
		}
		outputs, err = catalogconnect.BucketOutputs(raw)
	} else {
		access, parseErr := catalogconnect.ParseAPIAccess(raw)
		expected := cloudflareResourceDescriptor(account, job.Action.Resource.Kind, resource)
		if parseErr != nil || access.Native || access.Provider != "cloudflare" || access.Kind != job.Action.Resource.Kind || access.AccountID != account || access.ResourceID != resource.ID || access.Incarnation != "" && access.Incarnation != expected.Incarnation || access.Token == strings.TrimSpace(string(job.Credential)) {
			return errors.New("the application API credentials do not match the created Cloudflare resource")
		}
		outputs, err = catalogconnect.APIOutputs(raw)
	}
	if err != nil {
		return errors.New("the Cloudflare application access material is invalid")
	}
	for key, value := range outputs {
		if key == "CLOUDFLARE_BINDING" {
			continue
		}
		if keepErr := job.Keep(ctx, "output:"+key, []byte(value)); keepErr != nil {
			return keepErr
		}
	}
	return nil
}
