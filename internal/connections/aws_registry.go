package connections

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/security"
)

// MethodAWSRegistry is an ECR registry whose credential is minted from an
// AWS connection for each check and deployment. Every login is narrowed by
// a session policy to the repositories under the connection's prefix.
const MethodAWSRegistry = "aws_registry"

// awsRegistrySource is all an ECR registry connection stores: the AWS
// connection it mints from, that connection's identity when it was made,
// the region and the repository prefix.
type awsRegistrySource struct {
	Kind       string
	Connection string
	Identity   string
	Region     string
	Prefix     string
}

func (s *Service) awsAccount(ctx context.Context, org, id string) (*aws.Client, *Connection, []byte, error) {
	c, err := s.store.GetConnection(ctx, org, id)
	if err != nil {
		return nil, nil, nil, err
	}
	if c.Cloud != AWS {
		return nil, nil, nil, fmt.Errorf("%w: choose an AWS account", ErrInvalid)
	}
	checker, ok := s.checkers[AWS].(AWSChecker)
	if !ok || checker.Client == nil {
		return nil, nil, nil, ErrUnavailable
	}
	c, raw, err := s.Credential(ctx, org, id)
	return checker.Client, c, raw, err
}

// ecrLogin mints a scoped login for source's repositories, and says where
// the registry is.
func (s *Service) ecrLogin(ctx context.Context, org string, source awsRegistrySource) (login []byte, endpoint string, err error) {
	client, c, raw, err := s.awsAccount(ctx, org, source.Connection)
	if err != nil {
		return nil, "", err
	}
	if source.Identity == "" || AccountIdentity(c.Cloud, c.Account, c.Details) != source.Identity {
		return nil, "", ErrChanged
	}
	key, err := aws.ParseKey(raw)
	if err != nil {
		return nil, "", err
	}
	session := client.Session(key, source.Region, c.Details["account_id"])
	user, password, err := session.ScopedRegistryLogin(ctx, source.Prefix, true)
	if err != nil {
		return nil, "", fmt.Errorf("%w: the AWS account couldn't mint a registry login: %w", ErrInvalid, err)
	}
	login, err = json.Marshal(map[string]string{"username": user, "password": password})
	return login, session.RegistryEndpoint(), err
}

// createECRRegistry makes a registry connection for the ECR repositories
// under a prefix of an AWS connection's account.
func (s *Service) createECRRegistry(ctx context.Context, p security.Principal, in CreateInput, parent *Connection) (*Connection, error) {
	region := in.Settings["region"]
	if err := aws.CheckRegion(region); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	source := awsRegistrySource{Kind: "ecr", Connection: parent.ID, Identity: AccountIdentity(parent.Cloud, parent.Account, parent.Details), Region: region, Prefix: in.Settings["repository"]}
	loginCtx, cancel := context.WithTimeout(ctx, accountRegistryLoginTimeout)
	defer cancel()
	login, endpoint, err := s.ecrLogin(loginCtx, p.OrgID, source)
	if err != nil {
		return nil, err
	}
	ref, err := json.Marshal(source)
	if err != nil {
		return nil, err
	}
	settings := map[string]string{"repository": source.Prefix, "endpoint": endpoint, "region": region}
	return s.create(ctx, p, "", Registry, in.Name, ref, login, settings, MethodAWSRegistry, "")
}

// awsRegistryCredential mints a fresh scoped login for an ECR registry
// connection, after checking its stored source still matches it.
func (s *Service) awsRegistryCredential(ctx context.Context, c *Connection) ([]byte, error) {
	ref, err := s.secrets.Reveal(ctx, c.OrgID, c.ID)
	if err != nil {
		return nil, err
	}
	var source awsRegistrySource
	if json.Unmarshal(ref, &source) != nil || source.Kind != "ecr" || c.Cloud != Registry || c.Details["repository"] != source.Prefix || c.Details["region"] != source.Region {
		return nil, ErrChanged
	}
	login, endpoint, err := s.ecrLogin(ctx, c.OrgID, source)
	if err != nil {
		return nil, err
	}
	if c.Details["endpoint"] != endpoint {
		return nil, ErrChanged
	}
	return login, nil
}
