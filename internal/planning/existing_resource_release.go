package planning

// checkExistingResourceCanaries runs after actual ramping actions are selected.
// Direct workers in a mixed release can still update their external access.
func checkExistingResourceCanaries(result *Result) {
	if result.Release == nil {
		return
	}
	resources := map[string]*Action{}
	for i := range result.Actions {
		a := &result.Actions[i]
		if a.Subject == ResourceSubject {
			resources[a.Name] = a
		}
	}
	for _, service := range result.Actions {
		if service.Service == nil || service.Provider != "azure-container-apps" || !result.Release.Ramps(service.ID) {
			continue
		}
		for _, link := range service.Service.Env {
			dependency := resources[link.From]
			if link.Subject != ResourceSubject || dependency == nil || dependency.Resource == nil || dependency.Resource.ExistingResource == nil || dependency.Op == Keep {
				continue
			}
			result.Problems = append(result.Problems, Finding{Code: "canary-external-resource-access", Subject: ServiceSubject, Name: service.Name, Path: "services." + service.Name + ".needs", Message: "Use a direct release when adding or changing external resource access on Azure Container Apps. Its canary recovery restores the previous revision's secret references."})
			result.Executable = false
			break
		}
	}
}
