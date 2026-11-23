package aws

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// repositoryPrefix is an ECR repository name prefix, without a slash at
// either end.
var repositoryPrefix = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*$`)

// pullActions and pushActions are what a scoped ECR login may do on the
// repositories under its prefix.
var (
	pullActions = []string{"ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer", "ecr:BatchCheckLayerAvailability"}
	pushActions = append(append([]string{}, pullActions...), "ecr:InitiateLayerUpload", "ecr:UploadLayerPart", "ecr:CompleteLayerUpload", "ecr:PutImage")
)

// RegistryEndpoint is the account's ECR registry in the session's region.
func (s *Session) RegistryEndpoint() string {
	return "https://" + s.account + ".dkr.ecr." + s.region + ".amazonaws.com"
}

// RegistryPolicy is the session policy a scoped ECR login carries: an
// authorization token, and pull, or pull and push, on the repositories
// under prefix only. A token minted under it can't reach any other
// repository, whatever the connection's own permissions.
func (s *Session) RegistryPolicy(prefix string, push bool) (string, error) {
	if !repositoryPrefix.MatchString(prefix) || len(prefix) > 200 {
		return "", fmt.Errorf("%q isn't an ECR repository prefix", prefix)
	}
	actions := pullActions
	if push {
		actions = pushActions
	}
	arn := "arn:aws:ecr:" + s.region + ":" + s.account + ":repository/" + prefix
	doc := map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{
			{"Effect": "Allow", "Action": "ecr:GetAuthorizationToken", "Resource": "*"},
			{"Effect": "Allow", "Action": actions, "Resource": []string{arn, arn + "/*"}},
		},
	}
	raw, err := json.Marshal(doc)
	return string(raw), err
}

// ScopedRegistryLogin mints a Docker login for the account's ECR registry
// that reaches only the repositories under prefix: pull, or pull and push.
// A static key gets federated credentials under the session policy; a
// keyless connection assumes its role with it. Either way the login lasts
// at most twelve hours, as ECR's do.
func (s *Session) ScopedRegistryLogin(ctx context.Context, prefix string, push bool) (user, password string, err error) {
	policy, err := s.RegistryPolicy(prefix, push)
	if err != nil {
		return "", "", err
	}
	if s.account == "" {
		return "", "", errors.New("the AWS connection's account isn't known; check it again")
	}
	cfg := s.cfg.Copy()
	if s.key.Keyless() {
		cfg.Credentials = sdk.NewCredentialsCache(s.c.webIdentityWithPolicy(s.key, s.region, policy))
	} else {
		out, ferr := sts.NewFromConfig(s.cfg).GetFederationToken(ctx, &sts.GetFederationTokenInput{
			Name: sdk.String("loomplane-registry"), Policy: sdk.String(policy), DurationSeconds: sdk.Int32(3600),
		})
		if ferr != nil {
			return "", "", fmt.Errorf("scope the registry login: %w", ferr)
		}
		c := out.Credentials
		cfg.Credentials = sdk.NewCredentialsCache(credentials.NewStaticCredentialsProvider(sdk.ToString(c.AccessKeyId), sdk.ToString(c.SecretAccessKey), sdk.ToString(c.SessionToken)))
	}
	tok, err := ecr.NewFromConfig(cfg).GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	if err != nil {
		return "", "", fmt.Errorf("get an ECR authorization token: %w", err)
	}
	if len(tok.AuthorizationData) == 0 {
		return "", "", errors.New("ECR returned no authorization token")
	}
	raw, err := base64.StdEncoding.DecodeString(sdk.ToString(tok.AuthorizationData[0].AuthorizationToken))
	if err != nil {
		return "", "", fmt.Errorf("read the ECR authorization token: %w", err)
	}
	user, password, ok := strings.Cut(string(raw), ":")
	if !ok || user == "" || password == "" {
		return "", "", errors.New("ECR returned an unreadable authorization token")
	}
	return user, password, nil
}

// webIdentityWithPolicy is webIdentity with a session policy, which the
// credentials it assumes can't exceed.
func (c *Client) webIdentityWithPolicy(key *Key, region, policy string) sdk.CredentialsProvider {
	cfg := sdk.Config{Region: region, Credentials: sdk.AnonymousCredentials{}, HTTPClient: c.newHTTPClient()}
	if c.endpoint != "" {
		cfg.BaseEndpoint = sdk.String(c.endpoint)
	}
	return stscreds.NewWebIdentityRoleProvider(sts.NewFromConfig(cfg), key.RoleARN, retriever{minter: c.minter, handout: *key.handout},
		func(o *stscreds.WebIdentityRoleOptions) {
			o.RoleSessionName = sessionName(key.handout.Subject)
			o.Policy = sdk.String(policy)
		})
}
