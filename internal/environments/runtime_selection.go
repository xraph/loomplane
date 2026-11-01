package environments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

var ErrSourceSelection = errors.New("the exact runtime variable selection is unavailable")

// RuntimeSourcePin is private execution metadata. It is never a public plan DTO.
// Plain values are pinned by source revision; secret values by stored version.
type RuntimeSourcePin struct {
	VariableID     string        `json:"variable_id"`
	Key            string        `json:"key"`
	Scope          string        `json:"scope"`
	ProjectID      string        `json:"project_id"`
	Environment    string        `json:"environment"`
	Service        string        `json:"service"`
	SourceRevision int64         `json:"source_revision"`
	Secret         bool          `json:"secret"`
	Version        SecretVersion `json:"version"`
}

// RuntimeSelection pins the resolved winners, including an empty selection.
// Its revision hashes metadata only. Possessing it grants no execution authority.
type RuntimeSelection struct {
	SchemaVersion int                `json:"schema_version"`
	OrgID         string             `json:"org_id"`
	ProjectID     string             `json:"project_id"`
	EnvironmentID string             `json:"environment_id"`
	Environment   string             `json:"environment"`
	Service       string             `json:"service"`
	Revision      string             `json:"revision"`
	Sources       []RuntimeSourcePin `json:"sources"`
}

func selectionRevision(selection RuntimeSelection) (string, error) {
	selection.Revision = ""
	raw, err := json.Marshal(selection)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func sourcePin(v *Variable) RuntimeSourcePin {
	return RuntimeSourcePin{VariableID: v.ID, Key: v.Key, Scope: v.Scope, ProjectID: v.ProjectID, Environment: v.Environment, Service: v.Service, SourceRevision: v.sourceRevision, Secret: v.Secret, Version: SecretVersion{Identity: v.secretIdentity, Version: v.secretVersion}}
}

// SelectRuntime uses the same six-layer resolver as ordinary environments. It
// enrolls legacy secret metadata under the source gate without revealing values.
func (s *Service) SelectRuntime(ctx context.Context, p security.Principal, project, environment, service string) (RuntimeSelection, error) {
	var out RuntimeSelection
	if _, ok := s.store.(sourceTransactions); !ok {
		return out, ErrSourceSelection
	}
	secrets, ok := s.secrets.(versionedSecrets)
	if !ok {
		return out, ErrSourceSelection
	}
	if err := s.authz.Require(ctx, p, security.VariableRead, project); err != nil {
		return out, err
	}
	if _, err := s.projects.GetProject(ctx, p.OrgID, project); err != nil {
		return out, err
	}
	if service != "" {
		if s.services == nil {
			return out, ErrSourceSelection
		}
		known, err := s.services.Services(ctx, p.OrgID, project)
		if err != nil {
			return out, err
		}
		if _, ok := known[service]; !ok {
			return out, ErrSourceSelection
		}
	}
	env, err := s.environment(ctx, p.OrgID, project, environment)
	if err != nil {
		return out, err
	}
	if env.InstanceID != "" {
		return out, projects.ErrFleetManaged
	}
	out = RuntimeSelection{SchemaVersion: 1, OrgID: p.OrgID, ProjectID: project, EnvironmentID: env.ID, Environment: env.Name, Service: service, Sources: []RuntimeSourcePin{}}
	err = s.withSourceTransaction(ctx, p.OrgID, func(ctx context.Context) error {
		if scopeErr := s.validateSourceScope(ctx, &Variable{OrgID: p.OrgID, ProjectID: env.ProjectID, environmentID: env.ID, Environment: env.Name}); scopeErr != nil {
			return scopeErr
		}
		candidates, e := s.store.ForEnvironment(ctx, p.OrgID, project, env.ID, env.Name)
		if e != nil {
			return e
		}
		for _, selected := range resolve(candidates, service) {
			v := selected.Variable
			if v.Secret {
				if e = s.enrollSource(ctx, &v); e != nil {
					return ErrSourceSelection
				}
				current, metaErr := secrets.Metadata(ctx, p.OrgID, v.ID)
				if metaErr != nil {
					return ErrSourceSelection
				}
				if current.Identity != v.secretIdentity || current.Version < v.secretVersion {
					return ErrSourceSelection
				}
			}
			out.Sources = append(out.Sources, sourcePin(&v))
		}
		out.Revision, e = selectionRevision(out)
		return e
	})
	return out, err
}

// CaptureRuntimeSelection reads only the reviewed versions. Callers must load
// selection from the accepted group and reauthorize its current execution epoch
// before calling this method. The returned maps belong in encrypted storage.
func (s *Service) CaptureRuntimeSelection(ctx context.Context, p security.Principal, selection RuntimeSelection) (values map[string]string, flags map[string]bool, err error) {
	if _, ok := s.store.(sourceTransactions); !ok {
		return nil, nil, ErrSourceSelection
	}
	if selection.SchemaVersion != 1 || selection.OrgID != p.OrgID || selection.ProjectID == "" || selection.EnvironmentID == "" || selection.Revision == "" {
		return nil, nil, ErrSourceSelection
	}
	revision, e := selectionRevision(selection)
	if e != nil || revision != selection.Revision {
		return nil, nil, ErrSourceSelection
	}
	if authErr := s.authz.Require(ctx, p, security.DeploymentApprove, selection.ProjectID); authErr != nil {
		return nil, nil, authErr
	}
	env, e := s.environment(ctx, p.OrgID, selection.ProjectID, selection.Environment)
	if e != nil {
		return nil, nil, e
	}
	if env.ID != selection.EnvironmentID {
		return nil, nil, ErrSourceSelection
	}
	if env.InstanceID != "" {
		return nil, nil, projects.ErrFleetManaged
	}
	values, flags = map[string]string{}, map[string]bool{}
	err = s.withSourceTransaction(ctx, p.OrgID, func(ctx context.Context) error {
		if scopeErr := s.validateSourceScope(ctx, &Variable{OrgID: p.OrgID, ProjectID: env.ProjectID, environmentID: env.ID, Environment: env.Name}); scopeErr != nil {
			return scopeErr
		}
		for _, pin := range selection.Sources {
			if pin.Environment != "" && pin.Environment != selection.Environment || pin.SourceRevision < 1 || pin.Key == "" {
				return ErrSourceSelection
			}
			switch pin.Scope {
			case ScopeOrganization:
				if pin.ProjectID != "" || pin.Service != "" {
					return ErrSourceSelection
				}
			case ScopeProject:
				if pin.ProjectID != selection.ProjectID || pin.Service != "" {
					return ErrSourceSelection
				}
			case ScopeService:
				if pin.ProjectID != selection.ProjectID || selection.Service == "" || pin.Service != selection.Service {
					return ErrSourceSelection
				}
			default:
				return ErrSourceSelection
			}
			if _, exists := values[pin.Key]; exists {
				return ErrSourceSelection
			}
			v, readErr := s.store.GetVariable(ctx, p.OrgID, pin.VariableID)
			if readErr != nil {
				return ErrSourceSelection
			}
			if v.Key != pin.Key || v.Scope != pin.Scope || v.ProjectID != pin.ProjectID || v.Environment != pin.Environment || v.Service != pin.Service || v.Secret != pin.Secret {
				return ErrSourceSelection
			}
			if v.Secret {
				if pin.Version.Identity == "" || pin.Version.Version < 1 || v.secretIdentity != pin.Version.Identity {
					return ErrSourceSelection
				}
				secrets, ok := s.secrets.(versionedSecrets)
				if !ok {
					return ErrSourceSelection
				}
				value, revealErr := secrets.RevealPinned(ctx, v.OrgID, v.ID, pin.Version)
				if revealErr != nil {
					return ErrSourceSelection
				}
				values[pin.Key], flags[pin.Key] = string(value), true
				clear(value)
			} else {
				if v.sourceRevision != pin.SourceRevision {
					return ErrSourceSelection
				}
				values[pin.Key] = v.Value
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return values, flags, nil
}
