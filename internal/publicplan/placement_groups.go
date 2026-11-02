package publicplan

import domain "github.com/xraph/loomplane/pkg/federation"

// PlacementGroupReview exposes topology intent without private attachment,
// authority, grant or account pins. Execution remains unavailable.
type PlacementGroupReview struct {
	GroupID     string                       `json:"group_id"`
	Generation  int64                        `json:"generation"`
	Revision    string                       `json:"revision"`
	Name        string                       `json:"name"`
	Service     string                       `json:"service"`
	Image       string                       `json:"image"`
	MinReady    int                          `json:"min_ready"`
	RoutingMode string                       `json:"routing_mode"`
	Hostname    string                       `json:"hostname"`
	Members     []PlacementGroupMemberReview `json:"members"`
}
type PlacementGroupMemberReview struct {
	MemberID string `json:"member_id"`
	Replicas int    `json:"replicas"`
}

func placementGroupReviews(groups []domain.QualifiedGroupPlan) []PlacementGroupReview {
	if len(groups) == 0 {
		return nil
	}
	result := make([]PlacementGroupReview, 0, len(groups))
	for _, g := range groups {
		item := PlacementGroupReview{GroupID: g.GroupID, Generation: g.Generation, Revision: g.Revision, Name: g.Name, Service: g.Service, Image: g.Image, MinReady: g.MinReady, RoutingMode: g.Routing.Mode, Hostname: g.Routing.Hostname, Members: make([]PlacementGroupMemberReview, 0, len(g.Members))}
		for _, m := range g.Members {
			item.Members = append(item.Members, PlacementGroupMemberReview{MemberID: m.Identity.MemberID, Replicas: m.Replicas})
		}
		result = append(result, item)
	}
	return result
}
