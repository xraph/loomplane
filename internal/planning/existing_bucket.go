package planning

import "github.com/xraph/loomplane/internal/catalogconnect"

// linkExistingBucket publishes explicit SDK fields, using a resource prefix
// whenever a service consumes multiple buckets so credentials cannot collide.
func (p *planner) linkExistingBucket(_ string, a *Action, need, key string) {
	resource := p.in.Manifest.Resources[need]
	check := p.in.ExistingResources[resource.ExistingResource]
	prefix := ""
	if key != "STORAGE_URL" {
		prefix = envName(need) + "_"
	}
	for _, output := range catalogconnect.BucketEnvironment(check.Endpoint.Engine) {
		field := "output:" + output
		if output == "STORAGE_URL" {
			field = ""
		}
		a.Service.Env = append(a.Service.Env, EnvLink{Key: prefix + output, From: need, Subject: ResourceSubject, Field: field})
	}
}
