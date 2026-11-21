package planning

import (
	"cmp"
	"context"
	"fmt"
	"slices"
)

// DropletPostgresProbe checks the bound cluster's private endpoint from the SSH target. Its errors are safe to show in a plan.
type DropletPostgresProbe func(ctx context.Context, org, target string, db *Binding) error

// linkDropletPostgres handles the native database only for a uniquely identified Droplet.
func (p *planner) linkDropletPostgres(a, db *Action, key, path string) bool {
	i := slices.IndexFunc(p.in.Connections, func(c ConnectionOption) bool {
		return c.ID == a.Service.Target && c.Cloud == "server" && c.Ready
	})
	if i < 0 || len(p.in.Connections[i].Servers) != 1 {
		return false
	}
	host := p.in.Connections[i].Servers[0]
	if host.DropletAccount == "" {
		return false
	}
	// Compare the provisioning account, not the SSH runtime credential.
	connectionID := db.ConnectionID
	if connectionID == "" {
		connectionID = p.connectionFor("digitalocean")
	}
	sameAccount := slices.ContainsFunc(p.in.Connections, func(c ConnectionOption) bool {
		return c.ID == connectionID && c.Ready && c.Cloud == "digitalocean" && cmp.Or(c.Details["team_uuid"], c.Details["account_uuid"]) == host.DropletAccount
	})
	if !sameAccount {
		p.problem(Finding{Code: "resource-across-accounts", Subject: ServiceSubject, Name: a.Name, Path: path, Message: fmt.Sprintf("%s runs on a DigitalOcean Droplet. Choose a Postgres connection in the Droplet's DigitalOcean account for %s.", a.Name, db.Name)})
		return true
	}
	primary := len(a.Service.Env)
	p.linkResource(a.Name, a, db.Name, key, path)
	for i := primary; i < len(a.Service.Env); i++ {
		a.Service.Env[i].DropletPostgres = true
	}
	a.DependsOn = append(a.DependsOn, db.ID)
	return true
}

func (s *Service) checkDropletPostgres(ctx context.Context, plan *Plan, result *Result, bindings []*Binding) {
	live := map[string]*Binding{}
	for _, b := range bindings {
		if b.OrgID == plan.OrgID && b.ProjectID == plan.ProjectID && b.EnvironmentID == plan.EnvironmentID && b.Subject == ResourceSubject {
			live[b.Name] = b
		}
	}
	resources := map[string]Op{}
	for _, a := range result.Actions {
		if a.Subject == ResourceSubject {
			resources[a.Name] = a.Op
		}
	}
	type route struct{ target, resource string }
	checked := map[route]error{}
	for _, a := range result.Actions {
		if a.Service == nil || a.Op == Delete || (a.Op == Keep && len(plan.Services) > 0 && !slices.Contains(plan.Services, a.Name)) {
			continue
		}
		seen := map[string]bool{}
		for _, link := range a.Service.Env {
			if !link.DropletPostgres || seen[link.From] {
				continue
			}
			seen[link.From] = true
			f := Finding{Subject: ServiceSubject, Name: a.Name, Path: "services." + a.Name + ".needs"}
			if op := resources[link.From]; (op == Create || op == Replace) && s.dropletPostgres != nil {
				f.Code = "postgres-check-after-provisioning"
				f.Message = fmt.Sprintf("%s will check private access to %s from its DigitalOcean Droplet after provisioning. The service can start once Postgres accepts the connection.", a.Name, link.From)
				result.Warnings = append(result.Warnings, f)
				continue
			}
			r := route{a.Service.Target, link.From}
			err, ok := checked[r]
			if !ok && s.dropletPostgres != nil && live[r.resource] != nil {
				err = s.dropletPostgres(ctx, plan.OrgID, r.target, live[r.resource])
				checked[r] = err
			}
			if s.dropletPostgres == nil || live[r.resource] == nil || err != nil {
				f.Code = "droplet-postgres-unverified"
				reason := "the binding or verifier is unavailable"
				if err != nil {
					reason = err.Error()
				}
				f.Message = fmt.Sprintf("Could not verify private access from the Droplet running %s to %s: %s.", a.Name, link.From, reason)
				result.Problems = append(result.Problems, f)
				result.Executable = false
			}
		}
	}
}
