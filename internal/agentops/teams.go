package agentops

import (
	"context"
	"errors"

	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/security"
	defs "github.com/xraph/loomplane/pkg/agentops"
)

type (
	teamsOutput struct {
		Teams []teamOutput `json:"teams"`
	}
	// teamOutput is what the model reads about a team: enough to name one
	// in a request, and never who is in it.
	teamOutput struct {
		Slug        string `json:"slug"`
		Name        string `json:"name"`
		Description string `json:"description"`
		MemberCount int    `json:"member_count"`
	}
)

// errNoTeams answers list_teams on a server that didn't wire teams.
var errNoTeams = errors.New("teams can't be read here")

func registerTeams(r *Registry, d *Deps) {
	Register(r, def("list_teams"), security.MemberRead, func(ctx context.Context, call Call, _ defs.NoInput) (teamsOutput, error) {
		if d.Teams == nil {
			return teamsOutput{}, errNoTeams
		}
		list, err := d.Teams.List(ctx, call.Principal)
		if err != nil {
			return teamsOutput{}, err
		}
		out := teamsOutput{Teams: make([]teamOutput, 0, len(list.Teams))}
		for _, t := range list.Teams {
			out.Teams = append(out.Teams, teamOutput{
				Slug: t.Slug, Name: t.Name,
				// A person typed it, so it reaches the model as quoted text.
				Description: d.quote(ctx, agentsafety.SourceTeamDescription, t.Description),
				MemberCount: len(t.Members),
			})
		}
		return out, nil
	})
}
