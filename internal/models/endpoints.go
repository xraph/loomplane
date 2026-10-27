package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/xraph/loomplane/internal/models/registry"
)

// Gateways is told when the set of models a project's gateways should
// route to may have changed. W1 sets the real one; until then it is
// noGateways{}. It returns nothing: a gateway that can't follow must never
// fail a model's deployment.
type Gateways interface {
	ModelsChanged(ctx context.Context, orgID, projectID string)
}

// noGateways is the Gateways of a Loomplane without the AI gateway.
type noGateways struct{}

func (noGateways) ModelsChanged(context.Context, string, string) {}

// Placement is where a caller of a model runs: what reach is checked
// against.
type Placement struct {
	EnvironmentID, ConnectionID, Provider string // provider "server" or "kubernetes"
	// SharedNetworks are the project networks the caller has joined
	// (Docker). Nothing fills it yet: until models share a network, a
	// caller reaches only the models of its own environment.
	SharedNetworks []string
}

// Endpoint is one deployed model as a caller at a Placement reaches it.
type Endpoint struct {
	Deployment, Resource, EnvironmentID string
	// Provider is ProviderName(Resource).
	Provider string
	// BaseURL is the guard's URL and "/v1".
	BaseURL                              string
	APIKey                               string
	ServedName                           string
	Task                                 registry.Task
	ContextWindow, MaxOutput, Dimensions int
	// Alias is the entry's default alias.
	Alias string
}

// Skipped is a deployed model a caller can't reach, and why.
type Skipped struct{ Resource, EnvironmentID, Reason string }

// maxProviderName is the longest provider name a gateway takes.
const maxProviderName = 63

// ProviderName is the gateway provider name of a model resource:
// "model-" and the resource. One longer than 63 characters is cut, and
// ends with "-" and 6 hex of its hash, so two long names that share a
// prefix stay apart.
func ProviderName(resource string) string {
	name := "model-" + resource
	if len(name) <= maxProviderName {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	suffix := "-" + hex.EncodeToString(sum[:])[:6]
	return name[:maxProviderName-len(suffix)] + suffix
}
