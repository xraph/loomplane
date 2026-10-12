package planning

import (
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/pkg/provider"
)

// deployable is the catalog with Cloud Run and Vercel available, as they
// are once their adapters exist.
func deployable(t *testing.T) *provider.Registry {
	t.Helper()
	catalog := providers.Catalog()
	list := make([]provider.Provider, 0, len(catalog))
	for _, p := range catalog {
		if p.Name == "cloud-run" || p.Name == "vercel" {
			p.Available, p.Note = true, ""
		}
		list = append(list, p)
	}
	reg, err := provider.NewRegistry(list...)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

const webAndAPI = `version: 1
services:
  web:
    source: apps/web
    provider: vercel
    needs: [api]
  api:
    source: services/api
    provider: cloud-run
`

func findingCodes(fs []Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Code)
	}
	return out
}

func TestCompute_choosesAConnectionPerCloud(t *testing.T) {
	gcpA := ConnectionOption{ID: "conn_a", Name: "Prod", Cloud: "gcp", Ready: true}
	gcpB := ConnectionOption{ID: "conn_b", Name: "Staging", Cloud: "gcp", Ready: true}
	gcpBroken := ConnectionOption{ID: "conn_x", Name: "Old", Cloud: "gcp", Ready: false}
	web := ConnectionOption{ID: "conn_v", Name: "Web", Cloud: "vercel", Ready: true}

	cases := []struct {
		name        string
		connections []ConnectionOption
		chosen      map[string]string
		want        map[string]string
		problems    []string
	}{
		{"one ready connection per cloud", []ConnectionOption{gcpA, gcpBroken, web}, nil,
			map[string]string{"gcp": "conn_a", "vercel": "conn_v"}, []string{}},
		{"nothing connected", nil, nil, map[string]string{}, []string{"no-connection", "no-connection"}},
		{"two ready, no choice", []ConnectionOption{gcpA, gcpB, web}, nil,
			map[string]string{"vercel": "conn_v"}, []string{"connection-ambiguous"}},
		{"two ready, chosen", []ConnectionOption{gcpA, gcpB, web}, map[string]string{"gcp": "conn_b"},
			map[string]string{"gcp": "conn_b", "vercel": "conn_v"}, []string{}},
		{"chosen and removed", []ConnectionOption{gcpA, web}, map[string]string{"gcp": "conn_gone"},
			map[string]string{"vercel": "conn_v"}, []string{"connection-gone"}},
		{"chosen and broken", []ConnectionOption{gcpA, gcpBroken, web}, map[string]string{"gcp": "conn_x"},
			map[string]string{"vercel": "conn_v"}, []string{"connection-not-ready"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Compute(Input{
				Environment: "production", Graph: polyglot(t), Manifest: parse(t, webAndAPI), Providers: deployable(t),
				Connections: tc.connections, Chosen: tc.chosen,
			})
			if got := findingCodes(res.Problems); !slices.Equal(got, tc.problems) {
				t.Fatalf("problems %v, want %v: %+v", got, tc.problems, res.Problems)
			}
			if len(res.Connections) != len(tc.want) {
				t.Fatalf("connections %v, want %v", res.Connections, tc.want)
			}
			for k, v := range tc.want {
				if res.Connections[k] != v {
					t.Fatalf("connections %v, want %v", res.Connections, tc.want)
				}
			}
			if res.Executable != (len(tc.problems) == 0) {
				t.Fatalf("executable %v with problems %v", res.Executable, tc.problems)
			}
		})
	}
}

func TestCompute_unavailableTargetsNeedNoConnection(t *testing.T) {
	// With no target available, the plan is for review, and asks for no
	// connection.
	catalog := providers.Catalog()
	list := make([]provider.Provider, 0, len(catalog))
	for _, p := range catalog {
		p.Available, p.Note = false, "Not yet."
		list = append(list, p)
	}
	none, err := provider.NewRegistry(list...)
	if err != nil {
		t.Fatal(err)
	}
	res := Compute(Input{Environment: "production", Graph: polyglot(t), Manifest: parse(t, webAndAPI), Providers: none})
	if len(res.Problems) != 0 || len(res.Connections) != 0 || res.Executable {
		t.Fatalf("problems %+v, connections %v, executable %v", res.Problems, res.Connections, res.Executable)
	}
}

func TestCompute_aResourceOnAnotherCloudIsUnreachable(t *testing.T) {
	const manifest = `version: 1
services:
  web:
    source: apps/web
    provider: vercel
    needs: [db]
  api:
    source: services/api
    provider: cloud-run
    needs: [db]
resources:
  db:
    kind: postgres
    provider: cloud-sql
`
	res := Compute(Input{Environment: "production", Graph: polyglot(t), Manifest: parse(t, manifest), Providers: deployable(t),
		Connections: []ConnectionOption{{ID: "g", Cloud: "gcp", Ready: true}, {ID: "v", Cloud: "vercel", Ready: true}}})
	if got := findingCodes(res.Problems); !slices.Equal(got, []string{"unreachable-resource"}) || res.Problems[0].Name != "web" {
		t.Fatalf("problems %+v", res.Problems)
	}
}
