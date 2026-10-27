package digitalocean

import (
	"net/http"
	"testing"
)

func TestServerOptionsUsesAccountScopedProviderCatalog(t *testing.T) {
	f, c := newFake(t)
	infrastructureAccount(f)
	f.on("GET /v2/regions", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"regions": []any{map[string]any{"slug": "nyc3", "name": "New York 3", "available": true}, map[string]any{"slug": "closed", "available": false}}})
	})
	f.on("GET /v2/sizes", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"sizes": []any{map[string]any{"slug": "s-1vcpu-2gb", "vcpus": 1, "memory": 2048, "disk": 50, "price_hourly": 0.01786, "price_monthly": 12, "regions": []string{"nyc3"}, "available": true}}})
	})
	f.on("GET /v2/images", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.URL.Query().Get("type") != "distribution" {
			t.Error("unscoped image catalog")
		}
		reply(w, 200, map[string]any{"images": []any{map[string]any{"slug": "ubuntu-24-04-x64", "name": "Ubuntu 24.04", "distribution": "Ubuntu", "public": true, "regions": []string{"nyc3"}}, map[string]any{"slug": "unsupported", "distribution": "Other", "public": true, "regions": []string{"nyc3"}}}})
	})
	options, err := c.ServerOptions(t.Context(), infrastructureCredential())
	if err != nil {
		t.Fatal(err)
	}
	if len(options.Regions) != 1 || len(options.Sizes) != 1 || len(options.Images) != 2 || !options.Images[0].Bootstrap || options.Images[1].Bootstrap || options.Sizes[0].HourlyUSD != 0.01786 || options.Sizes[0].MemoryMiB != 2048 || !options.Sizes[0].HourlyPriceKnown || options.Images[0].Architecture != "amd64" {
		t.Fatalf("catalog: %+v", options)
	}
}
