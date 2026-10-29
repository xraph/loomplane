package provider_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

func TestClusterCredentialsNeverSerializeOrFormat(t *testing.T) {
	values := []any{
		provider.AccountAccess{Provider: "sentinel-provider", AccountID: "sentinel-account", ConnectionID: "sentinel-connection", Generation: 987654321, Credential: []byte("sentinel-credential")},
		provider.BootstrapAccess{Endpoint: "sentinel-endpoint", CAData: []byte("sentinel-ca"), Token: "sentinel-token", ClientCertificate: []byte("sentinel-certificate"), ClientKey: []byte("sentinel-key"), ExpiresAt: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, value := range values {
		var pointer any
		switch access := value.(type) {
		case provider.AccountAccess:
			pointer = &access
		case provider.BootstrapAccess:
			pointer = &access
		}
		encoded, err := json.Marshal(value)
		if err != nil || string(encoded) != "{}" {
			t.Fatalf("private JSON: %s, %v", encoded, err)
		}
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%#x", "%d"} {
			for _, input := range []any{value, pointer, struct{ Access any }{value}, struct{ Access any }{pointer}} {
				output := fmt.Sprintf(format, input)
				if !strings.Contains(output, "access]") || strings.Contains(output, "sentinel") || strings.Contains(output, "987654321") || strings.Contains(output, "2099") {
					t.Fatalf("private format %s: %s", format, output)
				}
			}
		}
	}
}
