package planning

import (
	"net/http"
	"testing"
)

func TestCloudLocationRoutes(t *testing.T) {
	routes := (&Extension{}).cloudLocationRoutes()
	want := map[string]string{"cloudLocationPolicy.get": http.MethodGet, "cloudLocationPolicy.save": http.MethodPut, "cloudLocations.delete": http.MethodDelete, "cloudLocations.verify": http.MethodPost, "cloudLocations.list": http.MethodGet, "cloudLocations.save": http.MethodPost, "cloudLocations.get": http.MethodGet, "projectCloudLocations.list": http.MethodGet, "projectCloudLocations.get": http.MethodGet}
	for _, route := range routes {
		if method, ok := want[route.name]; !ok || method != route.method || route.handler == nil {
			t.Fatalf("invalid location route: %+v", route)
		}
		delete(want, route.name)
	}
	if len(want) != 0 {
		t.Fatalf("missing routes: %v", want)
	}
}
