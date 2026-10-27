package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/planning"
)

// appliedVariables contains user variables and resource addresses. Service
// addresses are resolved against the serving bindings each time the pinned
// service runs. GatewayValues are the variables that go with a resource
// behind the database gateway: <KEY>_GATEWAY, the gateway's address, and
// <KEY>_DIRECT, the resource's own, which GatewaySecrets marks secret.
type appliedVariables struct {
	ResourceValues map[string]string `json:"resource_values,omitempty"`
	GatewayValues  map[string]string `json:"gateway_values,omitempty"`
	GatewaySecrets map[string]bool   `json:"gateway_secrets,omitempty"`
	Values         map[string]string `json:"values"`
	Secrets        map[string]bool   `json:"secrets"`
}

// captureAppliedVariables pins what the service gets in this deployment.
// The service's planned links say whether it reaches a resource through
// the database gateway, and services, the environment's service bindings,
// say where that gateway is. resources are the environment's resource
// bindings by name, which a per-consumer output is issued from.
func (r *Runner) captureAppliedVariables(ctx context.Context, d *Deployment, a *planning.Action, services, resources map[string]*planning.Binding) error {
	if a.Service == nil || a.Service.Generated() || r.resources == nil {
		return nil
	}
	cfg := *a.Service
	a.Service = &cfg
	if cfg.AppliedInputs != nil && cfg.AppliedInputs.Variables != "" {
		return nil
	}
	store, durable := r.store.(AppliedVariableStore)
	ref := ""
	var err error
	if durable {
		ref, err = store.AppliedVariables(ctx, d.OrgID, d.ID, a.ID)
		if err != nil {
			return err
		}
	}
	if !durable {
		ref = resourceKey(d.OrgID, d.EnvironmentID, "applied-inputs/"+d.ID, a.ID)
	}
	var raw []byte
	if ref != "" {
		raw, err = r.resources.Get(ctx, ref)
	} else {
		err = ErrNoCredential
	}
	if errors.Is(err, ErrNoCredential) {
		if durable && ref != "" {
			return fmt.Errorf("applied variable snapshot is missing: %w", err)
		}
		values, secrets, verr := r.variables.Values(ctx, d.OrgID, d.ProjectID, d.Environment, cfg.Service)
		if verr != nil {
			return fmt.Errorf("resolve applied variables: %w", verr)
		}
		if durable {
			id, e := typeid.Generate("vars")
			if e != nil {
				return e
			}
			ref = resourceKey(d.OrgID, d.EnvironmentID, "applied-inputs/"+d.ID, id.String())
		}
		resourceValues, gatewayValues, gatewaySecrets := map[string]string{}, map[string]string{}, map[string]bool{}
		for _, link := range cfg.Env {
			if link.Subject != planning.ResourceSubject {
				continue
			}
			if _, overridden := values[link.Key]; overridden {
				continue
			}
			if _, consumer := consumerOutput(link); consumer {
				if e := consumerThroughGateway(a.Name, link); e != nil {
					return fmt.Errorf("capture applied resource variable: %w", e)
				}
				// Issued again on every release, and kept only here.
				value, e := r.bindConsumer(ctx, d, a.Name, link, resources)
				if e != nil {
					return e
				}
				resourceValues[link.Key] = value
				continue
			}
			value, extra, e := r.resourceAddress(ctx, d, a.Name, services, link)
			if e != nil {
				return fmt.Errorf("capture applied resource variable: %w", e)
			}
			resourceValues[link.Key] = value
			for k, x := range extra {
				gatewayValues[k] = x.Value
				if x.Secret {
					gatewaySecrets[k] = true
				}
			}
		}
		raw, err = json.Marshal(appliedVariables{Values: values, Secrets: secrets, ResourceValues: resourceValues, GatewayValues: gatewayValues, GatewaySecrets: gatewaySecrets})
		if err == nil {
			err = r.resources.Put(ctx, ref, raw)
		}
		if err != nil {
			return fmt.Errorf("capture applied variables: %w", err)
		}
		if durable {
			winner, e := store.ClaimAppliedVariables(ctx, d.OrgID, d.ID, a.ID, ref)
			if e != nil {
				return e
			}
			if winner != ref {
				_ = r.resources.Delete(ctx, ref) //nolint:errcheck // best effort: the claimed winner is what deploys, a stray copy only costs storage
				ref = winner
				raw, err = r.resources.Get(ctx, ref)
			}
		}
	}
	if err != nil {
		return fmt.Errorf("capture applied variables: %w", err)
	}
	var stored appliedVariables
	if err = json.Unmarshal(raw, &stored); err != nil {
		return fmt.Errorf("read applied variables: %w", err)
	}
	inputs := planning.AppliedInputs{}
	if cfg.AppliedInputs != nil {
		inputs = *cfg.AppliedInputs
	}
	inputs.Variables = ref
	cfg.AppliedInputs = &inputs
	return nil
}

func (r *Runner) appliedSource(ctx context.Context, d *Deployment, a planning.Action) (io.ReadCloser, int64, error) {
	if a.Service.AppliedInputs != nil && a.Service.AppliedInputs.ArtifactKey != "" {
		pin := a.Service.AppliedInputs
		if pin.Source == nil || pin.Source.CommitSHA != a.Service.CommitSHA || pin.Source.Alias != a.Service.Repo {
			return nil, 0, errors.New("applied source does not match the service revision")
		}
		return r.sources.Open(ctx, pin.ArtifactKey)
	}
	key, err := sourceFor(d, a.Service.Repo)
	if err != nil {
		return nil, 0, err
	}
	return r.sources.Open(ctx, key)
}

func pinAppliedSource(d *Deployment, p *planning.Plan, a *planning.Action) {
	if a.Service == nil || a.Service.Generated() {
		return
	}
	cfg := *a.Service
	inputs := planning.AppliedInputs{}
	if cfg.AppliedInputs != nil {
		inputs = *cfg.AppliedInputs
	}
	if inputs.ArtifactKey != "" {
		return
	}
	for _, src := range p.Sources {
		if src.Alias != cfg.Repo {
			continue
		}
		for _, artifact := range d.Sources {
			if artifact.Alias == src.Alias && artifact.CommitSHA == src.CommitSHA && artifact.Key != "" {
				inputs.Source = &src
				inputs.ArtifactKey = artifact.Key
			}
		}
	}
	cfg.AppliedInputs = &inputs
	a.Service = &cfg
}
