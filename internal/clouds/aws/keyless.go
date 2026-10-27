package aws

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"strings"
	"time"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/xraph/loomplane/internal/clouds/keyless"
	"github.com/xraph/loomplane/internal/manageddb"
)

const (
	// audience is the aud STS expects in a web identity token.
	audience = "sts.amazonaws.com"
	// mintTimeout bounds minting a token. The SDK asks for one without a
	// context, so this is all that stops a stuck issuer.
	mintTimeout = 30 * time.Second
	// renewBefore is how long before the role's credentials expire they're
	// renewed, so a request isn't signed with ones about to lapse.
	renewBefore = time.Minute
)

var roleARNRE = regexp.MustCompile(`^arn:aws:iam::\d{12}:role/[\w+=,.@/-]{1,512}$`)

// CheckRoleARN tells whether s could be an IAM role's ARN, such as
// arn:aws:iam::123456789012:role/loomplane.
func CheckRoleARN(s string) error {
	if s == "" {
		return errors.New("give the ARN of the IAM role Loomplane assumes, such as arn:aws:iam::123456789012:role/loomplane")
	}
	if !roleARNRE.MatchString(s) {
		return fmt.Errorf("%q isn't an IAM role's ARN, such as arn:aws:iam::123456789012:role/loomplane", s)
	}
	return nil
}

// WithMinter returns a copy of the client that mints keyless keys' tokens
// with m.
func (c *Client) WithMinter(m keyless.Minter) *Client {
	configured := *c
	configured.minter = m
	return &configured
}

// Keyless reports whether k is a keyless handout's, which assumes RoleARN
// and holds no secret of AWS's.
func (k *Key) Keyless() bool { return k != nil && k.handout != nil }

// keylessKey reads a handout's role and region.
func keylessKey(h keyless.Handout) (*Key, error) {
	role := strings.TrimSpace(h.Settings["role_arn"])
	if err := CheckRoleARN(role); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidKey, err)
	}
	if err := CheckRegion(strings.TrimSpace(h.Settings["region"])); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidKey, err)
	}
	return &Key{RoleARN: role, handout: &h}, nil
}

// webIdentity assumes key's role with a token minted for its handout. STS
// takes the token unsigned, so its client has no credentials.
func (c *Client) webIdentity(key *Key, region string) sdk.CredentialsProvider {
	cfg := sdk.Config{Region: region, Credentials: sdk.AnonymousCredentials{}}
	if c.endpoint != "" {
		cfg.BaseEndpoint = sdk.String(c.endpoint)
	}
	return stscreds.NewWebIdentityRoleProvider(sts.NewFromConfig(cfg), key.RoleARN, retriever{minter: c.minter, handout: *key.handout},
		func(o *stscreds.WebIdentityRoleOptions) { o.RoleSessionName = sessionName(key.handout.Subject) })
}

// retriever mints a token each time the SDK asks for one.
type retriever struct {
	minter  keyless.Minter
	handout keyless.Handout
}

// GetIdentityToken implements stscreds.IdentityTokenRetriever.
func (r retriever) GetIdentityToken() ([]byte, error) {
	if r.minter == nil {
		return nil, keyless.ErrNoMinter
	}
	ctx, cancel := context.WithTimeout(context.Background(), mintTimeout)
	defer cancel()
	token, err := r.minter.Mint(ctx, r.handout)
	if err != nil {
		return nil, fmt.Errorf("mint a keyless token: %w", err)
	}
	return []byte(token), nil
}

// sessionName names the role's sessions after the connection, so
// CloudTrail shows which one acted: loomplane- and its ID, kept to what
// STS allows, which is 64 of [\w+=,.@-].
func sessionName(subject string) string {
	id := subject
	if i := strings.LastIndex(subject, ":connection:"); i >= 0 {
		id = subject[i+len(":connection:"):]
	}
	id = strings.Map(func(r rune) rune {
		if r < 0x80 && (r == '_' || r == '+' || r == '=' || r == ',' || r == '.' || r == '@' || r == '-' ||
			'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9') {
			return r
		}
		return -1
	}, id)
	name := "loomplane-" + id
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

// KeylessSettings implements connections.KeylessCloud: the role to assume
// and its region, and the optional certificate and RDS recovery route a
// key's connection takes too.
func (c *Client) KeylessSettings(in map[string]string) (map[string]string, error) {
	role, region := strings.TrimSpace(in["role_arn"]), strings.TrimSpace(in["region"])
	if err := CheckRoleARN(role); err != nil {
		return nil, err
	}
	if err := CheckRegion(region); err != nil {
		return nil, err
	}
	out := map[string]string{"role_arn": role, "region": region}
	if cert := strings.TrimSpace(in["certificate_arn"]); cert != "" {
		if err := CheckCertificateARN(cert, region); err != nil {
			return nil, err
		}
		out["certificate_arn"] = cert
	}
	route, err := manageddb.RDSRecoverySettings(in)
	if err != nil {
		return nil, err
	}
	maps.Copy(out, route.Settings())
	return out, nil
}

// KeylessAudience implements connections.KeylessCloud.
func (c *Client) KeylessAudience(map[string]string) string { return audience }

// KeylessSetup implements connections.KeylessCloud: add the issuer as an
// identity provider, make a role that trusts subject alone, and give the
// role what a deployment needs.
func (c *Client) KeylessSetup(issuer, subject string) []keyless.SetupStep {
	host := strings.TrimRight(issuer, "/")
	if _, rest, ok := strings.Cut(host, "://"); ok {
		host = rest
	}
	trust := map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{map[string]any{
			"Effect":    "Allow",
			"Principal": map[string]string{"Federated": "arn:aws:iam::<account-id>:oidc-provider/" + host},
			"Action":    "sts:AssumeRoleWithWebIdentity",
			"Condition": map[string]any{"StringEquals": map[string]string{host + ":aud": audience, host + ":sub": subject}},
		}},
	}
	permissions := map[string]any{
		"Version":   "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Action": Actions, "Resource": "*"}},
	}
	return []keyless.SetupStep{
		{
			Title:   "Add Loomplane as an identity provider",
			Body:    "In the AWS account you're connecting, add Loomplane's issuer as an OpenID Connect provider for STS. You can run this with the AWS CLI, or add it in the IAM console under Identity providers.",
			Command: "aws iam create-open-id-connect-provider --url " + strings.TrimRight(issuer, "/") + " --client-id-list " + audience,
		},
		{
			Title:   "Create a role that trusts this connection",
			Body:    "Create an IAM role with this trust policy, putting your account number in place of <account-id>. It names this connection, so only Loomplane acting for it can assume the role.",
			Command: policyJSON(trust),
		},
		{
			Title:   "Give the role Loomplane's permissions",
			Body:    "Attach this policy to the role. Then paste the role's ARN and its region below, and Loomplane checks that AWS lets this connection assume it.",
			Command: policyJSON(permissions),
		},
	}
}

// policyJSON is an IAM policy to paste, indented. It keeps < and > as
// they are, so <account-id> reads as a placeholder.
func policyJSON(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		// Maps of strings always encode, so this doesn't happen.
		return ""
	}
	return strings.TrimSpace(b.String())
}
