package planning

import "github.com/xraph/loomplane/internal/connections"

// ResourceCheckNote describes where a check actually opens its connection.
// Native platform readiness does not establish an application's outbound access.
func ResourceCheckNote(cloud string) string {
	switch cloud {
	case "kubernetes":
		return "Checked from Loomplane, not from inside the cluster."
	case "digitalocean", "azure":
		return "Credentials and connectivity are checked from Loomplane. This does not verify the application's outbound network. Configure the provider's trusted sources, firewall and private network access before deployment."
	}
	return ""
}

// pinExternalResourceTarget ties secret delivery to the reviewed native account.
func (p *planner) pinExternalResourceTarget(name string, a *Action, path string) {
	cloud := p.cloud(a.Provider)
	if a.Service == nil || (cloud != connections.DigitalOcean && cloud != connections.Azure) {
		return
	}
	id := a.ConnectionID
	if id == "" {
		id = p.in.Chosen[cloud]
	}
	if id == "" {
		if c, ok := p.connection(cloud); ok {
			id = c.ID
		}
	}
	c := p.targetConnection(name, path, "target", cloud, id)
	if c == nil {
		return
	}
	identity := connections.AccountIdentity(c.Cloud, c.Account, c.Details)
	if identity == "" {
		p.problem(Finding{Code: "external-resource-target-identity", Subject: ServiceSubject, Name: name, Path: path, Message: "Check the deployment account's identity before sending it external resource credentials."})
		return
	}
	a.ConnectionID = c.ID
	a.Service.TargetAccountIdentity = identity
	a.Service.TargetSettings = ReviewedConnectionSettings(c.Cloud, c.Details)
}
