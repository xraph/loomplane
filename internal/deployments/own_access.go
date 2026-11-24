package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/planning"
)

// A service wired to a workspace service with access own_database or
// own_user gets a connection string of its own: the registered one with
// its own database, or with a user and database Loomplane makes for it.
// Each release of the service makes sure they're there, from the target
// the service runs on, before its variables are captured, and keeps the
// connection string for the capture to read. A release that stops using
// one lets go of it: an own user loses its login once no environment of
// the project uses it, and nothing is dropped.

// OwnAccessGranter makes and releases a service's own database or user
// on a workspace service. *catalogconnect.Service is the real one.
type OwnAccessGranter interface {
	GrantOwnAccess(ctx context.Context, orgID, endpointID string, a catalogconnect.OwnAccess, dial catalogconnect.DialFunc) (string, error)
	RevokeOwnUser(ctx context.Context, orgID, endpointID, user, environmentID string, dial catalogconnect.DialFunc) error
}

var _ OwnAccessGranter = (*catalogconnect.Service)(nil)

// OwnAccessKey is where the connection string a service gets under its own
// access is kept, in one environment, for one resource.
func OwnAccessKey(orgID, environmentID, resource, service string) string {
	return orgID + "/environments/" + environmentID + "/own-access/" + resource + "/" + service
}

// ownAccessGrant is what OwnAccessKey keeps: the connection string, and
// the workspace service it's on, so it can be let go of after the
// attachment is gone.
type ownAccessGrant struct {
	URL        string `json:"url"`
	EndpointID string `json:"endpoint_id"`
}

// ownAccessLink is the first of a service's links to resource with an
// access of its own, or false. Every link a need makes carries the same.
func ownAccessLink(svc *planning.ServiceConfig, resource string) (planning.EnvLink, bool) {
	if svc == nil {
		return planning.EnvLink{}, false
	}
	for _, l := range svc.Env {
		if l.Subject == planning.ResourceSubject && l.From == resource && l.Access != "" {
			return l, true
		}
	}
	return planning.EnvLink{}, false
}

// grantOwnAccess makes a service's own database or user on the workspace
// service pin attaches as resource, and keeps the connection string it
// gets, when the service is wired to one.
func (r *Runner) grantOwnAccess(ctx context.Context, job *Job, resource string, pin *planning.ExistingResourceConfig) error {
	link, ok := ownAccessLink(job.Action.Service, resource)
	if !ok {
		return nil
	}
	granter, ok := r.workspaceEndpoints.(OwnAccessGranter)
	if !ok || r.resources == nil {
		return errors.New("this Loomplane server can't give a service a database or user of its own")
	}
	d := job.Deployment
	dial, closer, err := r.consumerDial(ctx, d.OrgID, job.Plan, job.Action)
	if err != nil {
		return fmt.Errorf("reach %s from %s's target: %w", resource, job.Action.Name, err)
	}
	if closer != nil {
		defer func() { _ = closer.Close() }() // the grant has finished with it
	}
	address, err := granter.GrantOwnAccess(ctx, d.OrgID, pin.EndpointID, catalogconnect.OwnAccess{
		Access: link.Access, Database: link.Database, User: link.User,
		ProjectID: d.ProjectID, Service: job.Action.Name, EnvironmentID: d.EnvironmentID,
	}, dial)
	if err != nil {
		return fmt.Errorf("give %s its own %s on %s: %w", job.Action.Name, ownWhat(link.Access), resource, err)
	}
	raw, err := json.Marshal(ownAccessGrant{URL: address, EndpointID: pin.EndpointID})
	if err == nil {
		err = r.resources.Put(ctx, OwnAccessKey(d.OrgID, d.EnvironmentID, resource, job.Action.Name), raw)
	}
	if err != nil {
		return fmt.Errorf("keep %s's connection to %s: %w", job.Action.Name, resource, err)
	}
	return nil
}

// ownWhat words an access mode.
func ownWhat(access string) string {
	if access == catalogconnect.AccessOwnUser {
		return "user and database"
	}
	return "database"
}

// ownAccessAddress is the connection string a service got under its own
// access, as its release kept it.
func (r *Runner) ownAccessAddress(ctx context.Context, d *Deployment, app string, link planning.EnvLink) (string, error) {
	// The gateway stands in for the registered address, not one of a
	// service's own.
	if link.Gateway != "" {
		return "", fmt.Errorf("%s reaches %s through the database gateway, which can't carry a database or user of its own; plan again", app, link.From)
	}
	if r.resources == nil {
		return "", ErrNoCredential
	}
	raw, err := r.resources.Get(ctx, OwnAccessKey(d.OrgID, d.EnvironmentID, link.From, app))
	var grant ownAccessGrant
	if err != nil || json.Unmarshal(raw, &grant) != nil || grant.URL == "" {
		return "", fmt.Errorf("%s's own %s on %s wasn't set up; deploy again", app, ownWhat(link.Access), link.From)
	}
	return grant.URL, nil
}

// releaseOwnAccess lets go of the own databases and users a service had
// through before and no longer has through now, which is all of them for
// a service that's gone (now is nil). An own user loses its login once no
// environment of the project uses it; its database and data stay. What
// fails is returned as warnings, since the service's binding has changed
// already.
func (r *Runner) releaseOwnAccess(ctx context.Context, d *Deployment, service string, old *planning.Binding, before planning.ServiceConfig, now *planning.ServiceConfig) []string {
	if r.resources == nil {
		return nil
	}
	var warnings []string
	done := map[string]bool{}
	for _, link := range before.Env {
		if link.Access == "" || done[link.From] {
			continue
		}
		done[link.From] = true
		if kept, ok := ownAccessLink(now, link.From); ok && kept.Access == link.Access && kept.User == link.User && kept.Database == link.Database {
			continue
		}
		key := OwnAccessKey(d.OrgID, d.EnvironmentID, link.From, service)
		raw, err := r.resources.Get(ctx, key)
		var grant ownAccessGrant
		if err != nil || json.Unmarshal(raw, &grant) != nil {
			continue // never granted, so nothing to let go of
		}
		if link.Access == catalogconnect.AccessOwnUser {
			if err := r.revokeOwnUser(ctx, d, service, old, before, grant.EndpointID, link.User); err != nil {
				warnings = append(warnings, fmt.Sprintf("take %s's login away from %s's user %s: %v", link.From, service, link.User, err))
				continue
			}
		}
		if err := r.resources.Delete(ctx, key); err != nil {
			warnings = append(warnings, fmt.Sprintf("forget %s's connection to %s: %v", service, link.From, err))
		}
	}
	return warnings
}

// revokeOwnUser lets go of an own user from the target the service ran on.
func (r *Runner) revokeOwnUser(ctx context.Context, d *Deployment, service string, old *planning.Binding, before planning.ServiceConfig, endpointID, user string) error {
	granter, ok := r.workspaceEndpoints.(OwnAccessGranter)
	if !ok {
		return errors.New("workspace services are unavailable on this Loomplane server")
	}
	consumer := planning.Action{Name: service, Service: &before}
	if old != nil {
		consumer.Provider, consumer.ConnectionID = old.Provider, old.ConnectionID
	}
	dial, closer, err := r.consumerDial(ctx, d.OrgID, nil, consumer)
	if err != nil {
		return err
	}
	if closer != nil {
		defer func() { _ = closer.Close() }() // the revoke has finished with it
	}
	return granter.RevokeOwnUser(ctx, d.OrgID, endpointID, user, d.EnvironmentID, dial)
}
