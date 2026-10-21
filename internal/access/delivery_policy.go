package access

import (
	"slices"
	"time"

	domain "github.com/xraph/loomplane/pkg/federation"
)

// CompileForAgent includes only outbound grants from this destination and inbound
// grants to its exports. Other destinations in the same environment stay private.
func CompileForAgent(input domain.PolicyInput, agent domain.AgentIdentity) (domain.PolicyBundle, error) {
	if input.Scope != agent.Scope || !domain.ValidIdentitySegment(agent.DestinationID) {
		return domain.PolicyBundle{}, ErrPolicy
	}
	if input.EvaluatedAt.IsZero() {
		input.EvaluatedAt = time.Now().UTC()
	}
	compiled, err := Compile(input)
	if err != nil {
		return domain.PolicyBundle{}, err
	}
	targets := make(map[string]bool, len(compiled.Exports))
	for _, export := range compiled.Exports {
		targets[export.ID] = export.Scope == agent.Scope && slices.Contains(export.DestinationIDs, agent.DestinationID)
	}
	input.Exports = compiled.Exports
	input.Grants = nil
	for _, grant := range compiled.Grants {
		if grant.Source.Scope == agent.Scope && grant.Source.DestinationID == agent.DestinationID || targets[grant.ExportID] {
			input.Grants = append(input.Grants, grant)
		}
	}
	return Compile(input)
}
