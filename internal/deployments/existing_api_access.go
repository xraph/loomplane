package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providerresources"
)

const apiOutputFields = "api-output-fields"

func existingAccessOutputs(record *providerresources.ProviderResource, endpoint *catalogconnect.Endpoint, raw string) (map[string]string, error) {
	out := map[string]string{ResourceURL: raw}
	var values map[string]string
	var err error
	switch {
	case catalogconnect.IsAPIEngine(endpoint.Engine):
		values, err = catalogconnect.APIOutputs(raw)
	case endpoint.Engine == catalogconnect.EngineS3 || endpoint.Engine == catalogconnect.EngineGCS || endpoint.Engine == catalogconnect.EngineR2:
		values, err = catalogconnect.BucketOutputs(raw)
	}
	if err != nil {
		return nil, errors.New("the approved application access is invalid")
	}
	if record.Provider == "cloudflare" {
		parts := strings.Split(record.Scope, "/")
		if len(parts) < 3 || parts[0] != "accounts" {
			return nil, errors.New("the Cloudflare binding account is not verified")
		}
		kind := record.Kind
		if kind == "object-storage" {
			kind = "cloudflare-r2"
		}
		native := catalogconnect.APIAccess{Provider: "cloudflare", Kind: kind, AccountID: parts[1], ResourceID: record.ExternalID, Name: record.Name, Native: true, Incarnation: record.Incarnation}
		if kind == "cloudflare-r2" && len(parts) == 4 {
			native.Jurisdiction = parts[3]
		}
		if kind == "cloudflare-durable-object" && len(parts) == 7 {
			script, scriptErr := url.PathUnescape(parts[4])
			class, classErr := url.PathUnescape(parts[6])
			if scriptErr != nil || classErr != nil {
				return nil, errors.New("the Cloudflare namespace scope is invalid")
			}
			native.ScriptName, native.ClassName = script, class
		}
		encoded, e := catalogconnect.EncodeAPIAccess(native)
		if e != nil {
			return nil, e
		}
		binding, e := catalogconnect.APIOutputs(encoded)
		if e != nil {
			return nil, e
		}
		if values == nil {
			values = map[string]string{}
		}
		values["CLOUDFLARE_BINDING"] = binding["CLOUDFLARE_BINDING"]
	}
	for key, value := range values {
		out["output:"+key] = value
	}
	for _, key := range []string{"STORAGE_URL", "DATABASE_URL", "POSTGRES_URL", "REDIS_URL", "KV_URL", "GLOBAL_CONFIG", "BLOB_READ_WRITE_TOKEN", "CLOUDFLARE_BINDING"} {
		if v := values[key]; v != "" {
			out[ResourceURL] = v
			break
		}
	}
	return out, nil
}

func (r *Runner) storeAPIExistingAccess(ctx context.Context, d *Deployment, a planning.Action, record *providerresources.ProviderResource, endpoint *catalogconnect.Endpoint, raw string) error {
	outputs, err := existingAccessOutputs(record, endpoint, raw)
	if err != nil {
		return err
	}
	previous, err := r.resources.Get(ctx, resourceKey(d.OrgID, d.EnvironmentID, a.Name, apiOutputFields))
	if err != nil && !errors.Is(err, ErrNoCredential) {
		return err
	}
	var old []string
	if len(previous) > 0 && json.Unmarshal(previous, &old) != nil {
		return errors.New("the external output inventory is unreadable")
	}
	fields := make([]string, 0, len(outputs))
	for field := range outputs {
		if field != ResourceURL {
			if !validExternalOutputField(field) {
				return errors.New("the external output inventory is invalid")
			}
			fields = append(fields, field)
		}
	}
	slices.Sort(fields)
	union := slices.Clone(fields)
	for _, field := range old {
		if !validExternalOutputField(field) {
			return errors.New("the external output inventory is invalid")
		}
		if !slices.Contains(union, field) {
			union = append(union, field)
		}
	}
	slices.Sort(union)
	encodedUnion, encodeErr := json.Marshal(union)
	if encodeErr != nil {
		return encodeErr
	}
	if inventoryErr := r.resources.Put(ctx, resourceKey(d.OrgID, d.EnvironmentID, a.Name, apiOutputFields), encodedUnion); inventoryErr != nil {
		return inventoryErr
	}
	for _, field := range append(old, bucketCredentialFields()...) {
		if !validExternalOutputField(field) {
			return errors.New("the external output inventory is invalid")
		}
		if _, present := outputs[field]; !present {
			if deleteErr := r.resources.Delete(ctx, resourceKey(d.OrgID, d.EnvironmentID, a.Name, field)); deleteErr != nil {
				return deleteErr
			}
		}
	}
	for _, field := range append(slices.Clone(fields), ResourceURL) {
		if putErr := r.resources.Put(ctx, resourceKey(d.OrgID, d.EnvironmentID, a.Name, field), []byte(outputs[field])); putErr != nil {
			return putErr
		}
	}
	data, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	return r.resources.Put(ctx, resourceKey(d.OrgID, d.EnvironmentID, a.Name, apiOutputFields), data)
}

func (r *Runner) clearExistingAccess(ctx context.Context, d *Deployment, name string) error {
	if r.resources == nil {
		return nil
	}
	encoded, readErr := r.resources.Get(ctx, resourceKey(d.OrgID, d.EnvironmentID, name, apiOutputFields))
	if readErr != nil && !errors.Is(readErr, ErrNoCredential) {
		return readErr
	}
	fields := make([]string, 0, len(resourceCredentialFields)+1)
	if len(encoded) > 0 && json.Unmarshal(encoded, &fields) != nil {
		return errors.New("the external output inventory is unreadable")
	}
	for _, field := range fields {
		if !validExternalOutputField(field) {
			return errors.New("the external output inventory is invalid")
		}
	}
	fields = append(fields, resourceCredentialFields...)
	fields = append(fields, apiOutputFields)
	for _, field := range fields {
		if deleteErr := r.resources.Delete(ctx, resourceKey(d.OrgID, d.EnvironmentID, name, field)); deleteErr != nil {
			return deleteErr
		}
	}
	return nil
}

func validExternalOutputField(field string) bool {
	key, ok := strings.CutPrefix(field, "output:")
	if !ok || key == "" || len(key) > 128 {
		return false
	}
	for i, ch := range key {
		if ch < 'A' || ch > 'Z' {
			if i == 0 || ch != '_' && (ch < '0' || ch > '9') {
				return false
			}
		}
	}
	return true
}
