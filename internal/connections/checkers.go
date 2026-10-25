package connections

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/registry"
	"github.com/xraph/loomplane/internal/clouds/render"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/pkg/provider/sdk"
)

// GCPChecker checks Google Cloud service account keys.
type GCPChecker struct{ Client *gcp.Client }

// Parse implements Checker. The one setting is billing_export, the
// BigQuery table Cloud Billing exports to, which spend is read from.
func (g GCPChecker) Parse(credential []byte, settings map[string]string) (Check, error) {
	key, err := gcp.ParseKey(credential)
	if err != nil {
		return Check{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	details := map[string]string{"project_id": key.ProjectID, "service_account": key.ClientEmail}
	if table := strings.TrimSpace(settings[gcp.BillingExportSetting]); table != "" {
		if err := gcp.CheckBillingTable(table); err != nil {
			return Check{}, fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		details[gcp.BillingExportSetting] = table
	}
	return Check{Account: key.ProjectID, Details: details}, nil
}

// Check implements Checker: it asks Google which of the permissions a
// deployment needs the service account holds.
func (g GCPChecker) Check(ctx context.Context, credential []byte, settings map[string]string) (Check, error) {
	c, err := g.Parse(credential, settings)
	if err != nil {
		return Check{}, err
	}
	key, err := gcp.ParseKey(credential)
	if err != nil {
		return Check{}, err
	}
	s, err := g.Client.Session(ctx, key)
	if err != nil {
		return Check{}, err
	}
	if c.Missing, err = s.Missing(ctx); err != nil {
		return Check{}, err
	}
	return c, nil
}

// teamRE matches Vercel team IDs, such as team_a1B2c3.
var teamRE = regexp.MustCompile(`^team_[A-Za-z0-9]{1,64}$`)

// VercelChecker checks Vercel access tokens.
type VercelChecker struct{ Client *vercel.Client }

// Parse implements Checker. The one setting is team_id; without it the
// token deploys to the person's own account.
func (v VercelChecker) Parse(credential []byte, settings map[string]string) (Check, error) {
	if err := vercel.CheckToken(string(credential)); err != nil {
		return Check{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	details := map[string]string{}
	if team := strings.TrimSpace(settings["team_id"]); team != "" {
		if !teamRE.MatchString(team) {
			return Check{}, fmt.Errorf("%w: %q isn't a Vercel team ID; they look like team_ followed by letters and digits", ErrInvalid, team)
		}
		details["team_id"] = team
	}
	return Check{Details: details}, nil
}

// Check implements Checker: it asks Vercel who the token is.
func (v VercelChecker) Check(ctx context.Context, credential []byte, settings map[string]string) (Check, error) {
	c, err := v.Parse(credential, settings)
	if err != nil {
		return Check{}, err
	}
	acct, err := v.Client.Check(ctx, vercel.Token{Value: strings.TrimSpace(string(credential)), TeamID: c.Details["team_id"]})
	if err != nil {
		return Check{}, err
	}
	c.Account = acct.Name
	if acct.TeamID != "" {
		c.Details["team_id"] = acct.TeamID
	} else {
		c.Details["user_id"] = acct.UserID
	}
	return c, nil
}

// ownerRE matches Render workspace IDs: tea- for a team, usr- for a
// person's own.
var ownerRE = regexp.MustCompile(`^(tea|usr)-[a-z0-9]{1,64}$`)

// RenderChecker checks Render API keys. The one setting is owner_id, the
// workspace to deploy in. A key reaches every workspace its person is in;
// without the setting, the only one it reaches is used, and several is an
// error naming them.
type RenderChecker struct{ Client *render.Client }

// Parse implements Checker.
func (r RenderChecker) Parse(credential []byte, settings map[string]string) (Check, error) {
	if err := render.CheckKey(string(credential)); err != nil {
		return Check{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	details := map[string]string{}
	if owner := strings.TrimSpace(settings["owner_id"]); owner != "" {
		if !ownerRE.MatchString(owner) {
			return Check{}, fmt.Errorf("%w: %q isn't a Render workspace ID; they look like tea- or usr- followed by letters and digits", ErrInvalid, owner)
		}
		details["owner_id"] = owner
	}
	return Check{Details: details}, nil
}

// Check implements Checker: it asks Render which workspaces the key
// reaches, and settles on one.
func (r RenderChecker) Check(ctx context.Context, credential []byte, settings map[string]string) (Check, error) {
	c, err := r.Parse(credential, settings)
	if err != nil {
		return Check{}, err
	}
	owners, err := r.Client.Owners(ctx, render.Key{Value: strings.TrimSpace(string(credential))})
	if err != nil {
		return Check{}, err
	}
	want := c.Details["owner_id"]
	names := make([]string, 0, len(owners))
	for _, o := range owners {
		if o.ID == want || (want == "" && len(owners) == 1) {
			c.Details["owner_id"], c.Account = o.ID, o.Name
			return c, nil
		}
		names = append(names, o.Name+" ("+o.ID+")")
	}
	switch {
	case len(owners) == 0:
		return Check{}, fmt.Errorf("%w: the key reaches no Render workspace", render.ErrRejected)
	case want != "":
		return Check{}, fmt.Errorf("%w: the key can't reach workspace %s; it reaches %s", render.ErrRejected, want, strings.Join(names, ", "))
	}
	return Check{}, fmt.Errorf("%w: the key reaches %d workspaces, %s; set owner_id to the one to deploy in", ErrInvalid, len(owners), strings.Join(names, ", "))
}

// DigitalOceanChecker checks DigitalOcean API tokens. It needs no
// settings: a token belongs to one team, and that's where it deploys.
type DigitalOceanChecker struct{ Client *digitalocean.Client }

// Parse implements Checker.
func (d DigitalOceanChecker) Parse(credential []byte, _ map[string]string) (Check, error) {
	if err := digitalocean.CheckToken(string(credential)); err != nil {
		return Check{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return Check{Details: map[string]string{}}, nil
}

// Check implements Checker: it asks DigitalOcean whose token it is. A
// locked account can't create anything, so it's refused.
func (d DigitalOceanChecker) Check(ctx context.Context, credential []byte, settings map[string]string) (Check, error) {
	c, err := d.Parse(credential, settings)
	if err != nil {
		return Check{}, err
	}
	a, err := d.Client.Account(ctx, strings.TrimSpace(string(credential)))
	if err != nil {
		return Check{}, err
	}
	if a.Status == "locked" {
		return Check{}, fmt.Errorf("%w: the account %s is locked", digitalocean.ErrRejected, a.Email)
	}
	c.Account, c.Details["account_uuid"] = a.Email, a.UUID
	if a.Team != nil && a.Team.UUID != "" {
		c.Account, c.Details["team_uuid"] = a.Team.Name, a.Team.UUID
	}
	return c, nil
}

// AzureChecker checks Azure service principals. The credential is the
// principal's client secret; the settings say whose it is and where it
// deploys: tenant_id, client_id and subscription_id, and resource_group,
// which defaults to loomplane.
type AzureChecker struct{ Client *azure.Client }

// Parse implements Checker.
func (a AzureChecker) Parse(credential []byte, settings map[string]string) (Check, error) {
	cred, err := azure.ParseCredential(credential, settings)
	if err != nil {
		return Check{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return Check{Details: map[string]string{
		"tenant_id": cred.TenantID, "client_id": cred.ClientID,
		"subscription_id": cred.SubscriptionID, "resource_group": cred.ResourceGroup,
	}}, nil
}

// Check implements Checker: it signs in as the principal, reads its
// subscription, and asks Azure which of the actions a deployment needs
// its role assignments allow.
func (a AzureChecker) Check(ctx context.Context, credential []byte, settings map[string]string) (Check, error) {
	c, err := a.Parse(credential, settings)
	if err != nil {
		return Check{}, err
	}
	cred, err := azure.ParseCredential(credential, settings)
	if err != nil {
		return Check{}, err
	}
	s, err := a.Client.Session(ctx, cred)
	if err != nil {
		return Check{}, err
	}
	sub, err := s.Subscription(ctx)
	if err != nil {
		return Check{}, err
	}
	if sub.TenantID != "" && azureGUID(cred.TenantID) && !strings.EqualFold(sub.TenantID, cred.TenantID) {
		return Check{}, fmt.Errorf("%w: subscription %s is in tenant %s, not %s", azure.ErrRejected, cred.SubscriptionID, sub.TenantID, cred.TenantID)
	}
	if sub.TenantID != "" {
		// A tenant named by its domain is recorded by its ID, which
		// doesn't change.
		c.Details["tenant_id"] = strings.ToLower(sub.TenantID)
	}
	c.Account = sub.DisplayName
	if c.Account == "" {
		c.Account = cred.SubscriptionID
	}
	if c.Missing, err = s.Missing(ctx); err != nil {
		return Check{}, err
	}
	// Pulling with an identity takes more than Contributor, so it's
	// reported, never required.
	if c.Details["registry_pull"], err = s.PullMode(ctx); err != nil {
		return Check{}, err
	}
	return c, nil
}

func azureGUID(s string) bool { return len(s) == 36 && strings.Count(s, "-") == 4 }

// rejected reports whether a check failed because the cloud refused the
// credential, as opposed to the cloud being unreachable.
func rejected(err error) bool {
	var ge *gcp.APIError
	var ve *vercel.APIError
	var re *render.APIError
	var de *digitalocean.APIError
	return errors.Is(err, render.ErrRejected) || errors.As(err, &re) ||
		errors.Is(err, digitalocean.ErrRejected) || errors.As(err, &de) || errors.Is(err, gcp.ErrRejected) || errors.Is(err, gcp.ErrInvalidKey) || errors.Is(err, vercel.ErrRejected) ||
		errors.Is(err, server.ErrRejected) || errors.Is(err, kubernetes.ErrRejected) || errors.Is(err, registry.ErrRejected) ||
		errors.Is(err, aws.ErrRejected) || errors.Is(err, azure.ErrRejected) || errors.Is(err, sdk.ErrRejected) ||
		errors.As(err, &ge) || errors.As(err, &ve)
}

// AWSChecker checks AWS access keys in a required region. Optional private
// recovery route settings do not establish connectivity or SQL verification.
type AWSChecker struct{ Client *aws.Client }

// Parse implements Checker.
func (a AWSChecker) Parse(credential []byte, settings map[string]string) (Check, error) {
	key, err := aws.ParseKey(credential)
	if err != nil {
		return Check{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	region := strings.TrimSpace(settings["region"])
	if err := aws.CheckRegion(region); err != nil {
		return Check{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	details := map[string]string{"region": region, "access_key": "…" + key.AccessKeyID[len(key.AccessKeyID)-4:]}
	// An ACM certificate in the region serves public ECS services over
	// HTTPS; without one they serve HTTP.
	if cert := strings.TrimSpace(settings["certificate_arn"]); cert != "" {
		if !strings.HasPrefix(cert, "arn:aws:acm:"+region+":") || !strings.Contains(cert, ":certificate/") {
			return Check{}, fmt.Errorf("%w: %q isn't an ACM certificate in %s", ErrInvalid, cert, region)
		}
		details["certificate_arn"] = cert
	}
	route, routeErr := manageddb.RDSRecoverySettings(settings)
	if routeErr != nil {
		return Check{}, fmt.Errorf("%w: %w", ErrInvalid, routeErr)
	}
	for key, value := range route.Settings() {
		details[key] = value
	}

	return Check{Details: details}, nil
}

// Check implements Checker: it asks STS who the key is, then IAM's policy
// simulator which actions a deployment needs it may take. A key that may
// not run the simulator is ready, with its permissions unchecked.
func (a AWSChecker) Check(ctx context.Context, credential []byte, settings map[string]string) (Check, error) {
	c, err := a.Parse(credential, settings)
	if err != nil {
		return Check{}, err
	}
	key, err := aws.ParseKey(credential)
	if err != nil {
		return Check{}, err
	}
	who, err := a.Client.Identify(ctx, key, c.Details["region"])
	if err != nil {
		return Check{}, err
	}
	c.Account = who.Account
	c.Details["principal"] = who.ARN
	missing, checked, err := a.Client.Missing(ctx, key, c.Details["region"], principalARN(who.ARN))
	if err != nil {
		return Check{}, err
	}
	if !checked {
		c.Details["permissions"] = "unchecked"
	}
	c.Missing = missing
	return c, nil
}

// principalARN is what the policy simulator takes for an identity: an
// assumed role's session ARN names the role instead.
func principalARN(arn string) string {
	if !strings.Contains(arn, ":assumed-role/") {
		return arn
	}
	parts := strings.Split(arn, ":")
	if len(parts) < 6 {
		return arn
	}
	role := strings.Split(strings.TrimPrefix(parts[5], "assumed-role/"), "/")[0]
	return "arn:aws:iam::" + parts[4] + ":role/" + role
}
