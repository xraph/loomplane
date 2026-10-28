package planning

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/manifest"
)

// CatalogInputs is what a catalog kind renders into its components from
// outside the manifest, such as settings kept elsewhere in Loomplane, as
// a digest. A plan records each component's digest, so a change to what
// the kind reads plans an update of that component.
type CatalogInputs interface {
	// Digest returns, by component name, a digest of what the kind renders
	// into its components from outside the manifest. r is the resource as
	// the plan's own manifest revision has it, which may not be the
	// project's latest. It holds no secret, and neither does its error: the
	// error's text reaches the plan's failure reason and the API.
	Digest(ctx context.Context, orgID, projectID, environmentID, resource string, r manifest.Resource) (map[string]string, error)
}

// InputsProblem is a CatalogInputs error about the resource itself that
// says what to change, such as settings naming something the kind no
// longer offers. Its text is shown as it is: as the plan's failure reason,
// and as why an approval found the plan stale.
type InputsProblem interface {
	error
	InputsProblem()
}

// CatalogInputMap holds each kind's CatalogInputs, by kind name. The
// planning extension provides it through Forge DI, and an extension adds
// its kinds' entries during its own Register, never later.
type CatalogInputMap map[string]CatalogInputs

// catalogInputsError is a CatalogInputs that couldn't answer. It fails
// the plan, since planning without it could keep a component that has
// to change.
type catalogInputsError struct {
	resource string
	err      error
}

func (e *catalogInputsError) Error() string {
	var problem InputsProblem
	if errors.As(e.err, &problem) {
		return problem.Error()
	}
	return fmt.Sprintf("couldn't read what %s is configured with: %v", e.resource, e.err)
}

func (e *catalogInputsError) Unwrap() error { return e.err }

// featureOn reports whether a kind of the feature may be planned: a kind
// without a feature always may, and nil features have every feature off.
func featureOn(features map[string]bool, feature string) bool {
	return feature == "" || features[feature]
}

// featureOff is the problem of a kind whose feature is off on the server.
// A feature is named in capitals, as "AI", and turned on with
// <feature>.enabled in the server's config.
func featureOff(name, path string, kind recipes.Kind) Finding {
	return Finding{
		Code: "catalog-feature-off", Subject: ResourceSubject, Name: name, Path: path,
		Message: fmt.Sprintf("%s needs %s, which is off on this Loomplane server. An operator turns it on with %s.enabled.", kind.Title, strings.ToUpper(kind.Feature), kind.Feature),
	}
}

// catalogInputs asks each named composed resource's kind, when it has a
// CatalogInputs and its feature is on, for the digests of what it renders
// from outside the manifest, by resource and then component.
func (s *Service) catalogInputs(ctx context.Context, plan *Plan, m *manifest.Manifest, composed []string) (map[string]map[string]string, error) {
	if len(s.inputs) == 0 {
		return nil, nil
	}
	kinds := s.kinds
	if kinds == nil {
		kinds = recipes.Default()
	}
	out := map[string]map[string]string{}
	for _, name := range composed {
		kind, ok := kinds.Kind(m.Resources[name].Kind)
		if !ok || !featureOn(s.features, kind.Feature) {
			continue
		}
		source := s.inputs[kind.Name]
		if source == nil {
			continue
		}
		digests, err := source.Digest(ctx, plan.OrgID, plan.ProjectID, plan.EnvironmentID, name, m.Resources[name])
		if err != nil {
			return nil, &catalogInputsError{resource: name, err: err}
		}
		if len(digests) > 0 {
			out[name] = digests
		}
	}
	return out, nil
}

// failsPlanning reports an error that fails the plan instead of being
// tried again. A read that was cancelled or ran out of time is tried
// again, so only a CatalogInputs that answered with an error fails it.
func failsPlanning(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var inputs *catalogInputsError
	return errors.As(err, &inputs)
}

// unreadableInputs is why an approval settles a plan stale when what a
// resource is configured with couldn't be read: planning again may read it.
// An InputsProblem says itself what to change.
func unreadableInputs(err error) string {
	var problem InputsProblem
	if errors.As(err, &problem) {
		return problem.Error() + "."
	}
	var inputs *catalogInputsError
	if !errors.As(err, &inputs) {
		return err.Error()
	}
	return fmt.Sprintf("Couldn't check what %s is configured with. Plan again.", inputs.resource)
}
