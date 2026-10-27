package projects

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/security"
)

// Exclusion kinds, the same names graph.ExclusionRule uses.
const (
	ExclusionService = "service"
	ExclusionPath    = "path"
)

// Exclusion reasons the dashboard offers. Other is anything else.
const (
	ReasonTooling   = "tooling"
	ReasonFixture   = "fixture"
	ReasonElsewhere = "elsewhere"
	ReasonOther     = "other"
)

// maxExclusions bounds a project's exclusions, so a runaway client can't
// grow the row without end.
const maxExclusions = 200

// Exclusion keeps analysis-found code out of a project: out of its graph,
// its plans and its deploys, on every branch. It's a project setting, so
// nothing in the repository changes. A service exclusion names one
// service by its graph ID; a path exclusion names a directory glob, so a
// service added under it later is excluded too.
type Exclusion struct {
	Kind  string `json:"kind"`
	Match string `json:"match"`
	// Reason is one of the Reason constants.
	Reason string `json:"reason"`
	// Name is the service's name when it was excluded, for display. A
	// path exclusion has none.
	Name string `json:"name,omitempty"`
	// KeepRunning leaves a service that's already deployed running as it
	// is. Without it, the next plan removes it.
	KeepRunning bool      `json:"keep_running"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
}

// ExclusionInput is one exclusion to add.
type ExclusionInput struct {
	Kind        string `json:"kind"`
	Match       string `json:"match"`
	Reason      string `json:"reason"`
	Name        string `json:"name,omitempty"`
	KeepRunning bool   `json:"keep_running,omitempty"`
}

// ExcludeInput adds exclusions to a project. One that's already there,
// by kind and match, is updated in place.
type ExcludeInput struct {
	Exclusions []ExclusionInput `json:"exclusions"`
}

// IncludeInput removes exclusions from a project, by kind and match.
// One that isn't there is ignored.
type IncludeInput struct {
	Exclusions []ExclusionRef `json:"exclusions"`
}

// ExclusionRef names an exclusion by kind and match.
type ExclusionRef struct {
	Kind  string `json:"kind"`
	Match string `json:"match"`
}

func (in ExclusionInput) normalize() (ExclusionInput, error) {
	in.Kind = strings.TrimSpace(in.Kind)
	in.Match = strings.TrimSpace(in.Match)
	in.Reason = strings.TrimSpace(in.Reason)
	in.Name = strings.TrimSpace(in.Name)
	switch in.Kind {
	case ExclusionService:
		if in.Match == "" || len(in.Match) > 512 {
			return in, fmt.Errorf("%w: a service exclusion names a service ID of 1 to 512 characters", ErrInvalid)
		}
	case ExclusionPath:
		in.Match = strings.Trim(path.Clean(in.Match), "/")
		if in.Match == "" || in.Match == "." || strings.HasPrefix(in.Match, "..") || len(in.Match) > 512 {
			return in, fmt.Errorf("%w: a path exclusion names a directory in the repository, such as tests/acceptance/**", ErrInvalid)
		}
		if _, err := path.Match(in.Match, ""); err != nil {
			return in, fmt.Errorf("%w: %q isn't a valid path pattern", ErrInvalid, in.Match)
		}
		in.Name, in.KeepRunning = "", false
	default:
		return in, fmt.Errorf("%w: an exclusion's kind is service or path", ErrInvalid)
	}
	switch in.Reason {
	case "":
		in.Reason = ReasonOther
	case ReasonTooling, ReasonFixture, ReasonElsewhere, ReasonOther:
	default:
		return in, fmt.Errorf("%w: an exclusion's reason is tooling, fixture, elsewhere or other", ErrInvalid)
	}
	if len(in.Name) > 200 {
		return in, fmt.Errorf("%w: a service name is at most 200 characters", ErrInvalid)
	}
	return in, nil
}

// Exclude adds exclusions to a project.
func (s *Service) Exclude(ctx context.Context, p security.Principal, id string, in ExcludeInput) (*Project, error) {
	if err := s.authz.Require(ctx, p, security.ProjectUpdate, id); err != nil {
		return nil, err
	}
	if len(in.Exclusions) == 0 {
		return nil, fmt.Errorf("%w: name at least one service or path to exclude", ErrInvalid)
	}
	adds := make([]ExclusionInput, 0, len(in.Exclusions))
	for _, x := range in.Exclusions {
		n, err := x.normalize()
		if err != nil {
			return nil, err
		}
		adds = append(adds, n)
	}
	now := s.now().UTC()
	proj, err := s.store.UpdateExclusions(ctx, p.OrgID, id, func(list []Exclusion) ([]Exclusion, error) {
		for _, a := range adds {
			x := Exclusion{Kind: a.Kind, Match: a.Match, Reason: a.Reason, Name: a.Name, KeepRunning: a.KeepRunning, CreatedBy: p.UserID, CreatedAt: now}
			if i := slices.IndexFunc(list, func(e Exclusion) bool { return e.Kind == a.Kind && e.Match == a.Match }); i >= 0 {
				x.CreatedBy, x.CreatedAt = list[i].CreatedBy, list[i].CreatedAt
				list[i] = x
				continue
			}
			list = append(list, x)
		}
		if len(list) > maxExclusions {
			return nil, fmt.Errorf("%w: a project has at most %d exclusions; use a path rule for many services", ErrInvalid, maxExclusions)
		}
		return list, nil
	})
	if err != nil {
		return nil, err
	}
	s.record(ctx, AuditEvent{Action: "project.exclude", Severity: "info", ProjectID: id, Principal: p,
		Attributes: map[string]any{"exclusions": refs(adds)}})
	return proj, nil
}

// Include removes exclusions from a project.
func (s *Service) Include(ctx context.Context, p security.Principal, id string, in IncludeInput) (*Project, error) {
	if err := s.authz.Require(ctx, p, security.ProjectUpdate, id); err != nil {
		return nil, err
	}
	if len(in.Exclusions) == 0 {
		return nil, fmt.Errorf("%w: name at least one exclusion to remove", ErrInvalid)
	}
	proj, err := s.store.UpdateExclusions(ctx, p.OrgID, id, func(list []Exclusion) ([]Exclusion, error) {
		return slices.DeleteFunc(list, func(e Exclusion) bool {
			return slices.ContainsFunc(in.Exclusions, func(r ExclusionRef) bool {
				return r.Kind == e.Kind && strings.TrimSpace(r.Match) == e.Match
			})
		}), nil
	})
	if err != nil {
		return nil, err
	}
	s.record(ctx, AuditEvent{Action: "project.include", Severity: "info", ProjectID: id, Principal: p,
		Attributes: map[string]any{"exclusions": in.Exclusions}})
	return proj, nil
}

// ExclusionRules are the project's exclusions as graph.Exclude takes them.
func (p *Project) ExclusionRules() []graph.ExclusionRule {
	out := make([]graph.ExclusionRule, len(p.Exclusions))
	for i, x := range p.Exclusions {
		out[i] = graph.ExclusionRule{Kind: x.Kind, Match: x.Match}
	}
	return out
}

func refs(in []ExclusionInput) []ExclusionRef {
	out := make([]ExclusionRef, len(in))
	for i, x := range in {
		out[i] = ExclusionRef{Kind: x.Kind, Match: x.Match}
	}
	return out
}
