package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/security"
)

const MethodDigitalOceanRegistry = "digitalocean_registry"

type DigitalOceanRegistryNames struct {
	Names []string `json:"names"`
}

// Only this reference is stored. Each check and deployment obtains a short-lived
// registry login from the original account after checking its current identity.
type digitalOceanRegistrySource struct {
	Connection string
	Identity   string
	Registry   string
}

func (s *Service) digitalOceanAccount(ctx context.Context, org, id string) (*digitalocean.Client, *Connection, []byte, error) {
	c, err := s.store.GetConnection(ctx, org, id)
	if err != nil {
		return nil, nil, nil, err
	}
	if c.Cloud != DigitalOcean {
		return nil, nil, nil, fmt.Errorf("%w: choose a DigitalOcean account", ErrInvalid)
	}
	checker, ok := s.checkers[DigitalOcean].(DigitalOceanChecker)
	if !ok || checker.Client == nil {
		return nil, nil, nil, ErrUnavailable
	}
	c, token, err := s.Credential(ctx, org, id)
	return checker.Client, c, token, err
}

func (s *Service) DigitalOceanRegistries(ctx context.Context, p security.Principal, id string) (DigitalOceanRegistryNames, error) {
	if err := s.authz.Require(ctx, p, security.CloudAccountManage, ""); err != nil {
		return DigitalOceanRegistryNames{}, err
	}
	client, _, token, err := s.digitalOceanAccount(ctx, p.OrgID, id)
	if err != nil {
		return DigitalOceanRegistryNames{}, err
	}
	names, err := client.RegistryNames(ctx, string(token))
	return DigitalOceanRegistryNames{Names: names}, registryAccessError(err)
}

func registryAccessError(err error) error {
	if err == nil {
		return nil
	}
	if digitalocean.IsNotFound(err) {
		return fmt.Errorf("%w: the DigitalOcean registry no longer exists in this account", ErrInvalid)
	}
	if errors.Is(err, digitalocean.ErrRejected) {
		return fmt.Errorf("%w: grant registry access to the DigitalOcean account and check it again", ErrInvalid)
	}
	return fmt.Errorf("%w: DigitalOcean could not supply registry access", ErrUnavailable)
}

func (s *Service) registryLogin(ctx context.Context, org string, source digitalOceanRegistrySource) ([]byte, error) {
	client, c, token, err := s.digitalOceanAccount(ctx, org, source.Connection)
	if err != nil {
		return nil, err
	}
	if source.Identity == "" || AccountIdentity(c.Cloud, c.Account, c.Details) != source.Identity {
		return nil, ErrChanged
	}
	user, password, err := client.RegistryLogin(ctx, string(token), source.Registry)
	if err != nil {
		return nil, registryAccessError(err)
	}
	return json.Marshal(map[string]string{"username": user, "password": password})
}

func (s *Service) createAccountRegistry(ctx context.Context, p security.Principal, in CreateInput) (*Connection, error) {
	if in.Cloud != Registry || in.Credential != "" {
		return nil, fmt.Errorf("%w: choose a registry and a connected account", ErrInvalid)
	}
	parent, err := s.store.GetConnection(ctx, p.OrgID, in.RegistryAccountID)
	if err != nil {
		return nil, err
	}
	if parent.Cloud != DigitalOcean {
		return nil, ErrInvalid
	}
	source := digitalOceanRegistrySource{Connection: parent.ID, Identity: AccountIdentity(parent.Cloud, parent.Account, parent.Details), Registry: in.Settings["repository"]}
	login, err := s.registryLogin(ctx, p.OrgID, source)
	if err != nil {
		return nil, err
	}
	ref, err := json.Marshal(source)
	if err != nil {
		return nil, err
	}
	// A provider login can only be sent to its canonical registry and token service.
	settings := map[string]string{"repository": source.Registry, "endpoint": "https://" + digitalocean.RegistryHost, "auth_endpoint": digitalocean.RegistryAuthEndpoint}
	return s.create(ctx, p, "", Registry, in.Name, ref, login, settings, MethodDigitalOceanRegistry, "")
}

func (s *Service) accountRegistryCredential(ctx context.Context, c *Connection) ([]byte, error) {
	ref, err := s.secrets.Reveal(ctx, c.OrgID, c.ID)
	if err != nil {
		return nil, err
	}
	var source digitalOceanRegistrySource
	if json.Unmarshal(ref, &source) != nil || c.Cloud != Registry || c.Details["repository"] != source.Registry || c.Details["endpoint"] != "https://"+digitalocean.RegistryHost || c.Details["auth_endpoint"] != digitalocean.RegistryAuthEndpoint {
		return nil, ErrChanged
	}
	return s.registryLogin(ctx, c.OrgID, source)
}
