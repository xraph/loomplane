// Package providerinventory reads metadata through the installed cloud clients.
package providerinventory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/clouds/render"
	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/provider"
)

// Clients shares the same authentication and transport configuration as deployments.
type Clients struct {
	Cloudflare   *cloudflare.Client
	Vercel       *vercel.Client
	DigitalOcean *digitalocean.Client
	Azure        *azure.Client
	AWS          *aws.Client
	Google       *gcp.Client
	Render       *render.Client
}

// Supports reports native inventory capability; Inventory also requires the installed client.
func (*Clients) Supports(cloud string) bool {
	switch cloud {
	case connections.DigitalOcean, connections.Azure, connections.AWS, connections.GCP, connections.Render, connections.Cloudflare, connections.Vercel:
		return true
	}
	return false
}

// Inventory verifies the recorded account before reading its scoped resource metadata.
func (c *Clients) Inventory(ctx context.Context, conn *connections.Connection, credential []byte) (provider.ResourceInventory, error) {
	fail := func() (provider.ResourceInventory, error) {
		return provider.ResourceInventory{}, errors.New("provider resource inventory unavailable; check the account and read permissions")
	}
	if c == nil || conn == nil || !c.Supports(conn.Cloud) {
		return fail()
	}
	identity := connections.AccountIdentity(conn.Cloud, conn.Account, conn.Details)
	if identity == "" || (conn.AccountIdentity != "" && conn.AccountIdentity != identity) {
		return fail()
	}
	var out provider.ResourceInventory
	var err error
	switch conn.Cloud {
	case connections.Cloudflare:
		out, err = c.cloudflareResources(ctx, conn, credential)
	case connections.Vercel:
		out, err = c.vercelResources(ctx, conn, credential)
	case connections.DigitalOcean:
		if c.DigitalOcean == nil {
			return fail()
		}
		token := strings.TrimSpace(string(credential))
		if bytes.HasPrefix(bytes.TrimSpace(credential), []byte("{")) {
			var grant struct {
				AccessToken string `json:"access_token"`
			}
			if json.Unmarshal(credential, &grant) != nil {
				return fail()
			}
			token = grant.AccessToken
		}
		if digitalocean.CheckToken(token) != nil {
			return fail()
		}
		account, e := c.DigitalOcean.Account(ctx, token)
		if e != nil || account == nil {
			return fail()
		}
		if team := conn.Details["team_uuid"]; team != "" {
			if account.Team == nil || account.Team.UUID != team {
				return fail()
			}
		} else if account.Team != nil && account.Team.UUID != "" || account.UUID != conn.Details["account_uuid"] {
			return fail()
		}
		out, err = c.DigitalOcean.ResourceInventory(ctx, token)
		scopeID := account.UUID
		if account.Team != nil && account.Team.UUID != "" {
			scopeID = account.Team.UUID
		}
		out.Scope = "account/" + scopeID + "/all-regions/databases"
	case connections.Azure:
		if c.Azure == nil {
			return fail()
		}
		cred, e := azure.ParseCredential(credential, conn.Details)
		if e != nil {
			return fail()
		}
		session, e := c.Azure.Session(ctx, cred)
		if e != nil {
			return fail()
		}
		sub, e := session.Subscription(ctx)
		if e != nil || sub == nil || !strings.EqualFold(sub.ID, cred.SubscriptionID) || !strings.EqualFold(sub.TenantID, cred.TenantID) {
			return fail()
		}
		out, err = session.ResourceInventory(ctx)
	case connections.AWS:
		if c.AWS == nil || aws.CheckRegion(conn.Details["region"]) != nil {
			return fail()
		}
		key, e := aws.ParseKey(credential)
		if e != nil {
			return fail()
		}
		account, e := c.AWS.Identify(ctx, key, conn.Details["region"])
		if e != nil || account == nil || account.Account != conn.Account {
			return fail()
		}
		out, err = c.AWS.Session(key, conn.Details["region"], account.Account).ResourceInventory(ctx)
	case connections.GCP:
		if c.Google == nil {
			return fail()
		}
		key, e := gcp.ParseKey(credential)
		if e != nil || key.ProjectID != conn.Details["project_id"] {
			return fail()
		}
		session, e := c.Google.Session(ctx, key)
		if e != nil {
			return fail()
		}
		out, err = session.ResourceInventory(ctx)
	case connections.Render:
		if c.Render == nil || render.CheckKey(string(credential)) != nil {
			return fail()
		}
		key := render.Key{Value: string(credential), OwnerID: conn.Details["owner_id"]}
		owners, e := c.Render.Owners(ctx, key)
		if e != nil {
			return fail()
		}
		found := false
		for _, owner := range owners {
			if owner.ID == key.OwnerID {
				found = true
			}
		}
		if !found {
			return fail()
		}
		out, err = c.Render.ResourceInventory(ctx, key)
	}
	if err != nil {
		out.Complete = false
		return out, errors.New("provider resource inventory could not be read completely; check read permissions and retry")
	}
	return out, nil
}

// SupportedProviders lists the native readers installed in this process.
func (c *Clients) SupportedProviders() []string {
	out := []string{}
	if c == nil {
		return out
	}
	if c.Cloudflare != nil {
		out = append(out, connections.Cloudflare)
	}
	if c.Vercel != nil {
		out = append(out, connections.Vercel)
	}
	if c.DigitalOcean != nil {
		out = append(out, connections.DigitalOcean)
	}
	if c.Azure != nil {
		out = append(out, connections.Azure)
	}
	if c.AWS != nil {
		out = append(out, connections.AWS)
	}
	if c.Google != nil {
		out = append(out, connections.GCP)
	}
	if c.Render != nil {
		out = append(out, connections.Render)
	}
	return out
}
