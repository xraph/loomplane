package federation

import "testing"

func TestExactProtocolSelectors(t *testing.T) {
	for _, path := range []string{"/", "/orders", "/v1/order-items/", "/a_b~c+d.json"} {
		if !ValidHTTPPath(path) {
			t.Errorf("valid path rejected: %q", path)
		}
	}
	for _, path := range []string{"", "orders", "//orders", "/a//b", "/a/../b", "/./orders", "/%61dmin", "/orders?all=1", "/orders#x", "/orders/*", "/a\\b"} {
		if ValidHTTPPath(path) {
			t.Errorf("ambiguous path accepted: %q", path)
		}
	}
	if !ValidHTTPMethod("GET") || ValidHTTPMethod("get") || ValidHTTPMethod("*") {
		t.Fatal("method validation")
	}
	if !ValidGRPCMethod("/shop.Orders/Get") || ValidGRPCMethod("/shop.Orders/*") || ValidGRPCMethod("/shop..Orders/Get") {
		t.Fatal("grpc validation")
	}
}
