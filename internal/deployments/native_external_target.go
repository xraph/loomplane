package deployments

import (
	"errors"
	"reflect"
	"slices"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

// omitEmptyResourceToken removes a stale session token on a switch to long-term
// application keys. Native secret APIs cannot create a secret with no value.
func omitEmptyResourceToken(key string, env map[string]string, links []planning.EnvLink) bool {
	return env[key] == "" && slices.ContainsFunc(links, func(link planning.EnvLink) bool {
		return link.Key == key && link.Subject == planning.ResourceSubject && link.Field == "output:AWS_SESSION_TOKEN"
	})
}

// checkExternalResourceTarget fences native secret delivery to the reviewed
// account and placement. Token rotation in the same account remains allowed.
func checkExternalResourceTarget(org string, a planning.Action, c *connections.Connection) error {
	if a.Op == planning.Delete || a.Service == nil || a.Service.TargetAccountIdentity == "" {
		return nil
	}
	cloud := ""
	switch a.Provider {
	case "digitalocean":
		cloud = connections.DigitalOcean
	case "azure-container-apps":
		cloud = connections.Azure
	}
	if cloud == "" || c == nil || c.ID != a.ConnectionID || c.OrgID != org || c.Cloud != cloud || c.Status != connections.StatusReady || connections.AccountIdentity(c.Cloud, c.Account, c.Details) != a.Service.TargetAccountIdentity || !reflect.DeepEqual(planning.ReviewedConnectionSettings(c.Cloud, c.Details), a.Service.TargetSettings) {
		return errors.New("the external resource's reviewed deployment account or settings changed; review the deployment again")
	}
	return nil
}
