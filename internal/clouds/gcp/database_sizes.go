package gcp

import (
	"context"
	"net/http"
	"slices"
)

func (s *Session) DatabaseTiers(ctx context.Context, region string) ([]string, error) {
	var out struct {
		Items []struct {
			Tier   string   `json:"tier"`
			Region []string `json:"region"`
		} `json:"items"`
	}
	if err := s.do(ctx, http.MethodGet, s.sqlPath("/tiers"), nil, &out); err != nil {
		return nil, err
	}
	sizes := []string{}
	for _, item := range out.Items {
		if region == "" || slices.Contains(item.Region, region) {
			sizes = append(sizes, item.Tier)
		}
	}
	return sizes, nil
}
