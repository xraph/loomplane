// Package engines knows the inference servers Loomplane deploys models
// with: the release of each one it pins, and what that release accepts.
// The flag, parser and architecture lists under testdata/ are captured from
// the pinned images by scripts/capture-model-engines.sh, and the images
// themselves are pinned by digest in
// internal/recipes/testdata/model-engine-image-digests.json.
package engines

import (
	"slices"

	"github.com/xraph/loomplane/internal/models/registry"
)

// Release is one engine version Loomplane ships.
type Release struct {
	Engine  registry.Engine
	Version string // manifest version of the model kind, "0.31"
	Product string // the engine's own version, "0.31.0"; what qualification results compare
}

// releases moves with the tags in scripts/capture-model-engines.sh. When a
// version changes here, rerun the script and rename the testdata files.
var releases = []Release{
	{Engine: registry.EngineVLLM, Version: "0.31", Product: "0.31.0"},
	{Engine: registry.EngineTEI, Version: "1.9", Product: "1.9.4"},
	{Engine: registry.EngineLlamaCPP, Version: "11580", Product: "b11580"},
}

// Releases returns the pinned release of every engine.
func Releases() []Release {
	return slices.Clone(releases)
}

// Current maps each engine to the Product version Loomplane ships, which is
// what registry.FitOn needs to tell a tested fit from an estimated one.
func Current() map[registry.Engine]string {
	out := make(map[registry.Engine]string, len(releases))
	for _, r := range releases {
		out[r.Engine] = r.Product
	}
	return out
}
