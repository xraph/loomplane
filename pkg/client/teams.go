package client

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// --- Teams ---

// Team is a named group of workspace members, with the roles it gives.
type Team struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Slug        string       `json:"slug"`
	Description string       `json:"description"`
	Members     []TeamMember `json:"members"`
	Grants      []TeamGrant  `json:"grants"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// TeamMember is a person in a team.
type TeamMember struct {
	UserID  string    `json:"user_id"`
	Email   string    `json:"email"`
	Name    string    `json:"name"`
	AddedAt time.Time `json:"added_at"`
}

// TeamGrant is the role a team gives its members on one project, or on the
// whole workspace when ProjectID is empty.
type TeamGrant struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	ProjectID string `json:"project_id"`
}

// Member is a person in the workspace.
type Member struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
	Role   string `json:"role"`
}

// Teams lists the workspace's teams.
func (c *Client) Teams(ctx context.Context) ([]Team, error) {
	var out struct {
		Teams []Team `json:"teams"`
	}
	return out.Teams, c.do(ctx, http.MethodGet, "/api/v1/teams", nil, &out)
}

// Team returns one team by ID.
func (c *Client) Team(ctx context.Context, id string) (*Team, error) {
	var t Team
	return &t, c.do(ctx, http.MethodGet, "/api/v1/teams/"+esc(id), nil, &t)
}

// FindTeam finds a team by its ID, slug or name.
func (c *Client) FindTeam(ctx context.Context, ref string) (*Team, error) {
	teams, err := c.Teams(ctx)
	if err != nil {
		return nil, err
	}
	var byName []Team
	for _, t := range teams {
		if t.ID == ref || t.Slug == ref {
			return &t, nil
		}
		if strings.EqualFold(t.Name, ref) {
			byName = append(byName, t)
		}
	}
	switch len(byName) {
	case 1:
		return &byName[0], nil
	case 0:
		return nil, &Error{Status: http.StatusNotFound, Message: fmt.Sprintf("no team is called %q", ref)}
	}
	return nil, &Error{Status: http.StatusConflict, Message: fmt.Sprintf("more than one team is called %q; use its slug or ID", ref)}
}

// CreateTeam creates a team.
func (c *Client) CreateTeam(ctx context.Context, name, description string) (*Team, error) {
	in := struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
	}{name, description}
	var t Team
	return &t, c.do(ctx, http.MethodPost, "/api/v1/teams", in, &t)
}

// UpdateTeam changes the fields it is given.
func (c *Client) UpdateTeam(ctx context.Context, id string, name, description *string) (*Team, error) {
	in := struct {
		Name        *string `json:"name,omitempty"`
		Description *string `json:"description,omitempty"`
	}{name, description}
	var t Team
	return &t, c.do(ctx, http.MethodPatch, "/api/v1/teams/"+esc(id), in, &t)
}

// DeleteTeam deletes a team. Its members lose the roles it gave.
func (c *Client) DeleteTeam(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/teams/"+esc(id), nil, nil)
}

// AddTeamMember puts a workspace member in a team.
func (c *Client) AddTeamMember(ctx context.Context, teamID, userID string) (*Team, error) {
	var t Team
	return &t, c.do(ctx, http.MethodPut, "/api/v1/teams/"+esc(teamID)+"/members/"+esc(userID), nil, &t)
}

// RemoveTeamMember takes a person out of a team.
func (c *Client) RemoveTeamMember(ctx context.Context, teamID, userID string) (*Team, error) {
	var t Team
	return &t, c.do(ctx, http.MethodDelete, "/api/v1/teams/"+esc(teamID)+"/members/"+esc(userID), nil, &t)
}

// SetTeamGrant gives a team a role on a project, or on the whole workspace
// when projectID is empty. A scope that has a grant gets the new role.
func (c *Client) SetTeamGrant(ctx context.Context, teamID, role, projectID string) (*Team, error) {
	in := struct {
		Role      string `json:"role"`
		ProjectID string `json:"project_id,omitempty"`
	}{role, projectID}
	var t Team
	return &t, c.do(ctx, http.MethodPut, "/api/v1/teams/"+esc(teamID)+"/grants", in, &t)
}

// RemoveTeamGrant removes one of a team's grants.
func (c *Client) RemoveTeamGrant(ctx context.Context, teamID, grantID string) (*Team, error) {
	var t Team
	return &t, c.do(ctx, http.MethodDelete, "/api/v1/teams/"+esc(teamID)+"/grants/"+esc(grantID), nil, &t)
}

// SetProjectTeam names the team responsible for a project, or clears it
// when teamID is nil.
func (c *Client) SetProjectTeam(ctx context.Context, projectID string, teamID *string) (*Project, error) {
	in := struct {
		TeamID *string `json:"team_id"`
	}{teamID}
	var p Project
	return &p, c.do(ctx, http.MethodPut, "/api/v1/projects/"+esc(projectID)+"/team", in, &p)
}

// ProjectPermissions lists what the caller may do on one project, team
// grants included.
func (c *Client) ProjectPermissions(ctx context.Context, projectID string) ([]string, error) {
	var out struct {
		Permissions []string `json:"permissions"`
	}
	return out.Permissions, c.do(ctx, http.MethodGet, "/api/v1/projects/"+esc(projectID)+"/permissions", nil, &out)
}

// Members lists the people in the workspace.
func (c *Client) Members(ctx context.Context) ([]Member, error) {
	var out struct {
		Members []Member `json:"members"`
	}
	return out.Members, c.do(ctx, http.MethodGet, "/api/v1/members", nil, &out)
}
