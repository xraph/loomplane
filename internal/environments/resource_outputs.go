package environments

import "context"

// ResourceMappings returns winning resource references for each service layer. The empty service holds the project defaults.
func (s *Service) ResourceMappings(ctx context.Context, org, project, envID, envName string) (map[string]map[string]string, error) {
	rows, err := s.store.ForEnvironment(ctx, org, project, envID, envName)
	if err != nil {
		return nil, err
	}
	return resolveMappings(rows), nil
}

func resolveMappings(rows []*Variable) map[string]map[string]string {
	services := map[string]bool{"": true}
	mapped := false
	for _, row := range rows {
		services[row.Service] = true
		mapped = mapped || row.ResourceOutput != ""
	}
	if !mapped {
		return nil
	}
	out := make(map[string]map[string]string, len(services))
	for service := range services {
		refs := map[string]string{}
		for _, winner := range resolve(rows, service) {
			if winner.Variable.ResourceOutput != "" {
				refs[winner.Key] = winner.Variable.ResourceOutput
			}
		}
		out[service] = refs
	}
	return out
}
