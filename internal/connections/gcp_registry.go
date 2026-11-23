package connections

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/security"
)

// MethodGCPRegistry is an Artifact Registry repository whose credential is
// a short-lived access token minted from a Google Cloud connection for each
// check and deployment. Nothing long-lived is stored.
const MethodGCPRegistry = "gcp_registry"

// artifactRepository is project/repository in Artifact Registry.
var artifactRepository = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]/[a-z][a-z0-9-]{0,62}$`)

type gcpRegistrySource struct {
	Kind       string
	Connection string
	Identity   string
	Location   string
	Repository string
}

func (s *Service) gcpLogin(ctx context.Context, org string, source gcpRegistrySource) ([]byte, error) {
	c, err := s.store.GetConnection(ctx, org, source.Connection)
	if err != nil {
		return nil, err
	}
	if c.Cloud != GCP {
		return nil, fmt.Errorf("%w: choose a Google Cloud account", ErrInvalid)
	}
	checker, ok := s.checkers[GCP].(GCPChecker)
	if !ok || checker.Client == nil {
		return nil, ErrUnavailable
	}
	c, raw, err := s.Credential(ctx, org, source.Connection)
	if err != nil {
		return nil, err
	}
	if source.Identity == "" || AccountIdentity(c.Cloud, c.Account, c.Details) != source.Identity {
		return nil, ErrChanged
	}
	key, err := gcp.ParseKey(raw)
	if err != nil {
		return nil, err
	}
	session, err := checker.Client.Session(ctx, key)
	if err != nil {
		return nil, err
	}
	user, password, err := session.RegistryLogin(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: the Google Cloud account couldn't mint a registry login: %w", ErrInvalid, err)
	}
	return json.Marshal(map[string]string{"username": user, "password": password})
}

// createArtifactRegistry makes a registry connection for one Artifact
// Registry repository of a Google Cloud connection.
func (s *Service) createArtifactRegistry(ctx context.Context, p security.Principal, in CreateInput, parent *Connection) (*Connection, error) {
	endpoint, err := gcp.RegistryEndpoint(in.Settings["location"])
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if !artifactRepository.MatchString(in.Settings["repository"]) {
		return nil, fmt.Errorf("%w: name the repository as project/repository", ErrInvalid)
	}
	source := gcpRegistrySource{Kind: "artifact", Connection: parent.ID, Identity: AccountIdentity(parent.Cloud, parent.Account, parent.Details), Location: in.Settings["location"], Repository: in.Settings["repository"]}
	loginCtx, cancel := context.WithTimeout(ctx, accountRegistryLoginTimeout)
	defer cancel()
	login, err := s.gcpLogin(loginCtx, p.OrgID, source)
	if err != nil {
		return nil, err
	}
	ref, err := json.Marshal(source)
	if err != nil {
		return nil, err
	}
	settings := map[string]string{"repository": source.Repository, "endpoint": endpoint, "location": source.Location}
	return s.create(ctx, p, "", Registry, in.Name, ref, login, settings, MethodGCPRegistry, "")
}

func (s *Service) gcpRegistryCredential(ctx context.Context, c *Connection) ([]byte, error) {
	ref, err := s.secrets.Reveal(ctx, c.OrgID, c.ID)
	if err != nil {
		return nil, err
	}
	var source gcpRegistrySource
	if json.Unmarshal(ref, &source) != nil || source.Kind != "artifact" || c.Cloud != Registry || c.Details["repository"] != source.Repository || c.Details["location"] != source.Location {
		return nil, ErrChanged
	}
	if endpoint, eerr := gcp.RegistryEndpoint(source.Location); eerr != nil || c.Details["endpoint"] != endpoint {
		return nil, ErrChanged
	}
	return s.gcpLogin(ctx, c.OrgID, source)
}
