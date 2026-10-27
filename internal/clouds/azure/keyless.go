package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/keyless"
)

const (
	// audience is the aud Microsoft Entra ID expects in a token an app's
	// federated credential trusts.
	audience = "api://AzureADTokenExchange"
	// assertionType says the client assertion is a signed JWT.
	assertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
	// mintTimeout bounds minting a token, so a stuck issuer can't hold up
	// a session's renewal for good.
	mintTimeout = 30 * time.Second
)

// aadstsRE finds the AADSTS number in an Entra ID error description.
var aadstsRE = regexp.MustCompile(`AADSTS(\d{1,9})`)

// trustRefused are the AADSTS numbers that say the app doesn't trust
// Loomplane's token: no federated credential matches it (70021), or none
// matches its issuer, audience or subject (700211, 700212, 700213), the
// app has no federated credential left at all (70025), or the app isn't
// in the tenant (700016) or is disabled (7000112). Entra files 70021
// under invalid_request as well as invalid_client, so the number decides,
// not the error. Trying again won't change any of them.
var trustRefused = map[string]bool{"70021": true, "70025": true, "700211": true, "700212": true, "700213": true, "700016": true, "7000112": true}

// entraFailed are the AADSTS numbers that say Entra couldn't judge the
// token, whatever error it's filed under: its own service failing
// (50000), not reaching or reading Loomplane's discovery document or
// keys (50166, 501661, 900612), a signature its cached keys don't check (700027), or a token
// outside its time range (700024). Each token is minted just before it's
// sent, so that last one means the clocks of Loomplane and Entra
// disagree, which is Loomplane's to fix and not the trust's.
var entraFailed = map[string]bool{"50000": true, "50166": true, "501661": true, "900612": true, "700027": true, "700024": true}

// credentialNameRE is what a federated credential's name may hold besides
// the loomplane- it starts with.
var credentialNameRE = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// WithMinter returns a copy of the client that mints keyless credentials'
// client assertions with m.
func (c *Client) WithMinter(m keyless.Minter) *Client {
	configured := *c
	configured.minter = m
	return &configured
}

// Keyless reports whether c is a keyless handout's, which holds no secret
// of Azure's: the app's federated credential trusts a token minted for
// the handout.
func (c Credential) Keyless() bool { return c.handout != nil }

// keylessCredential reads a handout's app. Where it deploys comes from the
// connection's settings, as a sign-in's does.
func keylessCredential(h keyless.Handout) (Credential, error) {
	if h.Audience != audience {
		return Credential{}, fmt.Errorf("%w: the keyless handout's audience isn't Azure's", ErrInvalid)
	}
	client := strings.ToLower(strings.TrimSpace(h.Settings["client_id"]))
	if !guidRE.MatchString(client) {
		return Credential{}, fmt.Errorf("%w: client_id %q isn't an application (client) ID; they look like 00000000-0000-0000-0000-000000000000", ErrInvalid, client)
	}
	return Credential{ClientID: client, handout: &h}, nil
}

// assertion mints a fresh client assertion for the session's handout,
// with the request's context.
func (s *Session) assertion(ctx context.Context) (string, error) {
	if s.c.minter == nil {
		return "", keyless.ErrNoMinter
	}
	ctx, cancel := context.WithTimeout(ctx, mintTimeout)
	defer cancel()
	token, err := s.c.minter.Mint(ctx, *s.cred.handout)
	if err != nil {
		return "", fmt.Errorf("mint a keyless token: %w", err)
	}
	return token, nil
}

// assertionAnswer is why Entra ID didn't take Loomplane's token as the
// app's client assertion. Only Entra refusing the trust itself is
// ErrRejected: one of trustRefused, whatever error it's filed under, or
// unauthorized_client with any number but entraFailed's. Entra failing or being busy isn't, and neither is
// anything else it says, which a retry may get past. It keeps only the
// error code and the AADSTS number: the description can repeat what was
// sent.
func assertionAnswer(status int, code string, codes []int, description string) error {
	number := ""
	if len(codes) > 0 && codes[0] > 0 {
		number = strconv.Itoa(codes[0])
	} else if m := aadstsRE.FindStringSubmatch(description); m != nil {
		number = m[1]
	}
	why := errorCode(code)
	switch {
	case number != "" && code == "":
		why = "AADSTS" + number
	case number != "":
		why += " AADSTS" + number
	}
	switch {
	case status != http.StatusBadRequest && status != http.StatusUnauthorized:
		// A 5xx or a 429 is Entra failing or busy, never a refusal.
		return fmt.Errorf("azure sign-in answered %d: %s", status, why)
	case number == "700024":
		return fmt.Errorf("azure says Loomplane's token isn't valid at this time (%s); check the clock of the machine Loomplane runs on", why)
	case number == "50166" || number == "501661" || number == "900612":
		return fmt.Errorf("azure couldn't fetch Loomplane's discovery document or signing keys (%s); check that its public URL is reachable from the internet", why)
	case entraFailed[number]:
		return fmt.Errorf("azure couldn't check Loomplane's token (%s); try again", why)
	case trustRefused[number] || (code == "unauthorized_client"):
		return fmt.Errorf("%w: Azure doesn't trust Loomplane for this app; check its federated credential (%s)", ErrRejected, why)
	}
	return fmt.Errorf("azure didn't take Loomplane's token (%s)", why)
}

// KeylessSettings implements connections.KeylessCloud: the app whose
// federated credential trusts Loomplane, as client_id, and where the
// connection deploys, as CheckPlacement checks it.
func (c *Client) KeylessSettings(in map[string]string) (map[string]string, error) {
	tenant, subscription, group, err := CheckPlacement(in)
	if err != nil {
		return nil, err
	}
	client := strings.ToLower(strings.TrimSpace(in["client_id"]))
	if !guidRE.MatchString(client) {
		return nil, fmt.Errorf("client_id %q isn't an application (client) ID; they look like 00000000-0000-0000-0000-000000000000", client)
	}
	return map[string]string{"tenant_id": tenant, "client_id": client, "subscription_id": subscription, "resource_group": group}, nil
}

// KeylessAudience implements connections.KeylessCloud.
func (c *Client) KeylessAudience(map[string]string) string { return audience }

// credentialName names a connection's federated credential, which must be
// unique in its app: loomplane- and the connection's ID, with a colon as a
// hyphen and anything else Entra doesn't allow left out, kept to Entra's
// 120 characters.
func credentialName(subject string) string {
	id := subject
	if i := strings.LastIndex(subject, ":connection:"); i >= 0 {
		id = subject[i+len(":connection:"):]
	}
	id = credentialNameRE.ReplaceAllString(strings.ReplaceAll(id, ":", "-"), "")
	name := "loomplane-" + id
	if len(name) > 120 {
		name = name[:120]
	}
	return name
}

// KeylessSetup implements connections.KeylessCloud: add a federated
// credential that trusts subject alone to an app registration, and make
// the app a Contributor on the resource group.
func (c *Client) KeylessSetup(issuer, subject string) []keyless.Step {
	credential := struct {
		Name      string   `json:"name"`
		Issuer    string   `json:"issuer"`
		Subject   string   `json:"subject"`
		Audiences []string `json:"audiences"`
	}{Name: credentialName(subject), Issuer: strings.TrimRight(issuer, "/"), Subject: subject, Audiences: []string{audience}}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(credential); err != nil {
		// A struct of strings always encodes, so this doesn't happen.
		return nil
	}
	return []keyless.Step{
		{
			Title: "Let your app registration trust this connection",
			Body: "Loomplane acts as an app registration in the tenant you're connecting. If you don't have one for it, run az ad app create --display-name loomplane, then az ad sp create --id <client-id> so the app can hold roles. " +
				"Put the app's application (client) ID in place of <client-id> and run this in Cloud Shell, or anywhere az is signed in to the tenant. It adds a federated credential that names this connection, so only Loomplane acting for it gets in.",
			Command: "az ad app federated-credential create --id <client-id> --parameters '" + strings.TrimSpace(b.String()) + "'",
		},
		{
			Title: "Make the app a Contributor",
			Body: "Put your subscription ID in place of <subscription-id> and the resource group Loomplane deploys to in place of <resource-group>. The group has to exist first, and az group create --name <resource-group> --location <region> makes it. " +
				"Then paste the tenant ID, the app's client ID, the subscription ID and the resource group below, and Loomplane checks that Azure takes this connection's token.",
			Command: "az role assignment create --assignee <client-id> --role Contributor --scope /subscriptions/<subscription-id>/resourceGroups/<resource-group>",
		},
	}
}
