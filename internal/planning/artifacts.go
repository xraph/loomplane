package planning

import (
	"context"
	"regexp"
	"slices"
	"strings"
)

// digestReference is an image named by its digest: repository@sha256:<64 hex>.
var digestReference = regexp.MustCompile(`@sha256:[a-f0-9]{64}$`)

// commitBuilders build every deployment from a commit themselves, so there
// is no image of an earlier release to deploy again.
var commitBuilders = map[string]string{
	"render": "Render builds each deployment from the commit itself",
	"vercel": "Vercel builds each deployment from the commit itself",
}

// pullAccounts says, for a target that pulls the images Loomplane builds
// from its connection's own registry, what another connection or region
// is to people. Azure Container Apps pulls from the connection's container
// registry whatever the region.
var pullAccounts = map[string]struct {
	what   string
	region bool
}{
	"ecs-fargate":          {"AWS account or region", true},
	"cloud-run":            {"Google Cloud project or region", true},
	"azure-container-apps": {"Azure container registry", false},
}

// releasedImages reads what the From environment of a promotion ran, once
// per compute, so recomputing an approved plan sees the same images unless
// that environment released again since. ok is false when the plan reuses
// nothing: it isn't a promotion, or no history is wired.
func (s *Service) releasedImages(ctx context.Context, plan *Plan) (images map[string]ReleasedImage, ok bool, err error) {
	if s.history == nil || plan.Kind != KindPromotion || plan.FromEnvironmentID == "" {
		return nil, false, nil
	}
	images, err = s.history.ReleasedImages(ctx, plan.OrgID, plan.FromEnvironmentID, s.now().UTC())
	return images, err == nil, err
}

// applyArtifacts decides, for each service the plan deploys, whether it
// deploys the image from released (what `from` ran) or builds again, sets
// Artifact on those it reuses and lists every choice with its reason.
func (s *Service) applyArtifacts(plan *Plan, result *Result, released map[string]ReleasedImage, from string) {
	choices := []ArtifactChoice{}
	for i, a := range result.Actions {
		if !deploysBuiltCode(a) {
			continue
		}
		choice, artifact := s.reusable(plan.Environment, result.Connections, a, released[a.Name], from)
		if artifact != nil {
			cfg := *a.Service
			cfg.Artifact = artifact
			result.Actions[i].Service = &cfg
		}
		choices = append(choices, choice)
	}
	slices.SortFunc(choices, func(a, b ArtifactChoice) int { return strings.Compare(a.Service, b.Service) })
	result.Artifacts = choices
}

// deploysBuiltCode reports whether an action deploys a service built from
// a repository: the only kind of action an earlier image can stand in for.
// A gateway is generated, and a catalog service or component runs an image
// Loomplane didn't build, as does a service pinned to an existing image.
func deploysBuiltCode(a Action) bool {
	if a.Subject != ServiceSubject || a.Service == nil {
		return false
	}
	if a.Op != Create && a.Op != Update && a.Op != Replace {
		return false
	}
	cfg := a.Service
	if len(cfg.Gateway) > 0 || cfg.Catalog != nil || cfg.Component != nil {
		return false
	}
	return cfg.BuildTarget == nil || cfg.BuildTarget.Image == ""
}

// reusable decides whether action a of the plan for environment `to`
// deploys img, the image `from` ran, instead of building its commit again,
// and says why not when it can't. connections are the plan's connections
// by cloud, which name an action's connection when it pins none. img is
// the zero value when `from` ran no release of the service.
func (s *Service) reusable(to string, connections map[string]string, a Action, img ReleasedImage, from string) (ArtifactChoice, *ReusedArtifact) {
	cfg := a.Service
	choice := ArtifactChoice{Service: a.Name}
	short := shortCommit(cfg.CommitSHA)
	again := "; " + to + " builds " + short + " again"
	if reason, ok := commitBuilders[a.Provider]; ok {
		choice.Reason = reason
		return choice, nil
	}
	if a.Provider == "digitalocean" {
		choice.Reason = "App Platform builds the newest commit of its branch; this promotion works only while that is still " + short
		return choice, nil
	}
	switch {
	case img.Service == "":
		choice.Reason = from + " runs no release of " + a.Name + again
		return choice, nil
	case img.CommitSHA != cfg.CommitSHA:
		choice.Reason = from + " ran " + a.Name + " at " + shortCommit(img.CommitSHA) + again
		return choice, nil
	case img.Provider != a.Provider:
		choice.Reason = to + " runs " + a.Name + " on " + s.providerTitle(a.Provider) + ", " + from + " on " + s.providerTitle(img.Provider)
		return choice, nil
	}
	pull, pulls := pullAccounts[a.Provider]
	ownsRegistry := a.Provider == "server" || a.Provider == "kubernetes"
	if !pulls && !ownsRegistry {
		choice.Reason = s.providerTitle(a.Provider) + " builds each deployment itself" + again
		return choice, nil
	}
	if ownsRegistry && img.Registry == "" {
		choice.Reason = from + " built the image on its server without a registry" + again
		return choice, nil
	}
	// A release records a tag when it predates digest-pinned images, or
	// when its connection couldn't read the digest its build pushed.
	if !digestReference.MatchString(img.Image) {
		choice.Reason = from + " recorded " + a.Name + "'s image by tag: its release predates digest-pinned images, or its connection can't read image digests" + again
		return choice, nil
	}
	if ownsRegistry {
		registry := ""
		if cfg.BuildTarget != nil {
			registry = cfg.BuildTarget.Registry
		}
		if registry != img.Registry {
			choice.Reason = to + " pushes to another registry" + again
			return choice, nil
		}
	} else if s.actionConnection(connections, a) != img.ConnectionID || (pull.region && a.Region != img.Region) {
		choice.Reason = from + "'s image is in another " + pull.what + ", which " + to + " can't pull from" + again
		return choice, nil
	}
	_, digest, _ := strings.Cut(img.Image, "@")
	choice.Reused, choice.Reference, choice.From = true, img.Image, from
	return choice, &ReusedArtifact{
		Reference: img.Image, Digest: digest, CommitSHA: img.CommitSHA,
		FromEnvironment: from, FromDeploymentID: img.DeploymentID,
	}
}

// actionConnection is the connection an action deploys with: the one it
// pins, or the plan's for its provider's cloud, as the runner chooses.
func (s *Service) actionConnection(connections map[string]string, a Action) string {
	if a.ConnectionID != "" {
		return a.ConnectionID
	}
	p, err := s.providers.Get(a.Provider)
	if err != nil {
		return ""
	}
	return connections[p.Cloud]
}

// providerTitle names a provider for people.
func (s *Service) providerTitle(name string) string {
	if p, err := s.providers.Get(name); err == nil && p.Title != "" {
		return p.Title
	}
	return name
}

func shortCommit(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
