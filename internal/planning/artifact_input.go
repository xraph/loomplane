package planning

import (
	"fmt"

	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/pkg/fleet"
)

// ArtifactSnapshot carries the complete published release, without repository inputs.
// Authorization and scoped target selection belong to the instance planner entry point.
type ArtifactSnapshot struct {
	ReleaseID string
	Digest    string
	Template  fleet.Template
}

// ArtifactInput constructs explicit service identities and keeps source projects locked.
func ArtifactInput(snapshot ArtifactSnapshot) (Input, error) {
	if snapshot.ReleaseID == "" {
		return Input{}, fmt.Errorf("%w: missing release identity", fleet.ErrTemplate)
	}
	digest, err := fleet.CanonicalDigest(&snapshot.Template)
	if err != nil {
		return Input{}, err
	}
	if digest != snapshot.Digest {
		return Input{}, fmt.Errorf("%w: release digest mismatch", fleet.ErrTemplate)
	}
	m, err := snapshot.Template.ArtifactManifest()
	if err != nil {
		return Input{}, err
	}
	g := &graph.Graph{Version: graph.SchemaVersion}
	for _, name := range m.ServiceNames() {
		s := m.Services[name]
		service := graph.Service{ID: "svc:" + s.Source, Name: name, Kind: graph.ServiceKind(s.Kind), Deployable: true, Ecosystem: "docker", Path: s.Source, Entrypoint: s.Source}
		if s.Port > 0 {
			service.Ports = []int{s.Port}
		}
		g.Services = append(g.Services, service)
	}
	return Input{Manifest: m, Graph: g}, nil
}
