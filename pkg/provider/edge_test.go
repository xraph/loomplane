package provider

import "testing"

func TestEdgePolicyValidation(t *testing.T) {
	for _, path := range []string{"/assets/", "/api/public", "/"} {
		if !ValidEdgePath(path) {
			t.Error(path)
		}
	}
	for _, path := range []string{"//evil", "/../admin", "/%2fadmin", "/x\" or true", "/foo?bar", "/x\n"} {
		if ValidEdgePath(path) {
			t.Error(path)
		}
	}
	for _, r := range []EdgeRule{{PathPrefix: "/", Mode: "public", EdgeTTL: 59}, {PathPrefix: "/", Mode: "public", EdgeTTL: 3600, BrowserTTL: 4000}, {PathPrefix: "/", Mode: "origin", EdgeTTL: 60}, {PathPrefix: "/", Mode: "oops"}} {
		if (EdgePolicy{Rules: []EdgeRule{r}}).Validate() == nil {
			t.Error(r)
		}
	}
	for _, raw := range []string{"https://api.example.com:443/x", "https://user@api.example.com/x", "https://api.example.com/x#secret", "//api.example.com/x", "https://other.example.com/x"} {
		if ValidateEdgePurge("api.example.com", []string{raw}) == nil {
			t.Error(raw)
		}
	}
}
