package digitalocean

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

func TestDOKSPaginationEvidence(t *testing.T) {
	for _, resource := range []string{"clusters", "sizes", "vpcs"} {
		t.Run(resource, func(t *testing.T) {
			for _, tc := range []struct {
				name     string
				count    int
				evidence string
				invalid  bool
			}{
				{"missing-next-last", 0, `"links":{"pages":{"last":"?page=2"}}`, true},
				{"missing-next-total", 0, `"meta":{"total":201}`, true},
				{"missing-next-both", 0, `"links":{"pages":{"last":"?page=2"}},"meta":{"total":201}`, true},
				{"total-under-count", 1, `"meta":{"total":0}`, true},
				{"negative-total", 0, `"meta":{"total":-1}`, true},
				{"next-after-last", 1, `"links":{"pages":{"last":"?page=1","next":"?page=2"}}`, true},
				{"last-total-disagree", 1, `"links":{"pages":{"last":"?page=2","next":"?page=2"}},"meta":{"total":1}`, true},
				{"short-page", 1, `"links":{"pages":{"last":"?page=2","next":"?page=2"}},"meta":{"total":201}`, true},
				{"invalid-last", 0, `"links":{"pages":{"last":"?page=0"}}`, true},
				{"empty-proven", 0, `"links":{"pages":{"last":"?page=1"}},"meta":{"total":0}`, false},
				{"one-proven", 1, `"links":{"pages":{"last":"?page=1"}},"meta":{"total":1}`, false},
				{"optional-absent", 0, `"links":{}`, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					c := doksFixture(t, func(w http.ResponseWriter, r *http.Request) {
						switch r.URL.Path {
						case "/v2/kubernetes/options":
							_, _ = fmt.Fprint(w, `{"options":{"regions":[{"slug":"nyc1"}],"versions":[{"slug":"v"}],"sizes":[{"slug":"doks"}]}}`)
							return
						case "/v2/sizes":
							if resource != "sizes" {
								_, _ = fmt.Fprint(w, `{"sizes":[]}`)
								return
							}
						case "/v2/vpcs":
							if resource != "vpcs" {
								_, _ = fmt.Fprint(w, `{"vpcs":[]}`)
								return
							}
						}
						key := resource
						row := map[string]any{"id": "item", "region": "nyc1"}
						if resource == "clusters" {
							key = "kubernetes_clusters"
							row["created_at"] = "2026-01-01T00:00:00Z"
							row["status"] = map[string]string{"state": "running"}
						}
						if resource == "sizes" {
							row["slug"] = "doks"
						}
						rows := []map[string]any{}
						for i := 0; i < tc.count; i++ {
							rows = append(rows, row)
						}
						body, _ := json.Marshal(rows)
						_, _ = fmt.Fprintf(w, `{"%s":%s,%s}`, key, body, tc.evidence)
					})
					var err error
					if resource == "clusters" {
						_, err = c.ListClusters(t.Context(), doksAccess(), "")
					} else {
						_, err = c.Options(t.Context(), doksAccess(), "nyc1")
					}
					if tc.invalid && !errors.Is(err, provider.ErrInfrastructurePagination) {
						t.Fatalf("contradiction accepted: %v", err)
					}
					if !tc.invalid && err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}

func TestDOKSOptionsPaginationEvidenceAcrossPages(t *testing.T) {
	for _, resource := range []string{"sizes", "vpcs"} {
		t.Run(resource, func(t *testing.T) {
			for _, tc := range []struct {
				name, evidence string
				invalid        bool
			}{
				{"stable", `"links":{"pages":{"last":"?page=2"}},"meta":{"total":201}`, false},
				{"optional-omitted", `"links":{}`, false},
				{"empty-final", `"links":{"pages":{"last":"?page=2"}}`, true},
				{"total-changed", `"links":{"pages":{"last":"?page=2"}},"meta":{"total":202}`, true},
				{"last-changed", `"links":{"pages":{"last":"?page=3","next":"?page=3"}},"meta":{"total":201}`, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					c := doksFixture(t, func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == "/v2/kubernetes/options" {
							_, _ = fmt.Fprint(w, `{"options":{"regions":[{"slug":"nyc1"}],"versions":[{"slug":"v"}],"sizes":[{"slug":"doks"}]}}`)
							return
						}
						if r.URL.Path != "/v2/"+resource {
							_, _ = fmt.Fprint(w, `{"sizes":[],"vpcs":[]}`)
							return
						}
						count := 1
						if tc.name == "empty-final" {
							count = 0
						}
						offset := 200
						evidence := tc.evidence
						if r.URL.Query().Get("page") == "1" {
							count = 200
							offset = 0
							evidence = `"links":{"pages":{"next":"?page=2","last":"?page=2"}},"meta":{"total":201}`
							if tc.name == "empty-final" {
								evidence = `"links":{"pages":{"next":"?page=2","last":"?page=2"}}`
							}
						}
						rows := []map[string]any{}
						for i := 0; i < count; i++ {
							id := fmt.Sprint(offset + i)
							rows = append(rows, map[string]any{"id": id, "slug": id, "region": "nyc1"})
						}
						body, _ := json.Marshal(rows)
						_, _ = fmt.Fprintf(w, `{"%s":%s,%s}`, resource, body, evidence)
					})
					_, err := c.Options(t.Context(), doksAccess(), "nyc1")
					if tc.invalid && !errors.Is(err, provider.ErrInfrastructurePagination) {
						t.Fatalf("changed evidence accepted: %v", err)
					}
					if !tc.invalid && err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}
