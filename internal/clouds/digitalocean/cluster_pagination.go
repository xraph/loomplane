package digitalocean

import (
	"net/url"

	"github.com/xraph/loomplane/pkg/provider"
)

type doksPageEvidence struct {
	Links struct {
		Pages struct {
			Next string `json:"next"`
			Last string `json:"last"`
		} `json:"pages"`
	} `json:"links"`
	Meta struct {
		Total *int `json:"total"`
	} `json:"meta"`
}

// doksPageState checks the provider's evidence before accepting a page as complete.
// A changing total or last page makes a multi-page inventory inconclusive.
type doksPageState struct {
	total *int
	last  int
}

func (s *doksPageState) next(e doksPageEvidence, page, count int) (string, error) {
	next, err := clusterNext(e.Links.Pages.Next, page)
	if err != nil {
		return "", err
	}
	last := 0
	if e.Links.Pages.Last != "" {
		last, err = doksLinkPage(e.Links.Pages.Last)
		if err != nil || last < page || (s.last != 0 && s.last != last) {
			return "", provider.ErrInfrastructurePagination
		}
		s.last = last
	}
	if last == 0 {
		last = s.last
	}
	if last != 0 && (last < page || (page > 1 && count == 0) || (last > page) != (next != "") || (last > page && count != pageSize)) {
		return "", provider.ErrInfrastructurePagination
	}
	if total := e.Meta.Total; total != nil {
		if *total < 0 || *total > infrastructureMaxPages*pageSize || (s.total != nil && *total != *s.total) {
			return "", provider.ErrInfrastructurePagination
		}
		s.total = total
	}
	if s.total != nil {
		total := *s.total
		offset := (page - 1) * pageSize
		if offset > total || count != min(pageSize, total-offset) {
			return "", provider.ErrInfrastructurePagination
		}
		totalLast := max(1, (total+pageSize-1)/pageSize)
		if totalLast > infrastructureMaxPages || page > totalLast || (last != 0 && last != totalLast) || ((offset+count < total) != (next != "")) {
			return "", provider.ErrInfrastructurePagination
		}
	}
	return next, nil
}
func doksLinkPage(link string) (int, error) {
	u, err := url.Parse(link)
	if err != nil {
		return 0, provider.ErrInfrastructurePagination
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(values["page"]) != 1 || values.Get("page") == "" {
		return 0, provider.ErrInfrastructurePagination
	}
	return clusterPage(values.Get("page"))
}
