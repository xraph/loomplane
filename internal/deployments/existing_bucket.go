package deployments

import (
	"context"
	"errors"
	"slices"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/planning"
)

// bucketCredentialFields names the workload outputs retained locally. It never
// includes a cloud administrator's credentials or lifecycle authority.
func bucketCredentialFields() []string {
	fields := []string{}
	for _, engine := range []string{catalogconnect.EngineS3, catalogconnect.EngineGCS} {
		for _, key := range catalogconnect.BucketEnvironment(engine) {
			if key != "STORAGE_URL" && !slices.Contains(fields, "output:"+key) {
				fields = append(fields, "output:"+key)
			}
		}
	}
	return fields
}

func (r *Runner) storeExistingAccess(ctx context.Context, d *Deployment, a planning.Action, raw string) error {
	if a.Resource.Kind != "object-storage" {
		return r.resources.Put(ctx, resourceKey(d.OrgID, d.EnvironmentID, a.Name, ResourceURL), []byte(raw))
	}
	outputs, err := catalogconnect.BucketOutputs(raw)
	if err != nil {
		return errors.New("the approved bucket application access is invalid")
	}
	for _, field := range bucketCredentialFields() {
		if _, present := outputs[field[len("output:"):]]; !present {
			if err := r.resources.Delete(ctx, resourceKey(d.OrgID, d.EnvironmentID, a.Name, field)); err != nil {
				return err
			}
		}
	}
	keys := make([]string, 0, len(outputs))
	for key := range outputs {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		field := "output:" + key
		if key == "STORAGE_URL" {
			field = ResourceURL
		}
		if err := r.resources.Put(ctx, resourceKey(d.OrgID, d.EnvironmentID, a.Name, field), []byte(outputs[key])); err != nil {
			return err
		}
	}
	return nil
}
