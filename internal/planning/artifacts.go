package planning

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"
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

// BuildInputs are what a service's image is built from, besides its
// commit: two configs with equal inputs build the same image.
type BuildInputs struct {
	Source    string
	Image     string
	Context   string
	Ecosystem string
	Build     string
	Start     string
}

// buildInputNames name each input for people, in the order they're compared.
var buildInputNames = []struct {
	name  string
	value func(BuildInputs) string
}{
	{"source", func(b BuildInputs) string { return b.Source }},
	{"dockerfile", func(b BuildInputs) string { return b.Image }},
	{"build.context", func(b BuildInputs) string { return b.Context }},
	{"ecosystem", func(b BuildInputs) string { return b.Ecosystem }},
	{"build command", func(b BuildInputs) string { return b.Build }},
	{"start command", func(b BuildInputs) string { return b.Start }},
}

// BuildInputs are what c's image is built from.
func (c *ServiceConfig) BuildInputs() BuildInputs {
	return BuildInputs{Source: c.Source, Image: c.Image, Context: c.Context, Ecosystem: string(c.Ecosystem), Build: c.Build, Start: c.Start}
}

// BuildArchitecture is the architecture c's image is built for: its
// builder's, else its target's. It is "" for a cloud that builds for
// itself.
func (c *ServiceConfig) BuildArchitecture() string {
	if c.BuildTarget != nil && c.BuildTarget.BuilderSettings["architecture"] != "" {
		return c.BuildTarget.BuilderSettings["architecture"]
	}
	return c.TargetSettings["architecture"]
}

// runArchitecture is the architecture c runs on: its target's, else its
// builder's.
func (c *ServiceConfig) runArchitecture() string {
	if a := c.TargetSettings["architecture"]; a != "" {
		return a
	}
	if c.BuildTarget != nil {
		return c.BuildTarget.BuilderSettings["architecture"]
	}
	return ""
}

// releasedImages reads, once per compute, what a plan can deploy again
// and the environment that ran it: for a promotion, what its From
// environment runs, so recomputing an approved plan sees the same images
// unless that environment released again since; for a rollback, what its
// own environment ran after the release it restores. ok is false when
// the plan reuses nothing: it's neither, or no history is wired.
func (s *Service) releasedImages(ctx context.Context, plan *Plan) (images map[string]ReleasedImage, from string, ok bool, err error) {
	if s.history == nil {
		return nil, "", false, nil
	}
	var environmentID string
	var upTo time.Time
	switch {
	case plan.Kind == KindPromotion && plan.FromEnvironmentID != "":
		environmentID, upTo, from = plan.FromEnvironmentID, s.now().UTC(), plan.From
	case plan.Kind == KindRollback && plan.Rollback != nil && !plan.Rollback.RestoreFinishedAt.IsZero():
		environmentID, upTo, from = plan.EnvironmentID, plan.Rollback.RestoreFinishedAt, plan.Environment
	default:
		return nil, "", false, nil
	}
	images, err = s.history.ReleasedImages(ctx, plan.OrgID, environmentID, upTo)
	return images, from, err == nil, err
}

// applyArtifacts decides, for each service the plan deploys, whether it
// deploys the image from released (what `from` ran) or builds again, sets
// Artifact on those it reuses and lists every choice with its reason. A
// rollback of a DigitalOcean service that can't return to the deployment
// the restored release ran is a problem.
func (s *Service) applyArtifacts(plan *Plan, result *Result, released map[string]ReleasedImage, from string) {
	choices := []ArtifactChoice{}
	for i, a := range result.Actions {
		if !deploysBuiltCode(a) {
			continue
		}
		var choice ArtifactChoice
		var artifact *ReusedArtifact
		if plan.Kind == KindRollback && a.Provider == "digitalocean" {
			var problem *Finding
			if choice, artifact, problem = appPlatformRollback(a, released[a.Name], from); problem != nil {
				result.Problems = append(result.Problems, *problem)
				result.Executable = false
			}
		} else {
			choice, artifact = s.reusable(plan.Environment, result.Connections, a, released[a.Name], from)
		}
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
	case img.Architecture != cfg.runArchitecture():
		choice.Reason = from + " built " + a.Name + " for " + architecture(img.Architecture) + "; " + to + " runs " + architecture(cfg.runArchitecture())
		return choice, nil
	}
	if differs := changedInput(img.Inputs, cfg.BuildInputs()); differs != "" {
		choice.Reason = to + " builds " + a.Name + " differently from " + from + " (" + differs + ")"
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
		registry := cfg.ImageRegistry()
		if registry != img.Registry {
			choice.Reason = to + " pushes to another registry" + again
			return choice, nil
		}
	} else if connection := s.actionConnection(connections, a); connection == "" || img.ConnectionID == "" {
		choice.Reason = "Loomplane can't tell which " + pull.what + " " + from + "'s image is in" + again
		return choice, nil
	} else if connection != img.ConnectionID || (pull.region && a.Region != img.Region) {
		choice.Reason = from + "'s image is in another " + pull.what + ", which " + to + " can't pull from" + again
		return choice, nil
	}
	_, digest, _ := strings.Cut(img.Image, "@")
	choice.Reused, choice.Reference, choice.From = true, img.Image, from
	return choice, &ReusedArtifact{
		Reference: img.Image, Digest: digest, CommitSHA: img.CommitSHA,
		FromEnvironment: from, FromDeploymentID: img.DeploymentID, Architecture: img.Architecture,
	}
}

// changedInput names the first build input that differs, or "".
func changedInput(was, now BuildInputs) string {
	for _, in := range buildInputNames {
		if in.value(was) != in.value(now) {
			return in.name
		}
	}
	return ""
}

func architecture(a string) string {
	if a == "" {
		return "an unrecorded architecture"
	}
	return a
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
