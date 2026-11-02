package planning

import (
	"maps"
	"math"
	"slices"

	"github.com/xraph/loomplane/pkg/provider"
)

// CloudSQLApplicationRouteValid checks the shared review/execution route policy.
// Provider identity, live subnet membership and credentials need separate checks.
func CloudSQLApplicationRouteValid(service *ServiceConfig, resource *ResourceConfig) bool {
	if service == nil || resource == nil || resource.Managed == nil || service.CloudSQLNetwork == nil {
		return false
	}
	route, endpoint := service.CloudSQLNetwork, resource.Managed.CloudSQLPSC
	return service.Provider == "cloud-run" && resource.Provider == "cloud-sql" && service.Region == resource.Region && route.Valid(service.Region) && endpoint.Valid(resource.Region) && route.ConsumerProject == endpoint.ConsumerProject && route.Network == endpoint.Network && !service.PrivateEgress && (service.Size == nil || service.Size.CPU >= 1 && !math.IsInf(service.Size.CPU, 0) && math.Trunc(service.Size.CPU) == service.Size.CPU)
}

func (p *planner) checkCloudSQLApplications() {
	for _, name := range slices.Sorted(maps.Keys(p.services)) {
		action := p.services[name]
		service := action.Service
		if service == nil {
			continue
		}
		psc, memorystore := false, false
		for _, link := range service.Env {
			if link.Subject != ResourceSubject {
				continue
			}
			dep := p.resources[link.From]
			if dep == nil || dep.Resource == nil {
				continue
			}
			memorystore = memorystore || dep.Provider == "memorystore"
			managed := dep.Resource.Managed
			if managed == nil || managed.CloudSQLPSC == (provider.CloudSQLSourceEndpoint{}) {
				continue
			}
			psc = true
			if !CloudSQLApplicationRouteValid(service, dep.Resource) || !p.sameDeploymentAccount(action, dep) {
				p.problem(Finding{Code: "cloud-sql-psc-route-required", Subject: ServiceSubject, Name: name, Path: "services." + name + ".cloud_sql_network", Message: "Choose a Cloud Run service in the database project and region, a matching PSC VPC and subnet, and at least one whole CPU."})
			}
		}
		if service.CloudSQLNetwork != nil && !psc {
			p.problem(Finding{Code: "cloud-sql-psc-route-unused", Subject: ServiceSubject, Name: name, Path: "services." + name + ".cloud_sql_network", Message: "The PSC application route needs a reviewed PSC database dependency."})
		}
		if psc && memorystore {
			p.problem(Finding{Code: "cloud-sql-psc-route-conflict", Subject: ServiceSubject, Name: name, Path: "services." + name + ".needs", Message: "This service cannot combine its explicit PSC route with Memorystore's default network."})
		}
	}
}
