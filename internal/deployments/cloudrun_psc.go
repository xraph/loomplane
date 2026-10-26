package deployments

import (
	"encoding/json"
	"errors"
	"maps"
	"net/url"
	"reflect"
	"slices"

	"github.com/xraph/loomplane/internal/clouds/cloudrun"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/provider"
)

var errCloudRunPSCReview = errors.New("cloud sql PSC application transport requires matching reviewed sources, private routes and secret credentials")

type cloudRunPSCSource struct {
	Binding planning.Binding
	Config  planning.ResourceConfig
}

type cloudRunPSC struct {
	transport cloudrun.PSCTransport
	sources   []cloudRunPSCSource
	env       map[string]string
}

func cloudRunPSCBinding(binding *planning.Binding) (*planning.ResourceConfig, bool, error) {
	if binding == nil {
		return nil, false, errCloudRunPSCReview
	}
	if binding.Provider != "cloud-sql" || len(binding.Config) == 0 {
		return nil, false, nil
	}
	var cfg planning.ResourceConfig
	if err := json.Unmarshal(binding.Config, &cfg); err != nil {
		return nil, false, errCloudRunPSCReview
	}
	psc := cfg.Managed != nil && (cfg.Managed.CloudSQLPSC != (provider.CloudSQLSourceEndpoint{}) || cfg.Managed.Profile.Access == "cloud-sql-psc-connector")
	return &cfg, psc, nil
}

// prepareCloudRunPSC resolves only links pinned by the service review. It copies
// credentials in memory; neither the plan nor the binding stores rewritten URLs.
func prepareCloudRunPSC(job *Job) (*cloudRunPSC, error) {
	cfg := job.Action.Service
	prepared := &cloudRunPSC{env: maps.Clone(job.Env)}
	byName := map[string]cloudRunPSCSource{}
	for _, binding := range job.Attach {
		resource, psc, err := cloudRunPSCBinding(binding)
		if err != nil {
			return nil, err
		}
		if !psc {
			continue
		}
		if job.Deployment == nil || job.Plan == nil || job.Connection == nil || cfg == nil || cfg.CloudSQLNetwork == nil {
			return nil, errCloudRunPSCReview
		}
		if job.Deployment.ID == "" || job.Plan.ID == "" || job.Plan.Revision == "" || job.Deployment.PlanID != job.Plan.ID {
			return nil, errCloudRunPSCReview
		}
		selected := job.Action.ConnectionID
		if selected == "" {
			selected = job.Plan.Connections[connections.GCP]
		}
		if selected != job.Connection.ID {
			return nil, errCloudRunPSCReview
		}

		if binding.ID == "" || binding.PhysicalID == "" || binding.OrgID != job.Deployment.OrgID || binding.ProjectID != job.Deployment.ProjectID || binding.EnvironmentID != job.Deployment.EnvironmentID || binding.Subject != planning.ResourceSubject || binding.Region != job.Action.Region {
			return nil, errCloudRunPSCReview
		}
		if old, ok := byName[binding.Name]; ok {
			if !reflect.DeepEqual(old.Binding, *binding) {
				return nil, errCloudRunPSCReview
			}
			continue
		}
		profile, err := manageddb.CloudSQLPSCPostgres(resource.Version)
		identity := connections.AccountIdentity(job.Connection.Cloud, job.Connection.Account, job.Connection.Details)
		if err != nil || resource.Managed.Profile != *profile || resource.Kind != "postgres" || resource.Provider != "cloud-sql" || resource.Region != binding.Region || binding.ConnectionID == "" || binding.ConnectionID != resource.Managed.ConnectionID || identity == "" || resource.Managed.AccountIdentity != identity {
			return nil, errCloudRunPSCReview
		}
		reviewed := false
		for _, action := range job.Plan.Actions {
			if action.Subject != planning.ResourceSubject || action.Name != binding.Name {
				continue
			}
			reviewed = action.Op != planning.Delete && action.Provider == binding.Provider && action.Region == binding.Region && action.Resource != nil && action.Resource.Managed != nil && *action.Resource.Managed == *resource.Managed && action.Resource.Kind == resource.Kind && action.Resource.Version == resource.Version && action.Resource.Region == resource.Region
			selected := action.ConnectionID
			if selected == "" {
				selected = job.Plan.Connections[connections.GCP]
			}
			reviewed = reviewed && selected == binding.ConnectionID
			break
		}
		if !reviewed {
			return nil, errCloudRunPSCReview
		}
		if !planning.CloudSQLApplicationRouteValid(cfg, resource) {
			return nil, errCloudRunPSCReview
		}
		if _, _, err := boundSQLInstance(binding.ExternalID, job.Connection.Details["project_id"], binding.Region); err != nil {
			return nil, errCloudRunPSCReview
		}
		byName[binding.Name] = cloudRunPSCSource{Binding: *binding, Config: *resource}
	}
	if len(byName) == 0 {
		if cfg != nil && cfg.CloudSQLNetwork != nil {
			return nil, errCloudRunPSCReview
		}
		return nil, nil
	}
	if job.Connection.Cloud != connections.GCP || job.Connection.Status != connections.StatusReady || job.Action.Provider != "cloud-run" || cfg.Provider != "cloud-run" || cfg.Region != job.Action.Region || !cfg.CloudSQLNetwork.Valid(job.Action.Region) {
		return nil, errCloudRunPSCReview
	}
	for _, binding := range job.Attach {
		if binding.Provider == "memorystore" {
			return nil, errCloudRunPSCReview
		}
	}
	prepared.transport.Network = *cfg.CloudSQLNetwork
	linked := map[string]bool{}
	keys := map[string]string{}
	for _, link := range cfg.Env {
		source, ok := byName[link.From]
		if !ok || link.Subject != planning.ResourceSubject {
			continue
		}
		if link.Key == "" || link.Address != "" || link.Field != "" && link.Field != ResourceURL || !job.Secrets[link.Key] {
			return nil, errCloudRunPSCReview
		}
		if prior, exists := keys[link.Key]; exists && prior != link.From {
			return nil, errCloudRunPSCReview
		}
		keys[link.Key] = link.From
		value, err := pscDatabaseURL(job.Env[link.Key], source.Binding.Name, source.Binding.ExternalID)
		if err != nil {
			return nil, err
		}
		prepared.env[link.Key] = value
		linked[link.From] = true
	}
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		if !linked[name] {
			return nil, errCloudRunPSCReview
		}
		source := byName[name]
		prepared.sources = append(prepared.sources, source)
		prepared.transport.Instances = append(prepared.transport.Instances, source.Binding.ExternalID)
	}
	if err := prepared.transport.Validate(job.Connection.Details["project_id"], job.Action.Region); err != nil {
		return nil, errCloudRunPSCReview
	}
	return prepared, nil
}

func pscDatabaseURL(raw, name, instance string) (string, error) {
	value, err := url.Parse(raw)
	if err != nil || value == nil || value.Opaque != "" || value.Host != "" || value.Fragment != "" || (value.Scheme != "postgresql" && value.Scheme != "postgres") || value.User == nil || value.User.Username() != sqlUser || value.Path != "/"+databaseName(name) {
		return "", errCloudRunPSCReview
	}
	if password, ok := value.User.Password(); !ok || password == "" {
		return "", errCloudRunPSCReview
	}
	query, err := url.ParseQuery(value.RawQuery)
	if err != nil {
		return "", errCloudRunPSCReview
	}
	for key, values := range query {
		if len(values) != 1 || !slices.Contains([]string{"host", "sslmode", "connect_timeout", "application_name", "options"}, key) {
			return "", errCloudRunPSCReview
		}
	}
	host := query.Get("host")
	if host != "/cloudsql/"+instance && host != cloudrun.PSCSocketDirectory+"/"+instance {
		return "", errCloudRunPSCReview
	}
	query.Set("host", cloudrun.PSCSocketDirectory+"/"+instance)
	value.RawQuery = query.Encode()
	return value.String(), nil
}
